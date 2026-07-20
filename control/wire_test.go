// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestHealthReportsAPIVersion(t *testing.T) {
	client, _, stop := dialAgent(t, func(*Server) {})
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, err := client.Health(ctx, &loomv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.GetApiVersion() != APIVersion {
		t.Fatalf("api_version = %d, want %d", h.GetApiVersion(), APIVersion)
	}
}

func TestConfigureRoleSemantics(t *testing.T) {
	client, _, stop := dialAgent(t, func(*Server) {})
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Unspecified role defaults to a sender (no data port bound).
	sender := &loomv1.FlowSpec{
		Generator: "stream", Payload: "random", Datapath: "discard",
		PacketSize: 1000, Duration: durationpb.New(50 * time.Millisecond),
	}
	cfg, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: sender})
	if err != nil {
		t.Fatalf("Configure sender (unspecified role): %v", err)
	}
	if cfg.GetDataPort() != 0 {
		t.Fatalf("sender data_port = %d, want 0", cfg.GetDataPort())
	}

	// Receiver role binds an ephemeral port.
	rxCfg, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RECEIVER, Datapath: "udp", PacketSize: 1000,
	}})
	if err != nil || rxCfg.GetDataPort() == 0 {
		t.Fatalf("Configure receiver = %+v, %v", rxCfg, err)
	}

	// Reflector is not implemented yet.
	_, err = client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_REFLECTOR, Datapath: "udp", PacketSize: 1000,
	}})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("reflector Configure = %v, want Unimplemented", err)
	}
}

// TestAppMetricsRoundTrip pins the TelemetrySample.app / AppMetrics oneof
// wire shape: a fully populated VoipMetrics survives marshal/unmarshal, and
// the oneof discriminates voip vs http.
func TestAppMetricsRoundTrip(t *testing.T) {
	in := &loomv1.TelemetrySample{
		FlowId: "f1",
		Final:  true,
		App: &loomv1.AppMetrics{Kind: &loomv1.AppMetrics_Voip{Voip: &loomv1.VoipMetrics{
			MosCq: 4.2, RFactor: 87.5, JitterMs: 3.1,
			LossPct: 0.5, DiscardPct: 0.1, BurstR: 1.4,
			RttMs: 42, OwdMs: 21, OwdErrMs: 2.5, OwdMethod: "timesync",
			RxPackets: 5000, Lost: 25,
			Gaps: []*loomv1.MediaGap{
				{StartUnixNanos: 100, EndUnixNanos: 160, PacketsLost: 3},
			},
			RemoteMosCq: 4.1,
			Emodel: &loomv1.EModelBreakdown{
				Ro: 93.2, Is: 1.4, Idte: 0.1, Idle: 0.2, Idd: 2.0, IeEff: 2.0,
			},
		}}},
	}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := &loomv1.TelemetrySample{}
	if err := proto.Unmarshal(raw, out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
	v := out.GetApp().GetVoip()
	if v == nil {
		t.Fatal("oneof kind = nil, want voip")
	}
	if out.GetApp().GetHttp() != nil || out.GetApp().GetVideo() != nil {
		t.Fatal("oneof carries http/video alongside voip")
	}
	if v.GetMosCq() != 4.2 || v.GetOwdMethod() != "timesync" ||
		len(v.GetGaps()) != 1 || v.GetEmodel().GetRo() != 93.2 {
		t.Fatalf("voip fields lost in round trip: %+v", v)
	}

	// Switching the oneof member replaces it.
	in.App = &loomv1.AppMetrics{Kind: &loomv1.AppMetrics_Http{Http: &loomv1.HttpMetrics{
		Requests: 10, Errors: 1, TtfbMsP95: 12.5,
		GoodputMbps: 95.2, TlsHandshakeMs: 8.1, ConnectMs: 1.9,
	}}}
	raw, err = proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal http: %v", err)
	}
	out = &loomv1.TelemetrySample{}
	if err := proto.Unmarshal(raw, out); err != nil {
		t.Fatalf("Unmarshal http: %v", err)
	}
	if out.GetApp().GetVoip() != nil || out.GetApp().GetHttp().GetRequests() != 10 {
		t.Fatalf("http oneof round trip = %+v", out.GetApp())
	}
}
