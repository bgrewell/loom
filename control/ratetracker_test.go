// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"testing"
	"time"
)

// TestRateTrackerFirstIntervalNotInflated is the regression guard for the
// inflated-first-sample bug: a flow that already sent bytes before the telemetry
// stream opened must not have all of those bytes charged to the first interval.
func TestRateTrackerFirstIntervalNotInflated(t *testing.T) {
	t0 := time.Unix(0, 0)
	// The flow has already sent 1,000,000 bytes by the time we start measuring.
	m := newRateTracker(1_000_000, t0)

	// One interval later it has sent another 125,000 bytes (= 1.0 Mbps).
	got := m.bitsPerSec(1_125_000, t0.Add(time.Second))
	if want := 1_000_000.0; got != want {
		t.Fatalf("first interval rate = %v bps, want %v (only the 125 KB delta, not the 1.125 MB total)", got, want)
	}

	// The next interval keeps measuring the delta, not the cumulative total.
	got = m.bitsPerSec(1_250_000, t0.Add(2*time.Second))
	if want := 1_000_000.0; got != want {
		t.Fatalf("second interval rate = %v bps, want %v", got, want)
	}
}

func TestRateTrackerEdgeCases(t *testing.T) {
	t0 := time.Unix(100, 0)
	m := newRateTracker(500, t0)

	// Zero elapsed → 0, not a divide blow-up.
	if got := m.bitsPerSec(9999, t0); got != 0 {
		t.Errorf("zero interval rate = %v, want 0", got)
	}
	// A counter that went backwards → 0, not a negative rate. (lastBytes is now
	// 9999 from the previous call.)
	if got := m.bitsPerSec(100, t0.Add(time.Second)); got != 0 {
		t.Errorf("backwards counter rate = %v, want 0", got)
	}
}
