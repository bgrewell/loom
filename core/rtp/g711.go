// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import "math/bits"

// ITU-T G.711 companding, in the form of the CCITT/Sun reference
// implementation (the de-facto conformance reference: its outputs match the
// published G.711 tables at the extremes — μ-law 0→0xFF, −1→0x7F,
// +max→0x80, −max→0x00; A-law 0→0xD5, −1→0x55, +max→0xAA, −max→0x2A).

const (
	muLawBias = 0x84  // 132: shifts the segment boundaries per G.711 Table 2a
	muLawClip = 32635 // largest magnitude that survives the bias without overflow
)

// EncodeMuLaw compresses one 16-bit linear PCM sample to G.711 μ-law
// (PCMU, RFC 3551 §4.5.14).
func EncodeMuLaw(pcm int16) byte {
	v := int32(pcm)
	var sign byte
	if v < 0 {
		sign = 0x80
		v = -v
	}
	if v > muLawClip {
		v = muLawClip
	}
	v += muLawBias
	// v is in [132, 32767]: bit length 8..15 maps to exponent 0..7.
	exp := uint(bits.Len32(uint32(v))) - 8
	mant := byte(v>>(exp+3)) & 0x0F
	return ^(sign | byte(exp)<<4 | mant)
}

// EncodeALaw compresses one 16-bit linear PCM sample to G.711 A-law
// (PCMA, RFC 3551 §4.5.14).
func EncodeALaw(pcm int16) byte {
	// The reference algorithm works on the 13-bit magnitude (>>3).
	v := int32(pcm) >> 3
	var mask byte = 0xD5 // positive sign, pre-inverted even bits
	if v < 0 {
		mask = 0x55
		v = -v - 1
	}
	// v is in [0, 4095]. Segment 0 covers 0..31; segments 1..7 double.
	var seg uint
	if v > 0x1F {
		seg = uint(bits.Len32(uint32(v))) - 5
	}
	shift := seg
	if seg < 2 {
		shift = 1
	}
	code := byte(seg)<<4 | byte(v>>shift)&0x0F
	return code ^ mask
}
