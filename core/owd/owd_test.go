// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package owd

import (
	"testing"
	"time"

	"github.com/bgrewell/loom/core/timesync"
)

func TestMethodString(t *testing.T) {
	tests := []struct {
		m    Method
		want string
	}{
		{Synced, "timesync"}, // the telemetry label — NOT "synced"
		{RTTHalf, "rtt/2"},
		{AssumeSynced, "assume-synced"},
		{Method(7), "method(7)"}, // invalid values never wear a real label
	}
	for _, tt := range tests {
		if got := tt.m.String(); got != tt.want {
			t.Errorf("Method(%d).String() = %q, want %q", tt.m, got, tt.want)
		}
	}
}

func TestZeroEstimateInvalid(t *testing.T) {
	var e Estimate
	if e.Valid {
		t.Error("zero Estimate must not be Valid")
	}
}

// Tracker satisfies OffsetProvider.
var _ OffsetProvider = (*Tracker)(nil)

func TestTrackerImplementsOffsetProvider(t *testing.T) {
	var p OffsetProvider = NewTracker(time.Second, 4)
	if _, _, ok := p.Offset(); ok {
		t.Error("empty Tracker reported ok=true through OffsetProvider")
	}
}

func TestNewTrackerDefaults(t *testing.T) {
	// Behavioral pin of DefaultWindow: samples join the in-progress window —
	// and can still move the estimate — until a feed lands DefaultWindow past
	// the anchor, which retires that window's minimum into the fit.
	tr := NewTracker(0, 0)
	t0 := time.Unix(1700000000, 0)
	if _, _, ok := tr.Offset(); ok {
		t.Fatal("ok=true on an empty tracker")
	}
	tr.Feed(timesync.Sample{Offset: time.Millisecond, Delay: 4 * time.Millisecond}, t0)

	// Still inside the first window: a lower-delay exchange displaces the
	// estimate, because nothing has been retired into the fit yet.
	inWindow := timesync.Sample{Offset: 3 * time.Millisecond, Delay: 2 * time.Millisecond}
	tr.Feed(inWindow, t0.Add(DefaultWindow-time.Second))
	if off, _, ok := tr.Offset(); !ok || off != inWindow.Offset {
		t.Fatalf("in-progress window: offset = %v (ok=%v), want %v", off, ok, inWindow.Offset)
	}

	// At DefaultWindow that window completes and its minimum is retired into
	// the fit. The sample that completes it belongs to the NEXT window, so it
	// no longer moves the estimate despite carrying the lowest delay yet.
	tr.Feed(timesync.Sample{Offset: 50 * time.Millisecond, Delay: time.Millisecond}, t0.Add(DefaultWindow))
	off, _, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false after the default window completed")
	}
	if off != inWindow.Offset {
		t.Fatalf("after completion: offset = %v, want the retired window minimum %v", off, inWindow.Offset)
	}
}
