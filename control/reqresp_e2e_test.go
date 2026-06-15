// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"fmt"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// TestRequestResponseE2E drives a request/response emulation through the gRPC
// control plane: a responder agent binds an ephemeral port, a requester agent
// runs the https-browse emulation against it, and the download bytes are
// accounted on both sides.
func TestRequestResponseE2E(t *testing.T) {
	srv := startAgent(t, "responder")
	defer srv.stop()
	cli := startAgent(t, "requester")
	defer cli.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Responder binds an ephemeral TCP port.
	respCfg, err := srv.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RESPONDER, Transport: "tcp", PacketSize: 1400,
	}})
	if err != nil || respCfg.GetDataPort() == 0 {
		t.Fatalf("responder Configure = %+v, %v", respCfg, err)
	}
	if _, err := srv.client.Start(ctx, &loomv1.StartRequest{FlowId: respCfg.GetFlowId()}); err != nil {
		t.Fatalf("responder Start: %v", err)
	}

	// Requester runs https-browse against the responder, bounded to 5 fetches of
	// 1000-byte objects (deterministic, fast).
	target := fmt.Sprintf("127.0.0.1:%d", respCfg.GetDataPort())
	reqCfg, err := cli.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_REQUESTER, Transport: "tcp", Target: target,
		Emulation: "https-browse", PacketSize: 1400, Count: 5,
		Params: map[string]string{"object_size": "1000", "objects": "5", "think": "0"},
	}})
	if err != nil {
		t.Fatalf("requester Configure: %v", err)
	}
	if _, err := cli.client.Start(ctx, &loomv1.StartRequest{FlowId: reqCfg.GetFlowId()}); err != nil {
		t.Fatalf("requester Start: %v", err)
	}

	// Drain the requester's telemetry to completion (its count-bound run finishes).
	reqStream, err := cli.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: reqCfg.GetFlowId()})
	if err != nil {
		t.Fatalf("requester StreamTelemetry: %v", err)
	}
	var last *loomv1.TelemetrySample
	for {
		s, err := reqStream.Recv()
		if err != nil {
			break
		}
		last = s
	}
	if last == nil || last.GetBytes() < 5000 {
		t.Fatalf("requester accounted too little: %+v", last)
	}
	t.Logf("requester received %d bytes / %d transactions", last.GetBytes(), last.GetPackets())

	// The responder should have served the same download.
	time.Sleep(100 * time.Millisecond)
	respStream, err := srv.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: respCfg.GetFlowId()})
	if err != nil {
		t.Fatalf("responder StreamTelemetry: %v", err)
	}
	sample, err := respStream.Recv()
	if err != nil {
		t.Fatalf("responder Recv: %v", err)
	}
	if sample.GetBytes() < 5000 {
		t.Fatalf("responder served too little: %+v", sample)
	}
	t.Logf("responder served %d bytes", sample.GetBytes())

	if _, err := srv.client.Stop(ctx, &loomv1.StopRequest{FlowId: respCfg.GetFlowId()}); err != nil {
		t.Fatalf("responder Stop: %v", err)
	}
}

// TestRequesterRejectsMissingEmulation verifies the agent rejects a requester
// without an emulation name.
func TestRequesterRejectsMissingEmulation(t *testing.T) {
	a := startAgent(t, "a")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_REQUESTER, Transport: "tcp", Target: "127.0.0.1:9",
	}})
	if err == nil {
		t.Fatal("requester without emulation should be rejected")
	}
}

// TestResponderRejectsBadTransport verifies the agent validates the transport.
func TestResponderRejectsBadTransport(t *testing.T) {
	a := startAgent(t, "a")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RESPONDER, Transport: "sctp", PacketSize: 1400,
	}})
	if err == nil {
		t.Fatal("responder with bad transport should be rejected")
	}
}
