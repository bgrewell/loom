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

func (c *capture) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.aggs)
}

func sample(idx int64, bytes uint64) *loomv1.TelemetrySample {
	return &loomv1.TelemetrySample{
		IntervalIndex: idx, IntervalBytes: bytes, IntervalPackets: bytes / 1400,
		IntervalNanos: int64(100 * time.Millisecond),
	}
}

func sampleTCP(idx int64, bytes uint64, totalRetrans, cwnd uint32) *loomv1.TelemetrySample {
	s := sample(idx, bytes)
	s.Tcp = &loomv1.TcpInfo{TotalRetrans: totalRetrans, SndCwnd: cwnd, RttUs: 250}
	return s
}

// TestAggregatorLiveTCPDelta: the live aggregate carries the sender's TCP health,
// with retrans as the per-interval delta (not the cumulative total).
func TestAggregatorLiveTCPDelta(t *testing.T) {
	tel, tx, rx, cap := newAgg(t)

	tel.fold(tx, sampleTCP(0, 1_000_000, 5, 40)) // cumulative retrans 5 by end of interval 0
	tel.fold(rx, sample(0, 1_000_000))
	tel.tryEmit(time.Now())

	tel.fold(tx, sampleTCP(1, 1_000_000, 8, 44)) // cumulative 8 → delta 3 this interval
	tel.fold(rx, sample(1, 1_000_000))
	tel.tryEmit(time.Now())

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.aggs) != 2 {
		t.Fatalf("emitted %d lines, want 2", len(cap.aggs))
	}
	if cap.aggs[0].TCP == nil || cap.aggs[0].TCP.Retrans != 5 {
		t.Errorf("interval 0 retrans delta = %+v, want 5", cap.aggs[0].TCP)
	}
	if cap.aggs[1].TCP == nil || cap.aggs[1].TCP.Retrans != 3 || cap.aggs[1].TCP.Cwnd != 44 {
		t.Errorf("interval 1 = %+v, want retrans delta 3, cwnd 44", cap.aggs[1].TCP)
	}
}

func newAgg(t *testing.T) (*Telemetry, Placed, Placed, *capture) {
	t.Helper()
	tx := Placed{AgentAddr: "a", FlowID: "1", Role: Sender, Event: "e", From: "client", To: "server"}
	rx := Placed{AgentAddr: "b", FlowID: "1", Role: Receiver, Event: "e", From: "client", To: "server"}
	tel := NewTelemetry(100 * time.Millisecond)
	tel.src = fakePlaced{flows: []Placed{tx, rx}}
	cap := &capture{}
	tel.AddObserver(cap)
	return tel, tx, rx, cap
}

func (t *Telemetry) fold(p Placed, s *loomv1.TelemetrySample) {
	t.mu.Lock()
	t.foldLocked(p, s)
	t.mu.Unlock()
}

// TestAggregatorWaitsForLiveStraggler is the core anti-"rx 0" guarantee: when one
// flow reports an interval and the other is still alive but slow, the watermark
// waits for it rather than flushing the interval out with the missing side at zero.
// When the straggler finally reports, the interval emits complete with both sides.
func TestAggregatorWaitsForLiveStraggler(t *testing.T) {
	tel, tx, rx, cap := newAgg(t)

	// Interval 0: both report → completes and emits, labeled with event/direction.
	tel.fold(tx, sample(0, 1_000_000))
	tel.fold(rx, sample(0, 990_000))
	tel.tryEmit(time.Now())

	// Interval 1: only the sender reports. The receiver is alive (its stream has not
	// ended) and we are within the backstop → no premature flush.
	tel.fold(tx, sample(1, 1_000_000))
	tel.tryEmit(time.Now().Add(300 * time.Millisecond)) // past the old 1-interval bound
	if n := cap.len(); n != 1 {
		t.Fatalf("a live straggler must not be flushed to rx 0: emitted %d lines, want 1", n)
	}

	// The receiver finally reports interval 1 (late but alive) → completes with rx.
	tel.fold(rx, sample(1, 980_000))
	tel.tryEmit(time.Now().Add(400 * time.Millisecond))

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.aggs) != 2 {
		t.Fatalf("after the straggler reported, emitted %d lines, want 2", len(cap.aggs))
	}
	first, second := cap.aggs[0], cap.aggs[1]
	if first.Event != "e" || first.From != "client" || first.To != "server" {
		t.Errorf("line not labeled with event/direction: %+v", first)
	}
	if !first.Complete || first.Index != 0 || first.TxBytes != 1_000_000 || first.RxBytes != 990_000 {
		t.Errorf("interval 0 = %+v, want complete index0 tx1000000 rx990000", first)
	}
	if !second.Complete || second.Index != 1 || second.RxBytes != 980_000 {
		t.Errorf("interval 1 = %+v, want complete index1 rx980000 (not dropped)", second)
	}
	if tel.LiveIncomplete() {
		t.Error("no interval flushed incomplete; LiveIncomplete should be false")
	}
}

// TestAggregatorSettlesOnEndedFlow: a missing contributor whose telemetry stream
// has ended will never report, so the interval flushes immediately (incomplete)
// without waiting for the backstop.
func TestAggregatorSettlesOnEndedFlow(t *testing.T) {
	tel, tx, rx, cap := newAgg(t)

	tel.fold(tx, sample(0, 1_000_000)) // only the sender reports interval 0
	tel.mu.Lock()
	tel.ended[rx.Key()] = true // the receiver's stream ended without reporting it
	tel.mu.Unlock()
	tel.tryEmit(time.Now()) // settles now: every flow has reported-or-ended

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.aggs) != 1 {
		t.Fatalf("ended straggler should settle immediately: emitted %d, want 1", len(cap.aggs))
	}
	a := cap.aggs[0]
	if a.Complete || a.Index != 0 || a.Sources != 1 || a.Expected != 2 || a.TxBytes != 1_000_000 {
		t.Errorf("interval 0 = %+v, want incomplete index0 1/2 tx1000000", a)
	}
	if !tel.LiveIncomplete() {
		t.Error("LiveIncomplete should be true after an incomplete flush")
	}
}

// TestAggregatorBackstopFlush: a contributor that goes silent without ending (no
// further reports, stream still open) cannot stall the live view forever — the
// interval flushes incomplete once the backstop elapses.
func TestAggregatorBackstopFlush(t *testing.T) {
	tel, tx, _, cap := newAgg(t)

	tel.fold(tx, sample(0, 1_000_000)) // only the sender reports
	tel.tryEmit(time.Now())            // within the backstop → wait
	if n := cap.len(); n != 0 {
		t.Fatalf("within the backstop, nothing should flush yet: emitted %d, want 0", n)
	}

	tel.tryEmit(time.Now().Add(2500 * time.Millisecond)) // past the backstop
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.aggs) != 1 {
		t.Fatalf("after the backstop, emitted %d lines, want 1", len(cap.aggs))
	}
	if a := cap.aggs[0]; a.Complete || a.Sources != 1 {
		t.Errorf("backstop line = %+v, want incomplete 1/2", a)
	}
}
