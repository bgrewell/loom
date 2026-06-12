// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package generator

import "github.com/bgrewell/loom/core/payload"

// Stream is the basic raw generator: it emits fixed-size packets drawn from a
// payload source. Run it over a UDP datapath for datagram traffic or a TCP
// datapath for a byte stream.
type Stream struct {
	pl   payload.Payloader
	size int
}

// NewStream returns a Stream emitting size-byte packets from pl (size clamped to
// at least 1).
func NewStream(pl payload.Payloader, size int) *Stream {
	if size < 1 {
		size = 1
	}
	return &Stream{pl: pl, size: size}
}

// Name implements Generator.
func (*Stream) Name() string { return "stream" }

// Next fills up to the configured packet size from the payload source.
func (s *Stream) Next(buf []byte) (int, bool) {
	n := s.size
	if n > len(buf) {
		n = len(buf)
	}
	m, _ := s.pl.Read(buf[:n])
	return m, false
}
