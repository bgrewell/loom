// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package owd estimates one-way delay with honest error bars. Every estimate
// carries the method that produced it and a half-width error bound, so a
// number derived from real clock synchronization is never conflated with an
// RTT/2 guess (DESIGN.md §10, ADR-0010).
//
// The math seam is core/timesync: repeated four-timestamp exchanges (RFC 5905
// §8 style) yield per-exchange offset and round-trip delay, and [Tracker]
// filters them the way the NTP clock filter does (RFC 5905 §10) — the
// minimum-delay sample per window is the least queuing-polluted observation —
// then fits a linear offset-drift model over the retained window minima. The
// reported bound is the fit's worst residual plus half the minimum observed
// round-trip delay, the irreducible asymmetry uncertainty of a four-timestamp
// exchange (a sample's offset error is at most delay/2, RFC 5905 §8).
package owd

import (
	"fmt"
	"time"
)

// Method identifies how a one-way-delay figure was obtained. Consumers must
// carry it (and the error bound) end to end so an RTT/2 guess is never
// presented as a measured value.
type Method int

const (
	// Synced means the value uses a measured clock offset from time-sync
	// exchanges (a [Tracker] or equivalent).
	Synced Method = iota
	// RTTHalf means the value is RTT/2 with ErrBound = RTT/2 — the delay
	// could be anywhere in the round trip.
	RTTHalf
	// AssumeSynced means the operator asserted external synchronization
	// (NTP/PTP) with a declared maximum error.
	AssumeSynced
)

// String renders the method as the label carried unchanged through telemetry
// (proto, CLI, Prometheus): "timesync", "rtt/2", or "assume-synced". An
// out-of-range Method renders as "method(N)" — never as one of the real
// labels, which would present garbage as a known provenance.
func (m Method) String() string {
	switch m {
	case Synced:
		return "timesync"
	case RTTHalf:
		return "rtt/2"
	case AssumeSynced:
		return "assume-synced"
	default:
		return fmt.Sprintf("method(%d)", int(m))
	}
}

// Estimate is a one-way-delay figure with its provenance: the value, the
// half-width error bound, the method that produced both, and whether the
// estimate is usable at all.
type Estimate struct {
	// Value is the estimated one-way delay.
	Value time.Duration
	// ErrBound is the half-width of the uncertainty interval: the true value
	// is believed to lie within Value ± ErrBound.
	ErrBound time.Duration
	// Method identifies how Value and ErrBound were obtained.
	Method Method
	// Valid reports whether the estimate is usable; a zero Estimate is not.
	Valid bool
}

// OffsetProvider supplies the current remote-minus-local clock offset with an
// error bound. Offset returns ok=false until the provider has enough data;
// a positive offset means the remote clock is ahead of the local clock
// (matching core/timesync). [Tracker] is the standard implementation.
type OffsetProvider interface {
	Offset() (offset, errBound time.Duration, ok bool)
}
