// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// TestBoundarySamplingExactIntervals checks the core of the redesign: an agent
// started with a report interval emits one boundary sample per interval (indices
// 0..N-1) carrying that interval's delta, then a final sample — so a D-long flow
// at interval I yields exactly D/I full intervals.
func TestBoundarySamplingExactIntervals(t *testing.T) {
	a := startAgent(t, "boundary")
	defer a.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const interval = 250 * time.Millisecond
	const dur = 1100 * time.Millisecond // floor(1.1s/0.25s) = 4 full intervals (non-multiple avoids the last-boundary/end race)
	cfg, err := a.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Generator: "stream", Payload: "random", Datapath: "discard",
		PacketSize: 1400, Rate: "80Mbps", Duration: durationpb.New(dur),
	}})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if _, err := a.client.Start(ctx, &loomv1.StartRequest{
		FlowId: cfg.GetFlowId(), ReportIntervalNanos: interval.Nanoseconds(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stream, err := a.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: cfg.GetFlowId()})
	if err != nil {
		t.Fatalf("StreamTelemetry: %v", err)
	}
	var boundaries int
	var sawFinal bool
	wantIdx := int64(0)
	for {
		s, err := stream.Recv()
		if err != nil {
			break
		}
		if s.GetFinal() {
			sawFinal = true
			continue
		}
		if got := s.GetIntervalIndex(); got != wantIdx {
			t.Fatalf("interval index = %d, want %d (must be monotonic from 0)", got, wantIdx)
		}
		wantIdx++
		boundaries++
		if s.GetIntervalBytes() == 0 {
			t.Errorf("interval %d reported 0 bytes for an 80Mbps flow", s.GetIntervalIndex())
		}
		if s.GetIntervalNanos() <= 0 {
			t.Errorf("interval %d reported non-positive elapsed ns", s.GetIntervalIndex())
		}
	}
	if boundaries != int(dur/interval) {
		t.Errorf("got %d boundary samples, want %d (D/I)", boundaries, int(dur/interval))
	}
	if !sawFinal {
		t.Error("never received the final sample")
	}
}
