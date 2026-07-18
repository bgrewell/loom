// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import (
	crand "crypto/rand"
	"encoding/binary"
	"fmt"

	"github.com/bgrewell/loom/core/rtp/codec"
)

// Packetizer emits the RTP packet stream for one codec: it stamps headers
// with a constant SSRC, a sequence number that advances by one per packet,
// and a timestamp that advances by the codec's samples-per-packet on the
// MEDIA clock (see the package comment for why wall-clock stamping is a
// measurement bug). SSRC, initial sequence number, and initial timestamp are
// drawn from crypto/rand per RFC 3550 §5.1/§8. A Packetizer is not safe for
// concurrent use.
type Packetizer struct {
	payloadType uint8
	ssrc        uint32
	seq         uint16
	ts          uint32
	samples     uint32
	marker      bool
}

// NewPacketizer returns a Packetizer for c with random identity. The
// timestamp advance per packet is c.SamplesPerPacket at the codec's ptime
// (codec.DefaultPtime when unset). The first packet carries the marker bit —
// a stream begins with a talkspurt (RFC 3551 §4.1). NewPacketizer panics if
// c has a nil SamplesPerPacket func (an unregistered, hand-built row), a
// PayloadType over 7 bits (unencodable in the RTP header — left unchecked it
// would make every Next return 0, a silently dead stream), or if crypto/rand
// fails; all are environment/programming errors.
func NewPacketizer(c codec.Codec) *Packetizer {
	if c.SamplesPerPacket == nil {
		panic("rtp: NewPacketizer: codec " + c.Name + " has nil SamplesPerPacket")
	}
	if c.PayloadType > 0x7F {
		panic(fmt.Sprintf("rtp: NewPacketizer: codec %s payload type %d exceeds 7 bits", c.Name, c.PayloadType))
	}
	ptime := c.Ptime
	if ptime <= 0 {
		ptime = codec.DefaultPtime
	}
	var r [10]byte
	if _, err := crand.Read(r[:]); err != nil {
		panic("rtp: crypto/rand unavailable: " + err.Error())
	}
	return &Packetizer{
		payloadType: c.PayloadType,
		ssrc:        binary.BigEndian.Uint32(r[0:4]),
		seq:         binary.BigEndian.Uint16(r[4:6]),
		ts:          binary.BigEndian.Uint32(r[6:10]),
		samples:     c.SamplesPerPacket(ptime),
		marker:      true,
	}
}

// SSRC returns the stream's synchronization source identifier.
func (p *Packetizer) SSRC() uint32 { return p.ssrc }

// Talkspurt marks the next packet as the first of a talkspurt: its header
// will carry the marker bit (RFC 3551 §4.1). Callers invoke it after a
// silence/DTX gap.
func (p *Packetizer) Talkspurt() { p.marker = true }

// Next writes one RTP packet — header then payload — into buf and returns
// the packet length, then advances the sequence number by one and the
// timestamp by the codec's samples-per-packet. If buf cannot hold
// HeaderLen+len(payload) bytes, Next writes nothing, leaves the stream state
// unchanged, and returns 0.
func (p *Packetizer) Next(buf, payload []byte) (n int) {
	h := Header{
		Marker:         p.marker,
		PayloadType:    p.payloadType,
		SequenceNumber: p.seq,
		Timestamp:      p.ts,
		SSRC:           p.ssrc,
	}
	hn, err := h.MarshalTo(buf)
	if err != nil || len(buf) < hn+len(payload) {
		return 0
	}
	copy(buf[hn:], payload)
	p.marker = false
	p.seq++
	p.ts += p.samples
	return hn + len(payload)
}
