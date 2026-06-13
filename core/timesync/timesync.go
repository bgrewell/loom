// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package timesync computes the clock offset and round-trip delay between two
// hosts from the four-timestamp NTP exchange (DESIGN.md §10, ADR-0010). It is
// the math seam for one-way-delay measurement: the controller stamps t1/t4 and
// the agent stamps t2/t3, and these functions turn the four into an offset and
// delay. The package is pure — no I/O, no clock — so it is trivially testable.
package timesync

import "time"

// Offset returns the estimated offset of the remote clock relative to the local
// clock, in nanoseconds, from the four NTP timestamps:
//
//	t1 — local send time
//	t2 — remote receive time
//	t3 — remote send time
//	t4 — local receive time
//
// A positive offset means the remote clock is ahead of the local clock. The
// estimate assumes the network delay is symmetric.
func Offset(t1, t2, t3, t4 int64) int64 {
	return ((t2 - t1) + (t3 - t4)) / 2
}

// Delay returns the round-trip network delay in nanoseconds (the total time on
// the wire, excluding the remote's processing time t3-t2) from the four NTP
// timestamps described in [Offset].
func Delay(t1, t2, t3, t4 int64) int64 {
	return (t4 - t1) - (t3 - t2)
}

// Sample is the result of one time-sync exchange.
type Sample struct {
	// Offset is the remote clock's offset relative to the local clock; positive
	// means the remote is ahead.
	Offset time.Duration
	// Delay is the round-trip network delay.
	Delay time.Duration
}

// NewSample computes a Sample from the four NTP timestamps (see [Offset]).
func NewSample(t1, t2, t3, t4 int64) Sample {
	return Sample{
		Offset: time.Duration(Offset(t1, t2, t3, t4)),
		Delay:  time.Duration(Delay(t1, t2, t3, t4)),
	}
}
