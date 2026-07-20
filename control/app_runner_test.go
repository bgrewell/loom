// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"sync"
	"testing"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/metrics"
)

// countingEngine is a fake app engine whose Metrics() is interval-closing
// (destructive) in spirit: every call increments intervalReads, so a test can
// prove how many observation intervals were closed. CumulativeMetrics is the
// non-destructive whole-call read.
type countingEngine struct {
	mu            sync.Mutex
	intervalReads int
	cumReads      int
	acct          accounting.Counters
}

func (e *countingEngine) Run(ctx context.Context) error  { <-ctx.Done(); return nil }
func (e *countingEngine) Counters() *accounting.Counters { return &e.acct }
func (e *countingEngine) Metrics() metrics.Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.intervalReads++
	return metrics.VoIP{RxPackets: uint64(e.intervalReads)}
}
func (e *countingEngine) CumulativeMetrics() metrics.Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cumReads++
	return metrics.VoIP{RxPackets: 999}
}

func (e *countingEngine) reads() (interval, cum int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.intervalReads, e.cumReads
}

// TestAppRunnerMetricsAtClosesEachBoundaryOnce: the engine's interval-closing
// Metrics() runs exactly once per boundary index no matter how many telemetry
// streams ask, and every stream gets that boundary's snapshot — a second
// StreamTelemetry subscriber must not split the observation intervals (which
// would corrupt LossPct/DiscardPct/MOS for every consumer).
func TestAppRunnerMetricsAtClosesEachBoundaryOnce(t *testing.T) {
	eng := &countingEngine{}
	r := &appRunner{Runner: eng}

	// Stream A closes boundaries 0 and 1.
	a0 := r.metricsAt(0)
	a1 := r.metricsAt(1)
	// Stream B catches up through the same boundaries: cached snapshots, no
	// fresh interval-closing reads.
	b0 := r.metricsAt(0)
	b1 := r.metricsAt(1)

	if iv, _ := eng.reads(); iv != 2 {
		t.Fatalf("engine Metrics() ran %d times for 2 boundaries x 2 streams, want 2", iv)
	}
	rx := func(s metrics.Snapshot) uint64 { return s.(metrics.VoIP).RxPackets }
	if rx(a1) != rx(b1) || rx(b0) != rx(b1) {
		// A catch-up stream gets the latest closed boundary's snapshot (stale
		// but interval-safe), never a fresh destructive read.
		t.Fatalf("catch-up stream got fresh reads: a0=%v a1=%v b0=%v b1=%v", rx(a0), rx(a1), rx(b0), rx(b1))
	}

	// The final sample prefers the whole-call snapshot, again exactly once.
	f1 := r.metricsAt(finalBoundary)
	f2 := r.metricsAt(finalBoundary)
	if iv, cum := eng.reads(); iv != 2 || cum != 1 {
		t.Fatalf("after final: interval reads %d (want 2), cumulative reads %d (want 1)", iv, cum)
	}
	if f1.(metrics.VoIP).RxPackets != 999 || f2.(metrics.VoIP).RxPackets != 999 {
		t.Fatalf("final snapshots = %v / %v, want the whole-call snapshot from CumulativeMetrics", f1, f2)
	}
}

// TestAppRunnerMetricsAtConcurrent hammers metricsAt from concurrent streams
// (the -race regression for the per-flow serialization).
func TestAppRunnerMetricsAtConcurrent(t *testing.T) {
	eng := &countingEngine{}
	r := &appRunner{Runner: eng}
	var wg sync.WaitGroup
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := int64(0); k < 50; k++ {
				if snap := r.metricsAt(k); snap == nil {
					t.Error("metricsAt returned nil for a metrics-capable engine")
					return
				}
			}
			r.metricsAt(finalBoundary)
		}()
	}
	wg.Wait()
	if iv, _ := eng.reads(); iv > 50 {
		t.Fatalf("engine Metrics() ran %d times for 50 boundaries, want <= 50 (one per boundary)", iv)
	}
}
