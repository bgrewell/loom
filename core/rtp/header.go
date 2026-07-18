// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// HeaderLen is the fixed portion of an RTP header (RFC 3550 §5.1). Each CSRC
// entry adds 4 bytes.
const HeaderLen = 12

// maxCSRC is the CSRC-list capacity of the 4-bit CC field.
const maxCSRC = 15

var (
	// ErrShortBuffer is returned when a buffer is too small to hold or parse
	// a complete RTP header.
	ErrShortBuffer = errors.New("rtp: buffer too short")
	// ErrVersion is returned by ParseHeader when the version field is not 2.
	ErrVersion = errors.New("rtp: version is not 2")
)

// Header is the fixed RTP header plus the CSRC list (RFC 3550 §5.1). The
// version field is implicit: MarshalTo always writes version 2 and
// ParseHeader rejects anything else.
type Header struct {
	// Padding is the P bit: the payload carries trailing padding whose length
	// is given by the payload's last byte. Header codecs pass the flag
	// through; trimming is the caller's job.
	Padding bool
	// Extension is the X bit: one RFC 3550 §5.3.1 extension header follows
	// the CSRC list. MarshalTo writes only the flag — a caller that sets it
	// must append the extension header itself before the payload.
	// ParseHeader skips the extension and points payloadOffset past it (the
	// extension body is not retained).
	Extension bool
	// Marker is the M bit; for audio it marks the first packet of a
	// talkspurt (RFC 3551 §4.1).
	Marker bool
	// PayloadType is the 7-bit payload type (RFC 3551 §6 static assignments;
	// see core/rtp/codec).
	PayloadType uint8
	// SequenceNumber increments by one per packet and wraps at 2^16.
	SequenceNumber uint16
	// Timestamp is the sampling instant of the first octet of the payload,
	// in media-clock units (never wall clock — see the package comment).
	Timestamp uint32
	// SSRC identifies the synchronization source.
	SSRC uint32
	// CSRC lists contributing sources (at most 15, mixers only).
	CSRC []uint32
}

// MarshalTo writes the header into b and returns the number of bytes written:
// HeaderLen + 4·len(CSRC). It fails with ErrShortBuffer if b is too small,
// and with a plain error if the CSRC list exceeds 15 entries or the payload
// type exceeds 7 bits (either would corrupt neighboring fields).
func (h *Header) MarshalTo(b []byte) (int, error) {
	if len(h.CSRC) > maxCSRC {
		return 0, fmt.Errorf("rtp: %d CSRC entries exceed the 4-bit CC field (max %d)", len(h.CSRC), maxCSRC)
	}
	if h.PayloadType > 0x7F {
		return 0, fmt.Errorf("rtp: payload type %d exceeds 7 bits", h.PayloadType)
	}
	n := HeaderLen + 4*len(h.CSRC)
	if len(b) < n {
		return 0, fmt.Errorf("%w: header needs %d bytes, have %d", ErrShortBuffer, n, len(b))
	}
	b[0] = 2 << 6 // version 2
	if h.Padding {
		b[0] |= 1 << 5
	}
	if h.Extension {
		b[0] |= 1 << 4
	}
	b[0] |= uint8(len(h.CSRC))
	b[1] = h.PayloadType
	if h.Marker {
		b[1] |= 1 << 7
	}
	binary.BigEndian.PutUint16(b[2:4], h.SequenceNumber)
	binary.BigEndian.PutUint32(b[4:8], h.Timestamp)
	binary.BigEndian.PutUint32(b[8:12], h.SSRC)
	for i, c := range h.CSRC {
		binary.BigEndian.PutUint32(b[12+4*i:], c)
	}
	return n, nil
}

// ParseHeader decodes the RTP header at the start of b and returns it along
// with the offset of the payload. It fails with ErrVersion when the version
// bits are not 2 and with ErrShortBuffer when b cannot hold the full header
// (fixed part, CSRC list, and — when the X bit is set — the RFC 3550 §5.3.1
// extension header, which is skipped so payloadOffset always points at the
// payload). Padding, if the P bit is set, remains inside the payload region;
// its length is the payload's last byte (RFC 3550 §5.1).
func ParseHeader(b []byte) (h Header, payloadOffset int, err error) {
	if len(b) < HeaderLen {
		return Header{}, 0, fmt.Errorf("%w: %d bytes cannot hold the %d-byte fixed header", ErrShortBuffer, len(b), HeaderLen)
	}
	if v := b[0] >> 6; v != 2 {
		return Header{}, 0, fmt.Errorf("%w (got %d)", ErrVersion, v)
	}
	h.Padding = b[0]&(1<<5) != 0
	h.Extension = b[0]&(1<<4) != 0
	cc := int(b[0] & 0x0F)
	h.Marker = b[1]&(1<<7) != 0
	h.PayloadType = b[1] & 0x7F
	h.SequenceNumber = binary.BigEndian.Uint16(b[2:4])
	h.Timestamp = binary.BigEndian.Uint32(b[4:8])
	h.SSRC = binary.BigEndian.Uint32(b[8:12])
	payloadOffset = HeaderLen + 4*cc
	if len(b) < payloadOffset {
		return Header{}, 0, fmt.Errorf("%w: %d bytes cannot hold %d CSRC entries", ErrShortBuffer, len(b), cc)
	}
	if cc > 0 {
		h.CSRC = make([]uint32, cc)
		for i := range h.CSRC {
			h.CSRC[i] = binary.BigEndian.Uint32(b[HeaderLen+4*i:])
		}
	}
	if h.Extension {
		if len(b) < payloadOffset+4 {
			return Header{}, 0, fmt.Errorf("%w: X bit set but no room for the extension header", ErrShortBuffer)
		}
		extWords := int(binary.BigEndian.Uint16(b[payloadOffset+2 : payloadOffset+4]))
		payloadOffset += 4 + 4*extWords
		if len(b) < payloadOffset {
			return Header{}, 0, fmt.Errorf("%w: extension declares %d words past the end of the packet", ErrShortBuffer, extWords)
		}
	}
	return h, payloadOffset, nil
}
