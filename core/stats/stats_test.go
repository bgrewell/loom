// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package stats

import (
	"math"
	"testing"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestStream(t *testing.T) {
	var s Stream
	for _, x := range []float64{1, 2, 3} {
		s.Add(x)
	}
	if s.Count() != 3 {
		t.Fatalf("count = %d", s.Count())
	}
	if !approx(s.Mean(), 2, 1e-9) {
		t.Fatalf("mean = %v, want 2", s.Mean())
	}
	if !approx(s.Variance(), 1, 1e-9) { // sample variance of {1,2,3}
		t.Fatalf("variance = %v, want 1", s.Variance())
	}
	if !approx(s.StdDev(), 1, 1e-9) {
		t.Fatalf("stddev = %v, want 1", s.StdDev())
	}
	if !approx(s.CoV(), 0.5, 1e-9) {
		t.Fatalf("cov = %v, want 0.5", s.CoV())
	}
	if s.Min() != 1 || s.Max() != 3 {
		t.Fatalf("min/max = %v/%v, want 1/3", s.Min(), s.Max())
	}
}

func TestStreamEmptyAndSingle(t *testing.T) {
	var s Stream
	if s.Variance() != 0 || s.StdDev() != 0 || s.CoV() != 0 {
		t.Fatal("empty stream should be all zero")
	}
	s.Add(5)
	if s.Mean() != 5 || s.Variance() != 0 {
		t.Fatalf("single = mean %v var %v", s.Mean(), s.Variance())
	}
}

func TestJitter(t *testing.T) {
	var j Jitter
	for _, x := range []float64{10, 12, 11} {
		j.Add(x)
	}
	// |12-10|=2, |11-12|=1 → mean 1.5
	if !approx(j.Mean(), 1.5, 1e-9) {
		t.Fatalf("jitter = %v, want 1.5", j.Mean())
	}
}

func TestSeqTracker(t *testing.T) {
	tr := NewSeqTracker()
	for _, s := range []uint64{0, 1, 2, 4} {
		tr.Observe(s)
	}
	if tr.Expected() != 5 || tr.Received() != 4 || tr.Lost() != 1 {
		t.Fatalf("expected=%d received=%d lost=%d, want 5/4/1", tr.Expected(), tr.Received(), tr.Lost())
	}
	tr.Observe(2) // duplicate
	if tr.Duplicates() != 1 {
		t.Fatalf("duplicates = %d, want 1", tr.Duplicates())
	}
	tr.Observe(3) // below max(4) → reorder, fills the gap
	if tr.Reordered() != 1 {
		t.Fatalf("reordered = %d, want 1", tr.Reordered())
	}
	if tr.Lost() != 0 {
		t.Fatalf("lost = %d after gap filled, want 0", tr.Lost())
	}
	if !approx(tr.LossPercent(), 0, 1e-9) {
		t.Fatalf("loss%% = %v, want 0", tr.LossPercent())
	}
}
