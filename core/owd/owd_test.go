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
	// Behavioral pin of DefaultWindow: with zero arguments the first window
	// completes only once a feed lands DefaultWindow past the anchor.
	tr := NewTracker(0, 0)
	t0 := time.Unix(1700000000, 0)
	s := timesync.Sample{Offset: time.Millisecond, Delay: 4 * time.Millisecond}
	tr.Feed(s, t0)
	tr.Feed(s, t0.Add(DefaultWindow-time.Second))
	if _, _, ok := tr.Offset(); ok {
		t.Fatal("ok=true before the default window completed")
	}
	tr.Feed(s, t0.Add(DefaultWindow))
	if _, _, ok := tr.Offset(); !ok {
		t.Fatal("ok=false after the default window completed")
	}
}
