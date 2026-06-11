// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package accounting

// Ring is a fixed-size ring buffer that overwrites the oldest element when
// full. It is the sliding-window retention primitive for sampled metrics.
type Ring[T any] struct {
	data []T
	head int
	n    int
}

// NewRing returns a Ring holding up to size elements (clamped to at least 1).
func NewRing[T any](size int) *Ring[T] {
	if size < 1 {
		size = 1
	}
	return &Ring[T]{data: make([]T, size)}
}

// Push appends v, overwriting the oldest element if the ring is full.
func (r *Ring[T]) Push(v T) {
	r.data[r.head] = v
	r.head = (r.head + 1) % len(r.data)
	if r.n < len(r.data) {
		r.n++
	}
}

// Len returns the number of elements currently held.
func (r *Ring[T]) Len() int { return r.n }

// Slice returns the held elements in insertion order (oldest first).
func (r *Ring[T]) Slice() []T {
	out := make([]T, r.n)
	start := (r.head - r.n + len(r.data)) % len(r.data)
	for i := 0; i < r.n; i++ {
		out[i] = r.data[(start+i)%len(r.data)]
	}
	return out
}
