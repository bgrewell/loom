// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"encoding/binary"
	"fmt"
)

// MarshalCompound concatenates pkts into one compound RTCP packet, enforcing
// the RFC 3550 §6.1 structural rules loom's receivers (and everyone else's)
// depend on: the first packet MUST be an SR or RR (even with nothing to
// report — it enables the header-validity checks of Appendix A.2), and an
// SDES packet containing a CNAME item MUST be present. It returns
// ErrEmptyCompound, ErrFirstPacket, or ErrNoCNAME when the rules are
// violated, and shares AppendTo's panic contract for unencodable values (see
// Packet).
func MarshalCompound(pkts ...Packet) ([]byte, error) {
	if len(pkts) == 0 {
		return nil, ErrEmptyCompound
	}
	switch pkts[0].(type) {
	case *SenderReport, *ReceiverReport:
	default:
		return nil, fmt.Errorf("%w (RFC 3550 §6.1); got %T first", ErrFirstPacket, pkts[0])
	}
	hasCNAME := false
	for _, p := range pkts {
		if s, ok := p.(*SDES); ok {
			if _, ok := s.CNAME(); ok {
				hasCNAME = true
				break
			}
		}
	}
	if !hasCNAME {
		return nil, fmt.Errorf("%w (RFC 3550 §6.1/§6.5)", ErrNoCNAME)
	}
	var b []byte
	for _, p := range pkts {
		b = p.AppendTo(b)
	}
	return b, nil
}

// ParseCompound walks the packets of a compound RTCP datagram by their
// length fields and returns the ones this package understands (SR, RR, SDES,
// BYE, XR); packets with other types are validated for framing but skipped,
// so profile extensions and newer packet types do not break parsing. It does
// NOT enforce the §6.1 compound rules — tolerant reading, strict writing.
// It fails with ErrVersion on a non-2 version and ErrTruncated when a
// declared length overruns the datagram or a known packet's fields overrun
// its declared length.
func ParseCompound(b []byte) ([]Packet, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: empty datagram", ErrTruncated)
	}
	var out []Packet
	for off := 0; off < len(b); {
		if len(b)-off < 4 {
			return nil, fmt.Errorf("%w: %d trailing bytes cannot hold a common header", ErrTruncated, len(b)-off)
		}
		if v := b[off] >> 6; v != 2 {
			return nil, fmt.Errorf("%w (got %d at offset %d)", ErrVersion, v, off)
		}
		count := int(b[off] & 0x1F)
		pt := b[off+1]
		plen := (int(binary.BigEndian.Uint16(b[off+2:off+4])) + 1) * 4
		if off+plen > len(b) {
			return nil, fmt.Errorf("%w: packet at offset %d declares %d bytes, %d remain", ErrTruncated, off, plen, len(b)-off)
		}
		pkt := b[off : off+plen]
		switch pt {
		case TypeSenderReport:
			sr, err := parseSenderReport(count, pkt)
			if err != nil {
				return nil, err
			}
			out = append(out, sr)
		case TypeReceiverReport:
			rr, err := parseReceiverReport(count, pkt)
			if err != nil {
				return nil, err
			}
			out = append(out, rr)
		case TypeSDES:
			s, err := parseSDES(count, pkt)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		case TypeBye:
			y, err := parseBye(count, pkt)
			if err != nil {
				return nil, err
			}
			out = append(out, y)
		case TypeXR:
			x, err := parseXR(pkt)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		default:
			// Unknown packet type: length-walked and skipped.
		}
		off += plen
	}
	return out, nil
}

// IsRTCP classifies a packet received on a port multiplexing RTP and RTCP
// (RFC 5761 §4). The RTCP packet type octet occupies the same position as
// the RTP marker bit + payload type, and all RTCP types sit in 192–223 —
// which is why RFC 5761 forbids RTP payload types 64–95 (64–95 with the
// marker bit set collide exactly with 192–223). Classification is therefore:
// version 2 and second octet in [192, 223] ⇒ RTCP. Anything shorter than the
// 4-byte RTCP common header, or without version 2 (which also excludes STUN
// and DTLS, whose leading bits are not 10), is not RTCP.
func IsRTCP(b []byte) bool {
	if len(b) < 4 || b[0]>>6 != 2 {
		return false
	}
	return b[1] >= 192 && b[1] <= 223
}
