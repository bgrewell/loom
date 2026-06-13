// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

func TestAgentFlowLifecycle(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer("test-1.0")
	srv.telemetry = 5 * time.Millisecond // fast samples for the test
	gs := NewGRPCServer(srv)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	client, conn, err := Dial(lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Configure a count-bound discard flow.
	cfg, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Generator:  "stream",
		Payload:    "random",
		Datapath:   "discard",
		PacketSize: 1400,
		Count:      200000,
	}})
	if err != nil || cfg.GetFlowId() == "" {
		t.Fatalf("Configure = %+v, %v", cfg, err)
	}

	if _, err := client.Arm(ctx, &loomv1.ArmRequest{FlowId: cfg.GetFlowId()}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if _, err := client.Start(ctx, &loomv1.StartRequest{FlowId: cfg.GetFlowId()}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Stream telemetry; expect at least one sample with bytes accounted.
	stream, err := client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: cfg.GetFlowId()})
	if err != nil {
		t.Fatalf("StreamTelemetry: %v", err)
	}
	var maxBytes uint64
	for {
		s, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if s.GetBytes() > maxBytes {
			maxBytes = s.GetBytes()
		}
	}
	if maxBytes == 0 {
		t.Fatal("expected telemetry to account some bytes")
	}

	if _, err := client.Destroy(ctx, &loomv1.DestroyRequest{FlowId: cfg.GetFlowId()}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// Lifecycle on an unknown flow errors.
	if _, err := client.Start(ctx, &loomv1.StartRequest{FlowId: "nope"}); err == nil {
		t.Fatal("Start of unknown flow should error")
	}
}
