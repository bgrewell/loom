// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func durApprox(a, b time.Duration, eps time.Duration) bool {
	return (a - b).Abs() <= eps
}

// TestIntervalTd pins the deterministic §6.3.1/A.7 interval on
// hand-computed cases: the 5% RTCP share, the 75/25 receiver/sender split
// with its 25%-of-members activation condition, and the Tmin floors.
//
// Base numbers: SessionBW 64000 bit/s → rtcp_bw = 0.05·64000/8 = 400
// octets/s; avg_rtcp_size defaults to 128 octets.
func TestIntervalTd(t *testing.T) {
	tests := []struct {
		name string
		iv   Interval
		want time.Duration
	}{
		{
			// senders 10 ≤ 25 = 0.25·100 → receiver share: n = 90,
			// bw = 300 → Td = 128·90/300 = 38.4 s.
			name: "receiver share",
			iv:   Interval{SessionBW: 64000, Members: 100, Senders: 10},
			want: 38400 * time.Millisecond,
		},
		{
			// Sender share: n = 10, bw = 100 → Td = 128·10/100 = 12.8 s.
			name: "sender share",
			iv:   Interval{SessionBW: 64000, Members: 100, Senders: 10, WeSent: true},
			want: 12800 * time.Millisecond,
		},
		{
			// senders 50 > 25 → no split: n = 100, bw = 400 → 32 s.
			name: "senders above one quarter share nothing special",
			iv:   Interval{SessionBW: 64000, Members: 100, Senders: 50, WeSent: true},
			want: 32 * time.Second,
		},
		{
			// Larger average compound size scales Td linearly: 76.8 s.
			name: "avg size scales",
			iv:   Interval{SessionBW: 64000, Members: 100, Senders: 10, AvgSize: 256},
			want: 76800 * time.Millisecond,
		},
		{
			// n = 1, bw = 300 → 128/300 ≈ 0.43 s, floored at Tmin 5 s.
			name: "Tmin floor",
			iv:   Interval{SessionBW: 64000, Members: 2, Senders: 0},
			want: 5 * time.Second,
		},
		{
			name: "initial halves the floor",
			iv:   Interval{SessionBW: 64000, Members: 2, Senders: 0, Initial: true},
			want: 2500 * time.Millisecond,
		},
		{
			name: "unknown bandwidth pins Tmin",
			iv:   Interval{Members: 100, Senders: 10},
			want: 5 * time.Second,
		},
		{
			name: "explicit Tmin",
			iv:   Interval{Members: 2, Tmin: 10 * time.Second},
			want: 10 * time.Second,
		},
		{
			name: "explicit Tmin, initial",
			iv:   Interval{Members: 2, Tmin: 10 * time.Second, Initial: true},
			want: 5 * time.Second,
		},
		{
			// Degenerate inputs clamp instead of dividing by zero: members
			// 0 → 1, and a we-sent view with zero senders floors at Tmin.
			name: "zero members",
			iv:   Interval{SessionBW: 64000},
			want: 5 * time.Second,
		},
		{
			name: "we sent with zero senders",
			iv:   Interval{SessionBW: 64000, Members: 10, WeSent: true},
			want: 5 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.iv.Td(); !durApprox(got, tt.want, time.Microsecond) {
				t.Fatalf("Td() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIntervalNextStatistical draws 10000 intervals and pins the A.7
// randomization contract: every draw inside [0.5, 1.5)·Td/1.21828, and the
// mean within tolerance of Td/1.21828 (the uniform draw's mean is 1.0·Td;
// the standard error of the mean over 10k draws is ≈0.3% of it, so 2% is a
// comfortable, non-flaky bound with a fixed seed).
func TestIntervalNextStatistical(t *testing.T) {
	iv := Interval{SessionBW: 64000, Members: 100, Senders: 10} // Td = 38.4 s
	td := iv.Td().Seconds()
	lo := 0.5 * td / compensation
	hi := 1.5 * td / compensation
	wantMean := td / compensation

	rng := rand.New(rand.NewSource(1))
	const n = 10000
	var sum float64
	for i := 0; i < n; i++ {
		got := iv.Next(rng).Seconds()
		if got < lo || got >= hi {
			t.Fatalf("draw %d = %.6fs outside [%.6f, %.6f)", i, got, lo, hi)
		}
		sum += got
	}
	mean := sum / n
	if math.Abs(mean-wantMean) > 0.02*wantMean {
		t.Fatalf("mean = %.4fs, want %.4fs ± 2%%", mean, wantMean)
	}
}

// TestIntervalNextInitialBounds pins the randomization bounds on the
// halved initial interval (Td = 2.5 s): the first report may fire as early
// as 0.5·2.5/1.21828 ≈ 1.03 s — below Tmin, by design — and never at or
// past 1.5·2.5/1.21828.
func TestIntervalNextInitialBounds(t *testing.T) {
	iv := Interval{SessionBW: 64000, Members: 2, Initial: true}
	if got, want := iv.Td(), 2500*time.Millisecond; got != want {
		t.Fatalf("Td() = %v, want %v", got, want)
	}
	lo := 0.5 * 2.5 / compensation
	hi := 1.5 * 2.5 / compensation
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		got := iv.Next(rng).Seconds()
		if got < lo || got >= hi {
			t.Fatalf("draw %d = %.6fs outside [%.6f, %.6f)", i, got, lo, hi)
		}
	}
}

// TestIntervalNextIndependentSeeds pins the anti-storm property the design
// leans on for fleet mode: two members with identical session views but
// independently seeded generators draw different schedules.
func TestIntervalNextIndependentSeeds(t *testing.T) {
	iv := Interval{SessionBW: 64000, Members: 100, Senders: 10}
	a := rand.New(rand.NewSource(1))
	b := rand.New(rand.NewSource(2))
	same := 0
	for i := 0; i < 100; i++ {
		if iv.Next(a) == iv.Next(b) {
			same++
		}
	}
	if same > 0 {
		t.Fatalf("%d of 100 draws identical across independent seeds", same)
	}
}
