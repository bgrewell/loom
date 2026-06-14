// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// TestAgentFlowConcurrentLifecycle hammers a single flow's lifecycle RPCs from
// many goroutines at once. gRPC dispatches each RPC on its own goroutine, so
// this is the realistic concurrency the agent faces. Run with -race: it catches
// unsynchronized access to a flow's done/cancel/err fields and double-start
// (close of a closed channel). It must pass cleanly under the race detector and
// never panic.
func TestAgentFlowConcurrentLifecycle(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer("test-1.0")
	srv.telemetry = time.Millisecond
	gs := NewGRPCServer(srv)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	client, conn, err := Dial(lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// A rate-limited flow so it stays alive while we race its lifecycle.
	cfg, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Generator: "stream", Payload: "random", Datapath: "discard",
		PacketSize: 1200, Rate: "10Mbps", Count: 1_000_000,
	}})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	id := cfg.GetFlowId()

	var wg sync.WaitGroup
	hammer := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				fn()
			}
		}()
	}

	// Concurrent Starts (write done/cancel; a double-start must not panic).
	for g := 0; g < 4; g++ {
		hammer(func() { _, _ = client.Start(ctx, &loomv1.StartRequest{FlowId: id}) })
	}
	// Concurrent telemetry subscribers (read done via doneCh).
	for g := 0; g < 4; g++ {
		hammer(func() {
			sctx, scancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer scancel()
			st, err := client.StreamTelemetry(sctx, &loomv1.TelemetryRequest{FlowId: id})
			if err != nil {
				return
			}
			for {
				if _, err := st.Recv(); err != nil {
					return
				}
			}
		})
	}
	// Concurrent Stops (read cancel/done).
	for g := 0; g < 2; g++ {
		hammer(func() { _, _ = client.Stop(ctx, &loomv1.StopRequest{FlowId: id}) })
	}

	wg.Wait()

	// The agent must still be responsive after the storm.
	if _, err := client.Destroy(ctx, &loomv1.DestroyRequest{FlowId: id}); err != nil {
		t.Fatalf("Destroy after storm: %v", err)
	}
	if _, err := client.Health(ctx, &loomv1.HealthRequest{}); err != nil {
		t.Fatalf("Health after storm: %v", err)
	}
	_ = io.EOF
}
