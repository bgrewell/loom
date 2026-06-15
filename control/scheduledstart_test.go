// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// firstSample opens a telemetry stream and returns the first sample's byte count.
func firstSample(t *testing.T, rig *agentRig, ctx context.Context, flowID string) uint64 {
	t.Helper()
	stream, err := rig.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: flowID})
	if err != nil {
		t.Fatalf("StreamTelemetry: %v", err)
	}
	s, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	return s.GetBytes()
}

// TestScheduledStartGate verifies a flow Start with a future start_at holds the
// flow at the gate (no bytes generated) until that time, then runs.
func TestScheduledStartGate(t *testing.T) {
	a := startAgent(t, "gate")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A discard sender generates and accounts bytes with no receiver needed.
	cfg, err := a.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Generator: "stream", Payload: "random", Datapath: "discard",
		PacketSize: 1200, Rate: "50Mbps", Count: 10_000_000,
	}})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	id := cfg.GetFlowId()

	gate := time.Now().Add(500 * time.Millisecond)
	if _, err := a.client.Start(ctx, &loomv1.StartRequest{
		FlowId: id, StartAtUnixNanos: gate.UnixNano(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Before the gate opens, no traffic should have been generated.
	time.Sleep(200 * time.Millisecond)
	if b := firstSample(t, a, ctx, id); b != 0 {
		t.Fatalf("flow generated %d bytes before the gate opened, want 0", b)
	}

	// After the gate, traffic flows.
	time.Sleep(600 * time.Millisecond)
	if b := firstSample(t, a, ctx, id); b == 0 {
		t.Fatal("flow generated no bytes after the gate opened")
	}

	if _, err := a.client.Stop(ctx, &loomv1.StopRequest{FlowId: id}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestScheduledStartStopBeforeGate verifies a flow stopped before its gate opens
// never runs (the wait is interruptible).
func TestScheduledStartStopBeforeGate(t *testing.T) {
	a := startAgent(t, "gate")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := a.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Generator: "stream", Payload: "random", Datapath: "discard",
		PacketSize: 1200, Rate: "50Mbps", Count: 10_000_000,
	}})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	id := cfg.GetFlowId()

	if _, err := a.client.Start(ctx, &loomv1.StartRequest{
		FlowId: id, StartAtUnixNanos: time.Now().Add(2 * time.Second).UnixNano(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Stop well before the gate; the run must unblock and never generate traffic.
	time.Sleep(150 * time.Millisecond)
	if _, err := a.client.Stop(ctx, &loomv1.StopRequest{FlowId: id}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if b := firstSample(t, a, ctx, id); b != 0 {
		t.Fatalf("flow generated %d bytes despite being stopped before the gate", b)
	}
}
