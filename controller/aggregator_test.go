// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"sync"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// fakePlaced is a placedSource with a fixed set of flows (for expected counts).
type fakePlaced struct{ flows []Placed }

func (f fakePlaced) Placed() []Placed { return f.flows }

type capture struct {
	mu   sync.Mutex
	aggs []Aggregate
}

func (c *capture) Observe(a Aggregate) {
	c.mu.Lock()
	c.aggs = append(c.aggs, a)
	c.mu.Unlock()
}

func sample(idx int64, bytes uint64) *loomv1.TelemetrySample {
	return &loomv1.TelemetrySample{
		IntervalIndex: idx, IntervalBytes: bytes, IntervalPackets: bytes / 1400,
		IntervalNanos: int64(100 * time.Millisecond),
	}
}

// TestAggregatorCompletenessAndLateness drives the index-keyed watermark directly:
// an interval emits once both flows report it (complete), and a straggler interval
// flushes incomplete only after the lateness bound.
func TestAggregatorCompletenessAndLateness(t *testing.T) {
	tx := Placed{AgentAddr: "a", FlowID: "1", Role: Sender, Event: "e"}
	rx := Placed{AgentAddr: "b", FlowID: "1", Role: Receiver, Event: "e"}

	tel := NewTelemetry(100 * time.Millisecond)
	tel.src = fakePlaced{flows: []Placed{tx, rx}}
	cap := &capture{}
	tel.AddObserver(cap)

	fold := func(p Placed, s *loomv1.TelemetrySample) {
		tel.mu.Lock()
		tel.foldLocked(p, s)
		tel.mu.Unlock()
	}

	// Interval 0: both report → completes and emits on the next tryEmit.
	fold(tx, sample(0, 1_000_000))
	fold(rx, sample(0, 990_000))
	tel.tryEmit(time.Now())

	// Interval 1: only the sender reports → not complete, not yet late → no emit.
	fold(tx, sample(1, 1_000_000))
	tel.tryEmit(time.Now())

	cap.mu.Lock()
	n := len(cap.aggs)
	cap.mu.Unlock()
	if n != 1 {
		t.Fatalf("after one complete + one partial interval, emitted %d lines, want 1", n)
	}

	// Past the lateness bound, the straggler interval flushes incomplete.
	tel.tryEmit(time.Now().Add(200 * time.Millisecond))

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.aggs) != 2 {
		t.Fatalf("after lateness flush, emitted %d lines, want 2", len(cap.aggs))
	}
	first, second := cap.aggs[0], cap.aggs[1]
	if !first.Complete || first.Index != 0 || first.Sources != 2 || first.Expected != 2 {
		t.Errorf("interval 0 = %+v, want complete index0 2/2", first)
	}
	if first.TxBytes != 1_000_000 || first.RxBytes != 990_000 {
		t.Errorf("interval 0 tx/rx = %d/%d, want 1000000/990000", first.TxBytes, first.RxBytes)
	}
	if second.Complete || second.Index != 1 || second.Sources != 1 || second.Expected != 2 {
		t.Errorf("interval 1 = %+v, want incomplete index1 1/2", second)
	}
	if !tel.LiveIncomplete() {
		t.Error("LiveIncomplete should be true after an incomplete flush")
	}
}
