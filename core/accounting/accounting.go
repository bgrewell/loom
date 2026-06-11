// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package accounting turns hot-path byte/packet counters into windowed
// throughput rates. The counters are lock-free atomics updated on the data
// plane; a Sampler reads them out-of-band so measurement never stalls sending.
// See docs/blueprints/accounting.md and DESIGN.md §6/§7.
package accounting

import (
	"sync/atomic"
	"time"
)

// Counters are the lock-free byte/packet totals for a flow. Add is called on
// the hot path; the read methods are called by the Sampler.
type Counters struct {
	bytes   atomic.Uint64
	packets atomic.Uint64
}

// Add records one packet of nbytes. Safe for concurrent use; allocation-free.
func (c *Counters) Add(nbytes uint64) {
	c.bytes.Add(nbytes)
	c.packets.Add(1)
}

// Bytes returns the total bytes counted.
func (c *Counters) Bytes() uint64 { return c.bytes.Load() }

// Packets returns the total packets counted.
func (c *Counters) Packets() uint64 { return c.packets.Load() }

// Sampler computes a windowed throughput from a Counters. It is driven
// out-of-band (a ticker, or explicit Sample calls in tests).
type Sampler struct {
	c     *Counters
	last  uint64
	rates *Ring[float64]
}

// NewSampler returns a Sampler over c retaining the last window samples.
func NewSampler(c *Counters, window int) *Sampler {
	return &Sampler{c: c, rates: NewRing[float64](window)}
}

// Sample records the throughput in bits/sec observed since the previous call,
// given the elapsed time, pushes it into the window, and returns it.
func (s *Sampler) Sample(elapsed time.Duration) float64 {
	total := s.c.Bytes()
	delta := total - s.last
	s.last = total

	var bps float64
	if elapsed > 0 {
		bps = float64(delta) * 8 / elapsed.Seconds()
	}
	s.rates.Push(bps)
	return bps
}

// Average returns the mean throughput (bits/sec) over the retained window.
func (s *Sampler) Average() float64 {
	r := s.rates.Slice()
	if len(r) == 0 {
		return 0
	}
	var sum float64
	for _, v := range r {
		sum += v
	}
	return sum / float64(len(r))
}
