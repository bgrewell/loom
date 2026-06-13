// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package log provides the two halves of loom's logging model (DESIGN.md §6):
// a lock-free, non-blocking hot-path event Ring drained off the data plane, and
// an async leveled Logger for control/diagnostic output. The data plane must
// never block on logging, so the hot path only ever touches the Ring.
package log

import "sync/atomic"

// Event codes.
const (
	// EventSent records that one packet was sent (Value = bytes).
	EventSent uint16 = iota + 1
)

// Event is a fixed-size, pointer-free hot-path record so pushing it allocates
// nothing and copies cheaply.
type Event struct {
	Code  uint16
	Seq   uint64
	Value uint64
	Nanos int64
}

// Ring is a single-producer/single-consumer lock-free ring buffer. The data
// plane (one worker) Pushes; a drainer Pops. Push never blocks: when the ring is
// full it drops the event and increments a counter. Capacity is rounded up to a
// power of two.
type Ring struct {
	buf     []Event
	mask    uint64
	head    atomic.Uint64 // producer index
	tail    atomic.Uint64 // consumer index
	dropped atomic.Uint64
}

// NewRing returns a Ring with capacity >= size (rounded up to a power of two,
// minimum 2).
func NewRing(size int) *Ring {
	n := 2
	for n < size {
		n <<= 1
	}
	return &Ring{buf: make([]Event, n), mask: uint64(n - 1)}
}

// Push enqueues e (single producer). It returns false and counts a drop if the
// ring is full. Never blocks; never allocates.
func (r *Ring) Push(e Event) bool {
	head := r.head.Load()
	tail := r.tail.Load()
	if head-tail >= uint64(len(r.buf)) {
		r.dropped.Add(1)
		return false
	}
	r.buf[head&r.mask] = e
	r.head.Store(head + 1) // release: publishes the slot write
	return true
}

// Pop dequeues the next event (single consumer); ok is false if empty.
func (r *Ring) Pop() (e Event, ok bool) {
	tail := r.tail.Load()
	head := r.head.Load() // acquire: sees the producer's slot write
	if tail == head {
		return Event{}, false
	}
	e = r.buf[tail&r.mask]
	r.tail.Store(tail + 1)
	return e, true
}

// Dropped returns the number of events dropped because the ring was full.
func (r *Ring) Dropped() uint64 { return r.dropped.Load() }

// Cap returns the ring capacity.
func (r *Ring) Cap() int { return len(r.buf) }
