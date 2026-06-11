// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package payload

import "math/rand"

// Random emits bytes from a pre-filled random buffer, read as a ring so no
// randomness is generated on the hot path. A fixed seed makes it reproducible.
type Random struct {
	buf []byte
	off int
}

// NewRandom returns a Random payloader backed by a size-byte buffer seeded by
// seed. size is clamped to at least 1.
func NewRandom(size int, seed int64) *Random {
	if size < 1 {
		size = 1
	}
	b := make([]byte, size)
	// nolint:gosec // not cryptographic; reproducibility is the point.
	r := rand.New(rand.NewSource(seed))
	_, _ = r.Read(b)
	return &Random{buf: b}
}

// Name implements Payloader.
func (*Random) Name() string { return "random" }

// Read fills p by cycling through the random buffer.
func (r *Random) Read(p []byte) (int, error) {
	for n := 0; n < len(p); {
		c := copy(p[n:], r.buf[r.off:])
		r.off += c
		if r.off >= len(r.buf) {
			r.off = 0
		}
		n += c
	}
	return len(p), nil
}
