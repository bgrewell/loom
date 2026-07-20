// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import (
	"bytes"
	"testing"
)

// TestEncodeMuLaw pins the G.711 μ-law encoder against reference byte
// values: the published extremes of the CCITT/Sun reference implementation
// plus mid-range samples hand-traced through the G.711 algorithm (bias 132,
// segment = bit length − 8, 4-bit mantissa, ones-complement output).
func TestEncodeMuLaw(t *testing.T) {
	tests := []struct {
		pcm  int16
		want byte
	}{
		{0, 0xFF},      // published: zero encodes to 0xFF
		{1, 0xFF},      // quantizes with 0
		{-1, 0x7F},     // published: −1 encodes to 0x7F
		{32767, 0x80},  // published: positive max
		{-32768, 0x00}, // published: negative max (clips at 32635)
		{32635, 0x80},  // clip boundary itself
		{8, 0xFE},      // 8+132=140: seg 0, mantissa 1
		{100, 0xF2},    // 232: seg 0, mantissa 13
		{-100, 0x72},   // sign bit onto the same magnitude
		{1000, 0xCE},   // 1132: seg 3, mantissa 1
		{-1000, 0x4E},
	}
	for _, tt := range tests {
		if got := EncodeMuLaw(tt.pcm); got != tt.want {
			t.Errorf("EncodeMuLaw(%d) = %#02x, want %#02x", tt.pcm, got, tt.want)
		}
	}
}

// TestEncodeALaw pins the G.711 A-law encoder against reference byte values:
// published extremes (even-bit inversion with 0x55) plus hand-traced
// mid-range samples (13-bit magnitude, segment lookup, 4-bit mantissa).
func TestEncodeALaw(t *testing.T) {
	tests := []struct {
		pcm  int16
		want byte
	}{
		{0, 0xD5},      // published: zero encodes to 0xD5
		{-1, 0x55},     // published: −1 encodes to 0x55
		{32767, 0xAA},  // published: positive max
		{-32768, 0x2A}, // published: negative max
		{100, 0xD3},    // 13-bit 12: seg 0, mantissa 6
		{-100, 0x53},
		{1000, 0xFA}, // 13-bit 125: seg 2, mantissa 15
		{-1000, 0x7A},
	}
	for _, tt := range tests {
		if got := EncodeALaw(tt.pcm); got != tt.want {
			t.Errorf("EncodeALaw(%d) = %#02x, want %#02x", tt.pcm, got, tt.want)
		}
	}
}

// TestG711SourceDeterministic pins the PayloadSource contract: same
// (pktIndex, length) always produces identical bytes; consecutive packets
// differ (the signal is alive, not a constant).
func TestG711SourceDeterministic(t *testing.T) {
	for _, law := range []string{"mulaw", "alaw"} {
		t.Run(law, func(t *testing.T) {
			src := NewG711Source(law)
			a := make([]byte, 160)
			b := make([]byte, 160)
			if n := src.Fill(a, 7); n != 160 {
				t.Fatalf("Fill = %d, want 160", n)
			}
			if n := NewG711Source(law).Fill(b, 7); n != 160 {
				t.Fatalf("Fill = %d, want 160", n)
			}
			if !bytes.Equal(a, b) {
				t.Error("two sources disagree for the same pktIndex")
			}
			src.Fill(b, 8)
			if bytes.Equal(a, b) {
				t.Error("packets 7 and 8 are identical; source is not generating a signal")
			}
		})
	}
}

// TestG711SourceLaws pins that the two laws encode the same underlying
// signal differently and that law aliases resolve.
func TestG711SourceLaws(t *testing.T) {
	mu := make([]byte, 160)
	al := make([]byte, 160)
	NewG711Source("PCMU").Fill(mu, 0)
	NewG711Source("pcma").Fill(al, 0)
	if bytes.Equal(mu, al) {
		t.Error("μ-law and A-law encodings are identical")
	}
	defer func() {
		if recover() == nil {
			t.Error("NewG711Source(\"g729\") did not panic")
		}
	}()
	NewG711Source("g729")
}

// TestOpusSource pins the Opus synthetic frames: valid 20 ms SILK-WB TOC
// byte (RFC 6716 §3.1 config 9 → 0x48), CBR sizing at bitrate/400 bytes,
// the 2-byte floor, deterministic bodies, and truncation to the buffer.
func TestOpusSource(t *testing.T) {
	tests := []struct {
		name      string
		bitrate   int
		wantBytes int
	}{
		{"16 kbit/s", 16000, 40},
		{"24 kbit/s", 24000, 60},
		{"32 kbit/s", 32000, 80},
		{"absurdly low floors at 2", 100, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := NewOpusSource(tt.bitrate)
			buf := make([]byte, 256)
			n := src.Fill(buf, 0)
			if n != tt.wantBytes {
				t.Fatalf("Fill = %d bytes, want %d", n, tt.wantBytes)
			}
			if buf[0] != 0x48 {
				t.Errorf("TOC = %#02x, want 0x48 (config 9 SILK-WB 20ms, mono, code 0)", buf[0])
			}
		})
	}

	t.Run("deterministic and varying", func(t *testing.T) {
		src := NewOpusSource(24000)
		a := make([]byte, 60)
		b := make([]byte, 60)
		src.Fill(a, 42)
		NewOpusSource(24000).Fill(b, 42)
		if !bytes.Equal(a, b) {
			t.Error("same pktIndex produced different bodies")
		}
		src.Fill(b, 43)
		if bytes.Equal(a, b) {
			t.Error("consecutive packets share a body")
		}
	})

	t.Run("truncates to buffer", func(t *testing.T) {
		src := NewOpusSource(24000)
		buf := make([]byte, 10)
		if n := src.Fill(buf, 0); n != 10 {
			t.Fatalf("Fill into short buffer = %d, want 10", n)
		}
		if buf[0] != 0x48 {
			t.Errorf("TOC = %#02x, want 0x48", buf[0])
		}
	})
}
