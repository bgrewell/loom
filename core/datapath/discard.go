// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

// Discard is a sink TxDatapath: it hands out frames to fill and drops them on
// commit. It's the "generate but don't deliver" backend used for rate tests and
// benchmarks where no receiver is needed.
type Discard struct {
	pool *framePool
}

// NewDiscard returns a discard sink whose frames are frameSize bytes.
func NewDiscard(frameSize int) *Discard {
	return &Discard{pool: newFramePool(defaultPoolDepth, frameSize)}
}

// Name implements TxDatapath.
func (*Discard) Name() string { return "discard" }

// Caps implements TxDatapath.
func (*Discard) Caps() Capabilities { return Capabilities{} }

// TxReserve hands out frames to fill.
func (d *Discard) TxReserve(n int) []Frame { return d.pool.take(n) }

// TxCommit drops the filled frames, counting those with data as sent.
func (d *Discard) TxCommit(frames []Frame) (int, error) {
	sent := 0
	for i := range frames {
		if frames[i].Len > 0 {
			sent++
		}
	}
	d.pool.release()
	return sent, nil
}

// Close is a no-op.
func (*Discard) Close() error { return nil }
