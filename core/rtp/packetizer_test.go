// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import (
	"bytes"
	"testing"

	"github.com/bgrewell/loom/core/rtp/codec"
)

func mustCodec(t *testing.T, name string) codec.Codec {
	t.Helper()
	c, err := codec.ByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestPacketizerAdvance pins the per-packet contract: sequence +1, timestamp
// +SamplesPerPacket on the media clock (160 for pcmu@20ms, 960 for
// opus@20ms), constant SSRC, and payload copied verbatim.
func TestPacketizerAdvance(t *testing.T) {
	tests := []struct {
		name    string
		codec   string
		tsStep  uint32
		payload int
		pt      uint8
	}{
		{"pcmu 20ms", "pcmu", 160, 160, 0},
		{"pcma 20ms", "pcma", 160, 160, 8},
		{"g729 20ms", "g729", 160, 20, 18},
		{"opus 20ms 48k clock", "opus", 960, 80, 111},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPacketizer(mustCodec(t, tt.codec))
			payload := make([]byte, tt.payload)
			for i := range payload {
				payload[i] = byte(i)
			}
			buf := make([]byte, HeaderLen+tt.payload)

			var prev Header
			for i := 0; i < 5; i++ {
				n := p.Next(buf, payload)
				if n != HeaderLen+tt.payload {
					t.Fatalf("packet %d: Next = %d, want %d", i, n, HeaderLen+tt.payload)
				}
				h, off, err := ParseHeader(buf[:n])
				if err != nil {
					t.Fatalf("packet %d: %v", i, err)
				}
				if h.PayloadType != tt.pt {
					t.Errorf("packet %d: PayloadType = %d, want %d", i, h.PayloadType, tt.pt)
				}
				if h.SSRC != p.SSRC() {
					t.Errorf("packet %d: SSRC = %#x, want %#x", i, h.SSRC, p.SSRC())
				}
				if !bytes.Equal(buf[off:n], payload) {
					t.Errorf("packet %d: payload not copied verbatim", i)
				}
				if i > 0 {
					if h.SequenceNumber != prev.SequenceNumber+1 {
						t.Errorf("packet %d: seq = %d, want %d", i, h.SequenceNumber, prev.SequenceNumber+1)
					}
					if h.Timestamp != prev.Timestamp+tt.tsStep {
						t.Errorf("packet %d: ts = %d, want %d (media clock, +%d/packet)",
							i, h.Timestamp, prev.Timestamp+tt.tsStep, tt.tsStep)
					}
				}
				prev = h
			}
		})
	}
}

// TestPacketizerTalkspurt pins marker semantics: set on the stream's first
// packet, cleared after, and re-armed by Talkspurt for exactly one packet.
func TestPacketizerTalkspurt(t *testing.T) {
	p := NewPacketizer(mustCodec(t, "pcmu"))
	buf := make([]byte, HeaderLen)

	wantMarkers := []bool{true, false, false}
	for i, want := range wantMarkers {
		p.Next(buf, nil)
		h, _, err := ParseHeader(buf)
		if err != nil {
			t.Fatal(err)
		}
		if h.Marker != want {
			t.Errorf("packet %d: Marker = %v, want %v", i, h.Marker, want)
		}
	}

	p.Talkspurt()
	for i, want := range []bool{true, false} {
		p.Next(buf, nil)
		h, _, err := ParseHeader(buf)
		if err != nil {
			t.Fatal(err)
		}
		if h.Marker != want {
			t.Errorf("post-Talkspurt packet %d: Marker = %v, want %v", i, h.Marker, want)
		}
	}
}

// TestPacketizerShortBuffer pins that a too-small buffer writes nothing and
// does not advance the stream.
func TestPacketizerShortBuffer(t *testing.T) {
	p := NewPacketizer(mustCodec(t, "pcmu"))
	payload := make([]byte, 160)

	if n := p.Next(make([]byte, HeaderLen+159), payload); n != 0 {
		t.Fatalf("Next with short buffer = %d, want 0", n)
	}
	if n := p.Next(make([]byte, 4), nil); n != 0 {
		t.Fatalf("Next with sub-header buffer = %d, want 0", n)
	}

	// State untouched: the next successful packet still carries the marker
	// (first of the stream) — a failed Next must not consume it.
	buf := make([]byte, HeaderLen+160)
	n := p.Next(buf, payload)
	if n == 0 {
		t.Fatal("Next with adequate buffer failed")
	}
	h, _, err := ParseHeader(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if !h.Marker {
		t.Error("failed Next consumed the talkspurt marker")
	}
}

// TestPacketizerBadPayloadTypePanics pins the constructor guard: a codec row
// whose PayloadType cannot be encoded in the 7-bit header field must be
// rejected at construction — otherwise every Next would return 0 (the
// MarshalTo error is indistinguishable from a short buffer there) and the
// stream would be silently dead.
func TestPacketizerBadPayloadTypePanics(t *testing.T) {
	c := mustCodec(t, "pcmu")
	c.PayloadType = 200
	defer func() {
		if recover() == nil {
			t.Error("NewPacketizer with payload type 200 did not panic")
		}
	}()
	NewPacketizer(c)
}

// TestPacketizerRandomIdentity pins RFC 3550 §5.1/§8: SSRC and initial
// seq/ts come from crypto/rand, so independent packetizers do not share an
// identity. (Eight SSRCs colliding by chance is a ~2^-96 event.)
func TestPacketizerRandomIdentity(t *testing.T) {
	c := mustCodec(t, "pcmu")
	ssrcs := make(map[uint32]bool)
	identical := true
	var first Header
	for i := 0; i < 8; i++ {
		p := NewPacketizer(c)
		ssrcs[p.SSRC()] = true
		buf := make([]byte, HeaderLen)
		p.Next(buf, nil)
		h, _, err := ParseHeader(buf)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = h
		} else if h.SequenceNumber != first.SequenceNumber || h.Timestamp != first.Timestamp {
			identical = false
		}
	}
	if len(ssrcs) < 2 {
		t.Error("eight packetizers share one SSRC; identity is not random")
	}
	if identical {
		t.Error("eight packetizers share initial seq and ts; identity is not random")
	}
}
