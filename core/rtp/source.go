// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import (
	"math"
	"strings"
)

// PayloadSource generates the payload for packet pktIndex of a synthetic
// media stream. Fill writes into buf and returns the number of bytes
// written. Sources are deterministic: the same (buf length, pktIndex) always
// yields the same bytes, so streams are reproducible and golden captures are
// stable. Implementations in this package follow the "wire-format-true,
// content-synthetic" rule described in the package comment.
type PayloadSource interface {
	Fill(buf []byte, pktIndex uint64) int
}

// g711Source synthesizes band-limited speech-like audio at 8 kHz and
// compands it through the G.711 encoder, one byte per sample. The signal is
// a low sum of sine partials (all well inside the 300–3400 Hz telephony
// band's spectral neighborhood) under a slow syllabic amplitude envelope, so
// Wireshark's RTP player renders an obviously alive, non-constant tone
// rather than silence or noise.
type g711Source struct {
	alaw bool
}

// NewG711Source returns a PayloadSource producing G.711 audio under the
// given companding law: "mulaw" (aliases "ulaw", "pcmu", "u") or "alaw"
// (aliases "pcma", "a"), case-insensitive. The phase position of packet
// pktIndex is pktIndex·len(buf) samples, so a stream filled with a constant
// payload size is phase-continuous. NewG711Source panics on an unknown law —
// a programming error, matching codec.Register's contract.
func NewG711Source(law string) PayloadSource {
	switch strings.ToLower(law) {
	case "mulaw", "ulaw", "pcmu", "u":
		return &g711Source{alaw: false}
	case "alaw", "pcma", "a":
		return &g711Source{alaw: true}
	default:
		panic("rtp: NewG711Source: unknown law " + law + " (want mulaw or alaw)")
	}
}

// Fill implements PayloadSource: one companded byte per 8 kHz sample.
func (g *g711Source) Fill(buf []byte, pktIndex uint64) int {
	base := pktIndex * uint64(len(buf))
	for i := range buf {
		t := float64(base+uint64(i)) / 8000
		// Three partials of a low "voice" fundamental plus a 3 Hz syllabic
		// envelope; peak magnitude ≤ 12000 so companding never clips.
		s := 0.5*math.Sin(2*math.Pi*210*t) +
			0.3*math.Sin(2*math.Pi*420*t+1.0) +
			0.2*math.Sin(2*math.Pi*840*t+2.0)
		env := 0.55 + 0.45*math.Sin(2*math.Pi*3*t)
		pcm := int16(s * env * 12000)
		if g.alaw {
			buf[i] = EncodeALaw(pcm)
		} else {
			buf[i] = EncodeMuLaw(pcm)
		}
	}
	return len(buf)
}

// opusTOCSilkWB20 is the RFC 6716 §3.1 TOC byte for configuration 9
// (SILK-only, wideband, 20 ms), mono (s=0), code 0 (one frame per packet):
// 9<<3 = 0x48. Dissectors classify the packet as valid Opus; the body is
// synthetic (see NewOpusSource).
const opusTOCSilkWB20 = 0x48

// opusSource emits fixed-size packets at a CBR target: a valid TOC byte
// followed by a deterministic pseudo-random body.
type opusSource struct {
	packetBytes int
}

// NewOpusSource returns a PayloadSource emitting one 20 ms Opus packet per
// Fill at the given constant bitrate: bitrateBps·0.020/8 bytes per packet
// (minimum 2 — TOC plus one body byte). The TOC byte declares the 20 ms
// SILK-WB configuration; the body is deterministic pseudo-random bytes, NOT
// decodable audio — wire-format-true, content-synthetic. If buf is smaller
// than the CBR target, Fill truncates to len(buf).
func NewOpusSource(bitrateBps int) PayloadSource {
	n := bitrateBps / 400 // bits/s × 0.020 s ÷ 8 bits/byte
	if n < 2 {
		n = 2
	}
	return &opusSource{packetBytes: n}
}

// Fill implements PayloadSource: TOC byte plus xorshift64 body keyed by
// pktIndex.
func (o *opusSource) Fill(buf []byte, pktIndex uint64) int {
	n := o.packetBytes
	if n > len(buf) {
		n = len(buf)
	}
	if n == 0 {
		return 0
	}
	buf[0] = opusTOCSilkWB20
	s := pktIndex*0x9E3779B97F4A7C15 + 1
	if s == 0 {
		s = 1
	}
	for i := 1; i < n; i++ {
		s ^= s << 13
		s ^= s >> 7
		s ^= s << 17
		buf[i] = byte(s)
	}
	return n
}
