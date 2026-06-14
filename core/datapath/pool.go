// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

// defaultPoolDepth is how many frames a kernel-socket backend keeps in flight.
// The pump currently reserves one frame per iteration; the depth leaves room for
// batched reserve/commit without per-flow memory growing with packet size.
const defaultPoolDepth = 16

// framePool provides reusable frames over a single slab for backends that copy
// through the kernel (sockets) and so are not natively zero-copy, but still
// present the frame interface. It is single-goroutine, matching the hot path: a
// backend reserves frames with take, fills them, and returns them with release.
type framePool struct {
	fsize int
	slab  []byte
	free  []int   // available frame indices
	held  []int   // indices handed out by the last take
	out   []Frame // reused scratch returned by take (no per-call alloc)
}

// newFramePool builds a pool of n frames, each frameSize bytes.
func newFramePool(n, frameSize int) *framePool {
	if n < 1 {
		n = 1
	}
	if frameSize < 1 {
		frameSize = 1500
	}
	p := &framePool{
		fsize: frameSize,
		slab:  make([]byte, n*frameSize),
		free:  make([]int, n),
		held:  make([]int, 0, n),
		out:   make([]Frame, 0, n),
	}
	for i := range p.free {
		p.free[i] = i
	}
	return p
}

func (p *framePool) data(i int) []byte { return p.slab[i*p.fsize : (i+1)*p.fsize] }

// take reserves up to n free frames (Len reset to 0), tracking them so release
// can return them. It returns fewer than n — possibly none — when the pool is
// exhausted. The returned slice is valid only until the next take or release.
func (p *framePool) take(n int) []Frame {
	if n < 0 {
		n = 0
	}
	k := n
	if k > len(p.free) {
		k = len(p.free)
	}
	p.held = p.held[:0]
	p.out = p.out[:0]
	for i := 0; i < k; i++ {
		idx := p.free[len(p.free)-1]
		p.free = p.free[:len(p.free)-1]
		p.held = append(p.held, idx)
		p.out = append(p.out, Frame{Data: p.data(idx)})
	}
	return p.out
}

// release returns every frame from the last take to the free list.
func (p *framePool) release() {
	p.free = append(p.free, p.held...)
	p.held = p.held[:0]
}
