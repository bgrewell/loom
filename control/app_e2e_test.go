// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/metrics"

	// The app plane resolves engines from the registries; register the voip
	// pair the way a stock loomd does.
	_ "github.com/bgrewell/loom/core/app/voip"
)

// TestAppVoipE2E drives a full VoIP app pair through the gRPC control plane:
// an APP_SERVER agent binds the voip answerer and reports its data_port, an
// APP_CLIENT agent calls it over the host network, boundary telemetry carries
// VoipMetrics with a sane MOS, and stopping the client tears the call down
// with an RTCP BYE the server observes.
func TestAppVoipE2E(t *testing.T) {
	srv := startAgent(t, "app-server")
	defer srv.stop()
	cli := startAgent(t, "app-client")
	defer cli.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Server: voip answerer, duration-bounded (mandatory for APP_SERVER).
	srvCfg, err := srv.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_APP_SERVER, App: "voip",
		Duration: durationpb.New(30 * time.Second),
		Params:   map[string]string{"codec": "pcmu"},
	}})
	if err != nil {
		t.Fatalf("app server Configure: %v", err)
	}
	if srvCfg.GetDataPort() == 0 {
		t.Fatalf("app server Configure returned no data_port: %+v", srvCfg)
	}
	if _, err := srv.client.Start(ctx, &loomv1.StartRequest{FlowId: srvCfg.GetFlowId()}); err != nil {
		t.Fatalf("app server Start: %v", err)
	}

	// Client: call the server's advertised port over the host network.
	target := fmt.Sprintf("127.0.0.1:%d", srvCfg.GetDataPort())
	cliCfg, err := cli.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_APP_CLIENT, App: "voip", Target: target,
		Duration: durationpb.New(30 * time.Second),
		Params:   map[string]string{"codec": "pcmu"},
	}})
	if err != nil {
		t.Fatalf("app client Configure: %v", err)
	}
	if _, err := cli.client.Start(ctx, &loomv1.StartRequest{
		FlowId: cliCfg.GetFlowId(), ReportIntervalNanos: (100 * time.Millisecond).Nanoseconds(),
	}); err != nil {
		t.Fatalf("app client Start: %v", err)
	}

	// Boundary samples must carry the AppMetrics voip oneof; wait for one that
	// has received media and been scored, then stop the call.
	stream, err := cli.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: cliCfg.GetFlowId()})
	if err != nil {
		t.Fatalf("app client StreamTelemetry: %v", err)
	}
	var scored *loomv1.VoipMetrics
	stopped := false
	for {
		s, err := stream.Recv()
		if err != nil {
			break // stream ends after the final sample once the flow stops
		}
		v := s.GetApp().GetVoip()
		if v == nil {
			t.Fatalf("boundary sample carries no VoipMetrics: %+v", s)
		}
		if !stopped && v.GetRxPackets() > 0 && v.GetMosCq() > 0 {
			scored = v
			if _, err := cli.client.Stop(ctx, &loomv1.StopRequest{FlowId: cliCfg.GetFlowId()}); err != nil {
				t.Fatalf("app client Stop: %v", err)
			}
			stopped = true
		}
	}
	if scored == nil {
		t.Fatal("no scored VoipMetrics sample before the stream ended")
	}
	if mos := scored.GetMosCq(); mos < 1.0 || mos > 5.0 {
		t.Fatalf("MOS-CQ %v out of range [1, 5]", mos)
	}
	if r := scored.GetRFactor(); r <= 0 {
		t.Fatalf("R factor %v not positive", r)
	}
	if scored.GetEmodel() == nil || scored.GetEmodel().GetRo() <= 0 {
		t.Fatalf("E-model breakdown missing: %+v", scored.GetEmodel())
	}
	// OWD provenance must flow through: with no OffsetProvider at this layer
	// the label is "none" until an RTCP round trip yields the RTT/2 fallback —
	// never a bare number posing as measured.
	if m := scored.GetOwdMethod(); m != "none" && m != "rtt/2" {
		t.Fatalf("owd_method %q, want none or rtt/2", m)
	}
	t.Logf("scored sample: MOS-CQ %.2f R %.1f jitter %.3fms rx %d owd_method %s",
		scored.GetMosCq(), scored.GetRFactor(), scored.GetJitterMs(), scored.GetRxPackets(), scored.GetOwdMethod())

	// The client's teardown sends an RTCP BYE; the server session must observe
	// it. The wire does not carry RemoteBye, so read the server engine's
	// snapshot through its managed runner (in-package access). Poll via
	// CumulativeMetrics: unlike Metrics() it closes no observation interval,
	// so this extra consumer cannot corrupt the telemetry stream's per-interval
	// loss/discard accounting (the second-subscriber hazard appRunner.metricsAt
	// serializes against).
	mf, ok := srv.srv.mgr.get(srvCfg.GetFlowId())
	if !ok {
		t.Fatalf("server flow %q not found", srvCfg.GetFlowId())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, _ := mf.run.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.VoIP)
		if snap.RemoteBye {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never observed the client's RTCP BYE: %+v", snap)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := srv.client.Stop(ctx, &loomv1.StopRequest{FlowId: srvCfg.GetFlowId()}); err != nil {
		t.Fatalf("app server Stop: %v", err)
	}
	if _, err := srv.client.Destroy(ctx, &loomv1.DestroyRequest{FlowId: srvCfg.GetFlowId()}); err != nil {
		t.Fatalf("app server Destroy: %v", err)
	}
	if _, err := cli.client.Destroy(ctx, &loomv1.DestroyRequest{FlowId: cliCfg.GetFlowId()}); err != nil {
		t.Fatalf("app client Destroy: %v", err)
	}
}

// TestAppServerUnboundedRefused verifies the orphan-protection gate: an
// APP_SERVER spec without a positive duration is refused outright (never
// clamped), so a far-end server cannot outlive a crashed controller.
func TestAppServerUnboundedRefused(t *testing.T) {
	a := startAgent(t, "a")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for name, dur := range map[string]*durationpb.Duration{
		"no duration":   nil,
		"zero duration": durationpb.New(0),
	} {
		_, err := a.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
			Role: loomv1.FlowRole_FLOW_ROLE_APP_SERVER, App: "voip", Duration: dur,
		}})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
}

// TestAppServerDurationBounds verifies the enforced bound actually ends the
// flow: a short-duration APP_SERVER finishes on its own (telemetry stream
// drains to completion) with no Stop from the controller.
func TestAppServerDurationBounds(t *testing.T) {
	a := startAgent(t, "a")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := a.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_APP_SERVER, App: "voip",
		Duration: durationpb.New(300 * time.Millisecond),
	}})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if _, err := a.client.Start(ctx, &loomv1.StartRequest{FlowId: cfg.GetFlowId()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stream, err := a.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: cfg.GetFlowId()})
	if err != nil {
		t.Fatalf("StreamTelemetry: %v", err)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			return // stream ended: the duration bound completed the flow
		}
	}
}

// TestAppUnknownRefused verifies a spec naming an unknown app or network fails
// cleanly with InvalidArgument (no panic, no leaked resources).
func TestAppUnknownRefused(t *testing.T) {
	a := startAgent(t, "a")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name string
		flow *loomv1.FlowSpec
	}{
		{"unknown app server", &loomv1.FlowSpec{
			Role: loomv1.FlowRole_FLOW_ROLE_APP_SERVER, App: "nosuch",
			Duration: durationpb.New(time.Second),
		}},
		{"unknown app client", &loomv1.FlowSpec{
			Role: loomv1.FlowRole_FLOW_ROLE_APP_CLIENT, App: "nosuch", Target: "127.0.0.1:9",
		}},
		{"missing app name", &loomv1.FlowSpec{
			Role: loomv1.FlowRole_FLOW_ROLE_APP_SERVER, Duration: durationpb.New(time.Second),
		}},
		{"unknown network", &loomv1.FlowSpec{
			Role: loomv1.FlowRole_FLOW_ROLE_APP_SERVER, App: "voip", Network: "nosuch",
			Duration: durationpb.New(time.Second),
		}},
		{"missing client target", &loomv1.FlowSpec{
			Role: loomv1.FlowRole_FLOW_ROLE_APP_CLIENT, App: "voip",
		}},
		{"bad local address", &loomv1.FlowSpec{
			Role: loomv1.FlowRole_FLOW_ROLE_APP_SERVER, App: "voip", Local: "not-an-ip",
			Duration: durationpb.New(time.Second),
		}},
	}
	for _, tc := range cases {
		_, err := a.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: tc.flow})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: err = %v, want InvalidArgument", tc.name, err)
		}
	}
}

// TestCapabilitiesAppsNetworks verifies the version-skew gate's raw material:
// Capabilities advertises the registered netpath networks and app engines.
func TestCapabilitiesAppsNetworks(t *testing.T) {
	a := startAgent(t, "a")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	caps, err := a.client.Capabilities(ctx, &loomv1.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !contains(caps.GetNetworks(), "host") {
		t.Errorf("networks %v missing %q", caps.GetNetworks(), "host")
	}
	if !contains(caps.GetApps(), "voip") {
		t.Errorf("apps %v missing %q", caps.GetApps(), "voip")
	}
	// The per-side lists are the skew gate's side-aware raw material: a stock
	// agent registers the voip pair, so both sides advertise it (a slimmed
	// one-side build would advertise only its side).
	if !contains(caps.GetAppsClient(), "voip") {
		t.Errorf("apps_client %v missing %q", caps.GetAppsClient(), "voip")
	}
	if !contains(caps.GetAppsServer(), "voip") {
		t.Errorf("apps_server %v missing %q", caps.GetAppsServer(), "voip")
	}
}
