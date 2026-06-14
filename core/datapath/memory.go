// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

// Memory is an in-process loopback datapath for tests and the registry's
// "memory" backend: committed frames are received from the same backing slab, so
// the full flow path is exercised without a kernel, NIC, or privileges. It is the
// zero-copy arena under a stable name; like the hot path it is
// single-producer/single-consumer.
type Memory struct{ ring *Arena }

// NewMemory returns a Memory loopback buffering up to frames packets of
// frameSize bytes each.
func NewMemory(frames, frameSize int) *Memory {
	return &Memory{ring: NewArena(frames, frameSize)}
}

// Name implements the datapath interfaces.
func (*Memory) Name() string { return "memory" }

// Caps implements the datapath interfaces.
func (m *Memory) Caps() Capabilities { return m.ring.Caps() }

// TxReserve implements TxDatapath.
func (m *Memory) TxReserve(n int) []Frame { return m.ring.TxReserve(n) }

// TxCommit implements TxDatapath.
func (m *Memory) TxCommit(frames []Frame) (int, error) { return m.ring.TxCommit(frames) }

// RxPoll implements RxDatapath.
func (m *Memory) RxPoll(max int) ([]Frame, error) { return m.ring.RxPoll(max) }

// RxRelease implements RxDatapath.
func (m *Memory) RxRelease(frames []Frame) { m.ring.RxRelease(frames) }

// Close implements the datapath interfaces.
func (m *Memory) Close() error { return m.ring.Close() }
