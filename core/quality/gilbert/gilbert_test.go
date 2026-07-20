// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package gilbert

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

const ptime = 20 * time.Millisecond

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// feed runs a loss/receive vector ('L' = lost/discarded, 'R' = received)
// through e with observations spaced ptime apart.
func feed(e *Estimator, seq string) {
	at := time.Unix(0, 0)
	for _, c := range seq {
		e.Observe(c == 'L', at)
		at = at.Add(ptime)
	}
}

func TestStartup(t *testing.T) {
	m := New(16).Metrics()
	if m != (Metrics{BurstR: 1}) {
		t.Fatalf("startup metrics = %+v, want zero value with BurstR 1", m)
	}
}

func TestNewGminDefault(t *testing.T) {
	for _, gmin := range []int{0, -3} {
		if got := New(gmin).gmin; got != DefaultGmin {
			t.Fatalf("New(%d).gmin = %d, want %d", gmin, got, DefaultGmin)
		}
	}
	if got := New(4).gmin; got != 4 {
		t.Fatalf("New(4).gmin = %d, want 4", got)
	}
}

// TestVectors pins hand-computed RFC 3611 §4.7.2 burst/gap metrics and
// Gilbert transition estimates on short sequences at gmin=4 (compact vectors;
// the semantics do not depend on the threshold's value).
func TestVectors(t *testing.T) {
	cases := []struct {
		name string
		gmin int
		seq  string
		want Metrics
	}{
		{
			// One burst (L R L L: losses separated by < gmin received) and
			// one isolated loss (followed by gmin received). Gap periods
			// hold 2 (t0,t1) and 11 (t6..t16) packet slots → mean 6.5 slots
			// = 130ms. The burst holds 4 slots (t2..t5) = 80ms — the final
			// loss's own slot counts (RFC 3611 §4.7.2 period duration).
			// Transitions: RR=9 RL=3 LR=3 LL=1 → p = 3/12, q = 3/4,
			// p+q = 1 → BurstR = 1.
			name: "burst and isolated loss",
			gmin: 4,
			seq:  "RRLRLLRRRRRLRRRRR",
			want: Metrics{
				P: 0.25, Q: 0.75, BurstR: 1,
				BurstDensity:  3.0 / 4.0,
				GapDensity:    1.0 / 13.0,
				BurstDuration: 80 * time.Millisecond,
				GapDuration:   130 * time.Millisecond,
			},
		},
		{
			// No losses: a single gap spanning the whole session (5 slots =
			// 100ms); the unidentifiable Gilbert fit reports BurstR = 1.
			name: "all received",
			gmin: 4,
			seq:  "RRRRR",
			want: Metrics{BurstR: 1, GapDuration: 100 * time.Millisecond},
		},
		{
			// All lost: one burst spanning the session (5 slots = 100ms),
			// density 1, no gap. p is unidentifiable (never in the received
			// state) → BurstR 1 by the documented degenerate-stream
			// convention.
			name: "all lost",
			gmin: 4,
			seq:  "LLLLL",
			want: Metrics{
				BurstR:        1,
				BurstDensity:  1,
				BurstDuration: 100 * time.Millisecond,
			},
		},
		{
			// A single lost observation resolves as an isolated gap loss
			// (session edges assumed padded with ≥ gmin received packets).
			// GapDuration stays 0: with one observation the packet-slot
			// spacing is unknown.
			name: "single loss",
			gmin: 4,
			seq:  "L",
			want: Metrics{BurstR: 1, GapDensity: 1},
		},
		{
			// Isolated loss mid-stream: counted in the gap, no burst — one
			// gap of 9 slots = 180ms. Transitions: RL=1 RR=6 LR=1 →
			// p = 1/7, q = 1, p+q > 1 → clamp.
			name: "isolated loss in gap",
			gmin: 4,
			seq:  "RRRRLRRRR",
			want: Metrics{
				P: 1.0 / 7.0, Q: 1, BurstR: 1,
				GapDensity:  1.0 / 9.0,
				GapDuration: 180 * time.Millisecond,
			},
		},
		{
			// Losses separated by exactly gmin received stay isolated: both
			// fall in the gap and no burst forms (RFC 3611: a gap loss has
			// at least Gmin received packets on each side); one gap of 6
			// slots = 120ms. Transitions: LR=1 RR=3 RL=1 → p = 1/4, q = 1,
			// p+q > 1 → clamp.
			name: "losses gmin apart stay isolated",
			gmin: 4,
			seq:  "LRRRRL",
			want: Metrics{
				P: 1.0 / 4.0, Q: 1, BurstR: 1,
				GapDensity:  2.0 / 6.0,
				GapDuration: 120 * time.Millisecond,
			},
		},
		{
			// Losses separated by gmin−1 received merge into one burst that
			// includes the received packets between them: 5 slots = 100ms.
			// Transitions: LR=1 RR=2 RL=1 → p = 1/3, q = 1, p+q > 1 → clamp.
			name: "losses gmin-1 apart form burst",
			gmin: 4,
			seq:  "LRRRL",
			want: Metrics{
				P: 1.0 / 3.0, Q: 1, BurstR: 1,
				BurstDensity:  2.0 / 5.0,
				BurstDuration: 100 * time.Millisecond,
			},
		},
		{
			// Alternating loss is anti-bursty: p = q = 1 gives a raw
			// 1/(p+q) = 0.5, clamped to 1. One burst holds every loss
			// (runs of 1 received never reach gmin): 7 slots (t1..t7) =
			// 140ms; the gap holds only t0 = 20ms.
			name: "alternating clamps BurstR",
			gmin: 4,
			seq:  "RLRLRLRL",
			want: Metrics{
				P: 1, Q: 1, BurstR: 1,
				BurstDensity:  4.0 / 7.0,
				BurstDuration: 140 * time.Millisecond,
				GapDuration:   20 * time.Millisecond,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := New(tc.gmin)
			feed(e, tc.seq)
			got := e.Metrics()
			if !approx(got.P, tc.want.P, 1e-12) || !approx(got.Q, tc.want.Q, 1e-12) {
				t.Errorf("p/q = %v/%v, want %v/%v", got.P, got.Q, tc.want.P, tc.want.Q)
			}
			if !approx(got.BurstR, tc.want.BurstR, 1e-12) {
				t.Errorf("BurstR = %v, want %v", got.BurstR, tc.want.BurstR)
			}
			if !approx(got.BurstDensity, tc.want.BurstDensity, 1e-12) {
				t.Errorf("BurstDensity = %v, want %v", got.BurstDensity, tc.want.BurstDensity)
			}
			if !approx(got.GapDensity, tc.want.GapDensity, 1e-12) {
				t.Errorf("GapDensity = %v, want %v", got.GapDensity, tc.want.GapDensity)
			}
			if got.BurstDuration != tc.want.BurstDuration {
				t.Errorf("BurstDuration = %v, want %v", got.BurstDuration, tc.want.BurstDuration)
			}
			if got.GapDuration != tc.want.GapDuration {
				t.Errorf("GapDuration = %v, want %v", got.GapDuration, tc.want.GapDuration)
			}
		})
	}
}

// TestMetricsDoesNotMutate checks that Metrics is a pure snapshot: calling it
// mid-stream (with unresolved burst/gap state) neither changes its own result
// nor perturbs the estimate relative to feeding one estimator continuously.
func TestMetricsDoesNotMutate(t *testing.T) {
	const seq = "RRLRLLRRRRRLRRRRR"
	e := New(4)
	feed(e, seq[:6]) // ends inside the burst
	m1 := e.Metrics()
	m2 := e.Metrics()
	if m1 != m2 {
		t.Fatalf("repeated Metrics differ: %+v vs %+v", m1, m2)
	}
	at := time.Unix(0, 0).Add(6 * ptime)
	for _, c := range seq[6:] {
		e.Observe(c == 'L', at)
		at = at.Add(ptime)
	}
	ref := New(4)
	feed(ref, seq)
	if got, want := e.Metrics(), ref.Metrics(); got != want {
		t.Fatalf("metrics after mid-stream snapshot = %+v, want %+v", got, want)
	}
}

// TestConservation checks the accounting invariant on random vectors: once
// resolved, every observation is in exactly one of burst or gap, and every
// loss is counted exactly once.
func TestConservation(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 50; trial++ {
		gmin := 1 + rng.Intn(20)
		e := New(gmin)
		var n, losses uint64
		at := time.Unix(0, 0)
		for i := 0; i < 500; i++ {
			lost := rng.Float64() < 0.2
			e.Observe(lost, at)
			at = at.Add(ptime)
			n++
			if lost {
				losses++
			}
		}
		// Force full resolution with gmin received packets.
		for i := 0; i < gmin; i++ {
			e.Observe(false, at)
			at = at.Add(ptime)
			n++
		}
		if e.ph != phaseGap {
			t.Fatalf("trial %d (gmin %d): phase = %d after %d received, want gap", trial, gmin, e.ph, gmin)
		}
		if got := e.gapTotal + e.burstTotalTot; got != n {
			t.Fatalf("trial %d (gmin %d): gap+burst packets = %d, want %d", trial, gmin, got, n)
		}
		if got := e.gapLost + e.burstLostTot; got != losses {
			t.Fatalf("trial %d (gmin %d): gap+burst losses = %d, want %d", trial, gmin, got, losses)
		}
	}
}

// TestRandomLoss checks that independent (Bernoulli) loss at rate p recovers
// p ≈ P, q ≈ 1−p, and BurstR ≈ 1 — the model's "random loss" anchor.
func TestRandomLoss(t *testing.T) {
	const lossProb = 0.05
	rng := rand.New(rand.NewSource(1))
	e := New(DefaultGmin)
	at := time.Unix(0, 0)
	for i := 0; i < 100000; i++ {
		e.Observe(rng.Float64() < lossProb, at)
		at = at.Add(ptime)
	}
	m := e.Metrics()
	if !approx(m.P, lossProb, 0.01) {
		t.Errorf("p = %v, want ≈ %v", m.P, lossProb)
	}
	if !approx(m.Q, 1-lossProb, 0.02) {
		t.Errorf("q = %v, want ≈ %v", m.Q, 1-lossProb)
	}
	if m.BurstR < 1 || m.BurstR > 1.05 {
		t.Errorf("BurstR = %v, want ≈ 1 (and never < 1)", m.BurstR)
	}
}

// TestGilbertRecovery generates deterministic two-state Markov loss traces
// and checks the estimator recovers the generating p, q, and BurstR.
func TestGilbertRecovery(t *testing.T) {
	cases := []struct {
		name string
		p, q float64
		seed int64
	}{
		{"mild bursts", 0.02, 0.25, 2},
		{"short bursts", 0.05, 0.50, 3},
		{"heavy bursts", 0.10, 0.30, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(tc.seed))
			e := New(DefaultGmin)
			lost := false
			at := time.Unix(0, 0)
			for i := 0; i < 300000; i++ {
				if lost {
					if rng.Float64() < tc.q {
						lost = false
					}
				} else {
					if rng.Float64() < tc.p {
						lost = true
					}
				}
				e.Observe(lost, at)
				at = at.Add(ptime)
			}
			m := e.Metrics()
			wantBurstR := 1 / (tc.p + tc.q)
			if math.Abs(m.P-tc.p)/tc.p > 0.05 {
				t.Errorf("p = %v, want %v ± 5%%", m.P, tc.p)
			}
			if math.Abs(m.Q-tc.q)/tc.q > 0.05 {
				t.Errorf("q = %v, want %v ± 5%%", m.Q, tc.q)
			}
			if math.Abs(m.BurstR-wantBurstR)/wantBurstR > 0.05 {
				t.Errorf("BurstR = %v, want %v ± 5%%", m.BurstR, wantBurstR)
			}
			if m.BurstDensity <= m.GapDensity {
				t.Errorf("BurstDensity %v ≤ GapDensity %v on a bursty trace", m.BurstDensity, m.GapDensity)
			}
		})
	}
}
