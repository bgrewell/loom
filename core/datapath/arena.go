// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "time"

// Arena is an in-process, zero-copy loopback datapath. Frames are slices into a
// single backing slab (modeling an AF_XDP UMEM): TxReserve/RxPoll hand out
// aliases of that slab, never copies, so a transmitted packet is received from
// the exact same memory. It exists to prove the TxDatapath/RxDatapath contract
// supports zero copy (see arena_test.go) and as the template a real AF_XDP/DPDK
// backend follows. It is single-producer/single-consumer, like the hot path.
type Arena struct {
	slab      []byte
	frameSize int

	free     []int   // available frame indices (TX reserve source)
	reserved []int   // indices handed out by the last TxReserve
	ready    []int   // committed frames awaiting RxPoll
	polled   []int   // indices handed out by the last RxPoll
	lens     []int   // per-frame committed length
	outTx    []Frame // reused scratch for TxReserve (no per-call alloc)
	outRx    []Frame // reused scratch for RxPoll
}

// NewArena builds an arena of frames buffers, each frameSize bytes.
func NewArena(frames, frameSize int) *Arena {
	if frames < 1 {
		frames = 1
	}
	if frameSize < 1 {
		frameSize = 1500
	}
	a := &Arena{
		slab:      make([]byte, frames*frameSize),
		frameSize: frameSize,
		free:      make([]int, frames),
		reserved:  make([]int, 0, frames),
		ready:     make([]int, 0, frames),
		polled:    make([]int, 0, frames),
		lens:      make([]int, frames),
		outTx:     make([]Frame, 0, frames),
		outRx:     make([]Frame, 0, frames),
	}
	for i := range a.free {
		a.free[i] = i
	}
	return a
}

func (a *Arena) frameData(i int) []byte { return a.slab[i*a.frameSize : (i+1)*a.frameSize] }

// Name implements the datapath interfaces.
func (a *Arena) Name() string { return "arena" }

// Caps implements the datapath interfaces.
func (a *Arena) Caps() Capabilities { return Capabilities{} }

// Close implements the datapath interfaces.
func (a *Arena) Close() error { return nil }

// TxReserve hands out up to n free frames as aliases of the slab.
func (a *Arena) TxReserve(n int) []Frame {
	if n < 0 {
		n = 0
	}
	k := n
	if k > len(a.free) {
		k = len(a.free)
	}
	a.reserved = a.reserved[:0]
	a.outTx = a.outTx[:0]
	for i := 0; i < k; i++ {
		idx := a.free[len(a.free)-1]
		a.free = a.free[:len(a.free)-1]
		a.reserved = append(a.reserved, idx)
		a.outTx = append(a.outTx, Frame{Data: a.frameData(idx)})
	}
	return a.outTx
}

// TxCommit queues filled frames for RX and releases every reserved frame. No
// packet bytes are copied — the committed data already lives in the slab.
func (a *Arena) TxCommit(frames []Frame) (int, error) {
	sent := 0
	for i := range frames {
		idx := a.reserved[i]
		if frames[i].Len > 0 {
			a.lens[idx] = frames[i].Len
			a.ready = append(a.ready, idx)
			sent++
		} else {
			a.free = append(a.free, idx)
		}
	}
	// Release any reserved-but-not-committed frames.
	for j := len(frames); j < len(a.reserved); j++ {
		a.free = append(a.free, a.reserved[j])
	}
	a.reserved = a.reserved[:0]
	return sent, nil
}

// RxPoll returns up to max ready frames as aliases of the slab. It returns
// (nil, nil) when none are ready.
func (a *Arena) RxPoll(max int) ([]Frame, error) {
	if max < 0 {
		max = 0
	}
	k := max
	if k > len(a.ready) {
		k = len(a.ready)
	}
	a.polled = a.polled[:0]
	a.outRx = a.outRx[:0]
	now := time.Now().UnixNano()
	for i := 0; i < k; i++ {
		idx := a.ready[i]
		a.outRx = append(a.outRx, Frame{Data: a.frameData(idx)[:a.lens[idx]], Len: a.lens[idx], Meta: Meta{Nanos: now}})
		a.polled = append(a.polled, idx)
	}
	copy(a.ready, a.ready[k:]) // keep cap; drop the consumed prefix
	a.ready = a.ready[:len(a.ready)-k]
	return a.outRx, nil
}

// RxRelease returns polled frames to the free list.
func (a *Arena) RxRelease(frames []Frame) {
	for range frames {
		// indices are tracked internally; release them all
	}
	a.free = append(a.free, a.polled...)
	a.polled = a.polled[:0]
}
