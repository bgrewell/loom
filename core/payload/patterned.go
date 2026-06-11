// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package payload

import "encoding/binary"

// recordSize is the width of one patterned record: an 8-byte big-endian
// sequence number.
const recordSize = 8

// Patterned emits consecutive big-endian sequence numbers, so a receiver can
// detect loss, reordering, and duplication. See docs/blueprints/payloaders.md.
type Patterned struct {
	seq uint64
	buf [recordSize]byte
	off int
	// primed reports whether buf holds the current record.
	primed bool
}

// NewPatterned returns a Patterned payloader starting at sequence 0.
func NewPatterned() *Patterned { return &Patterned{} }

// Name implements Payloader.
func (*Patterned) Name() string { return "patterned" }

// Read fills p with successive sequence-numbered records.
func (p *Patterned) Read(b []byte) (int, error) {
	for n := 0; n < len(b); {
		if !p.primed || p.off == len(p.buf) {
			binary.BigEndian.PutUint64(p.buf[:], p.seq)
			p.seq++
			p.off = 0
			p.primed = true
		}
		c := copy(b[n:], p.buf[p.off:])
		p.off += c
		n += c
	}
	return len(b), nil
}
