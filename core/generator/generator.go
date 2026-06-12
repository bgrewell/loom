// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package generator produces a flow's traffic content: it decides what bytes go
// in each packet, using a payload source. The transport (UDP/TCP/raw) is the
// datapath's concern, not the generator's — so the same generator runs over any
// datapath. See DESIGN.md §5.3 and docs/blueprints/{traffic-engine,payloaders}.md.
package generator

// Generator yields successive packets for a flow.
//
// Next fills buf with the next packet's bytes and returns how many were written
// and whether the generator is finished. For an open-ended source, done is
// always false and the flow's stop condition ends the run. Implementations must
// be allocation-free on this hot path.
type Generator interface {
	// Name returns the generator's registry identifier.
	Name() string
	// Next fills buf with the next packet; returns bytes written and done.
	Next(buf []byte) (n int, done bool)
}
