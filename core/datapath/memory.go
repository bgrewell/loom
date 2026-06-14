// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

// Memory is an in-process loopback datapath: Send enqueues a packet that a
// subsequent Recv returns. It needs no kernel, NIC, or privileges, which makes
// the full flow path unit-testable and deterministic.
type Memory struct {
	ch chan []byte
}

// NewMemory returns a Memory datapath buffering up to size packets (clamped to
// at least 1).
func NewMemory(size int) *Memory {
	if size < 1 {
		size = 1
	}
	return &Memory{ch: make(chan []byte, size)}
}

// Name implements Datapath.
func (*Memory) Name() string { return "memory" }

// Caps implements Datapath.
func (*Memory) Caps() Capabilities { return Capabilities{} }

// Send copies p into the queue, returning ErrFull if it is full.
func (m *Memory) Send(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	select {
	case m.ch <- b:
		return len(p), nil
	default:
		return 0, ErrFull
	}
}

// Recv returns the next queued packet, or ErrEmpty if none is available.
func (m *Memory) Recv(p []byte) (int, error) {
	select {
	case b := <-m.ch:
		return copy(p, b), nil
	default:
		return 0, ErrEmpty
	}
}

// Close releases the queue. It does not close the channel: a buffered channel is
// reclaimed by the GC once unreferenced, and closing it would panic any producer
// still in Send (close-then-send race). It is therefore safe to call Close
// concurrently with, or before joining, an in-flight Send.
func (m *Memory) Close() error { return nil }
