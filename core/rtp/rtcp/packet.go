// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/bgrewell/loom/core/rtp"
)

// RTCP packet types (RFC 3550 §12.1; RFC 3611 §5.1 for XR).
const (
	TypeSenderReport   = 200 // SR, RFC 3550 §6.4.1
	TypeReceiverReport = 201 // RR, RFC 3550 §6.4.2
	TypeSDES           = 202 // source description, RFC 3550 §6.5
	TypeBye            = 203 // goodbye, RFC 3550 §6.6
	TypeApp            = 204 // application-defined, RFC 3550 §6.7
	TypeXR             = 207 // extended reports, RFC 3611
)

// SDESCNAME is the canonical end-point identifier item type (RFC 3550
// §6.5.1), the one SDES item every compound packet must carry.
const SDESCNAME = 1

var (
	// ErrTruncated is returned when a packet, a declared packet length, or a
	// field inside a packet runs past the available bytes.
	ErrTruncated = errors.New("rtcp: packet truncated")
	// ErrVersion is returned by ParseCompound when a packet's version field
	// is not 2.
	ErrVersion = errors.New("rtcp: version is not 2")
	// ErrEmptyCompound is returned by MarshalCompound with no packets.
	ErrEmptyCompound = errors.New("rtcp: empty compound")
	// ErrFirstPacket is returned by MarshalCompound when the first packet is
	// not an SR or RR (RFC 3550 §6.1).
	ErrFirstPacket = errors.New("rtcp: compound must begin with SR or RR")
	// ErrNoCNAME is returned by MarshalCompound when no SDES packet carries
	// a CNAME item (RFC 3550 §6.1/§6.5).
	ErrNoCNAME = errors.New("rtcp: compound has no SDES CNAME item")
)

// Packet is one RTCP packet. AppendTo appends the packet's complete wire
// encoding — common header included, length field filled in — to b and
// returns the extended slice. Because AppendTo has no error return,
// implementations panic on values that CANNOT be encoded (more than 31
// report blocks, chunks, or SSRCs in a 5-bit count field; SDES text or BYE
// reasons over 255 bytes; a packet whose total size overflows the 16-bit
// length field, 65536 words); such values are programming errors, and
// MarshalCompound documents the same contract.
type Packet interface {
	AppendTo(b []byte) []byte
}

// ReportBlock is one reception report block (RFC 3550 §6.4.1), 24 bytes on
// the wire, carried by both SR and RR packets.
type ReportBlock struct {
	// SSRC identifies the source this report describes.
	SSRC uint32
	// FractionLost is the per-interval loss in 8-bit fixed point
	// (rtp.ReportBlockData.FractionLost).
	FractionLost uint8
	// CumulativeLost is the signed cumulative loss. The wire field is
	// 24 bits; AppendTo clamps to [−0x800000, 0x7FFFFF] and parsing
	// sign-extends.
	CumulativeLost int32
	// ExtHighestSeq is the extended highest sequence number received.
	ExtHighestSeq uint32
	// Jitter is the interarrival jitter in RTP timestamp units.
	Jitter uint32
	// LSR is the middle 32 bits (compact form) of the NTP timestamp of the
	// last SR received from SSRC, 0 if none has been received.
	LSR uint32
	// DLSR is the delay since that SR was received, in 1/65536-second
	// units (see DLSRFromDuration).
	DLSR uint32
}

// NewReportBlock combines the receiver-side fields produced by
// rtp.ReceiverStats.Report with the RTCP timing fields into a complete
// report block: ssrc is the reported-on source, lsr the compact NTP
// timestamp of its last SR (0 if none), and dlsr the delay since that SR in
// 1/65536-second units.
func NewReportBlock(ssrc uint32, d rtp.ReportBlockData, lsr, dlsr uint32) ReportBlock {
	return ReportBlock{
		SSRC:           ssrc,
		FractionLost:   d.FractionLost,
		CumulativeLost: d.CumulativeLost,
		ExtHighestSeq:  d.ExtHighestSeq,
		Jitter:         d.Jitter,
		LSR:            lsr,
		DLSR:           dlsr,
	}
}

// appendTo appends the 24-byte wire form, clamping CumulativeLost to the
// signed 24-bit field.
func (rb *ReportBlock) appendTo(b []byte) []byte {
	b = appendU32(b, rb.SSRC)
	lost := rb.CumulativeLost
	switch {
	case lost > 0x7FFFFF:
		lost = 0x7FFFFF
	case lost < -0x800000:
		lost = -0x800000
	}
	b = append(b, rb.FractionLost, byte(uint32(lost)>>16), byte(uint32(lost)>>8), byte(uint32(lost)))
	b = appendU32(b, rb.ExtHighestSeq)
	b = appendU32(b, rb.Jitter)
	b = appendU32(b, rb.LSR)
	return appendU32(b, rb.DLSR)
}

// parseReportBlock decodes 24 bytes, sign-extending the 24-bit loss field.
func parseReportBlock(b []byte) ReportBlock {
	raw := uint32(b[5])<<16 | uint32(b[6])<<8 | uint32(b[7])
	if raw&0x800000 != 0 {
		raw |= 0xFF000000
	}
	return ReportBlock{
		SSRC:           binary.BigEndian.Uint32(b[0:4]),
		FractionLost:   b[4],
		CumulativeLost: int32(raw),
		ExtHighestSeq:  binary.BigEndian.Uint32(b[8:12]),
		Jitter:         binary.BigEndian.Uint32(b[12:16]),
		LSR:            binary.BigEndian.Uint32(b[16:20]),
		DLSR:           binary.BigEndian.Uint32(b[20:24]),
	}
}

// SenderReport is an RTCP SR (RFC 3550 §6.4.1): the sender's NTP/RTP
// timestamp pair plus its send counters, followed by reception report
// blocks. NTPSec/NTPFrac come from NTPNow; RTPTime maps the SAME sampling
// instant on the media clock, so receivers can use SR pairs to relate the
// two clocks.
type SenderReport struct {
	SSRC            uint32
	NTPSec, NTPFrac uint32
	RTPTime         uint32
	PacketCount     uint32
	OctetCount      uint32
	Reports         []ReportBlock
}

// AppendTo appends the SR wire encoding (28 + 24·len(Reports) bytes). It
// panics on more than 31 report blocks (see Packet).
func (sr *SenderReport) AppendTo(b []byte) []byte {
	b, start := appendHeader(b, count5(len(sr.Reports), "report blocks"), TypeSenderReport)
	b = appendU32(b, sr.SSRC)
	b = appendU32(b, sr.NTPSec)
	b = appendU32(b, sr.NTPFrac)
	b = appendU32(b, sr.RTPTime)
	b = appendU32(b, sr.PacketCount)
	b = appendU32(b, sr.OctetCount)
	for i := range sr.Reports {
		b = sr.Reports[i].appendTo(b)
	}
	return finishPacket(b, start)
}

func parseSenderReport(count int, pkt []byte) (*SenderReport, error) {
	if len(pkt) < 28+24*count {
		return nil, fmt.Errorf("%w: SR with %d report blocks needs %d bytes, have %d", ErrTruncated, count, 28+24*count, len(pkt))
	}
	sr := &SenderReport{
		SSRC:        binary.BigEndian.Uint32(pkt[4:8]),
		NTPSec:      binary.BigEndian.Uint32(pkt[8:12]),
		NTPFrac:     binary.BigEndian.Uint32(pkt[12:16]),
		RTPTime:     binary.BigEndian.Uint32(pkt[16:20]),
		PacketCount: binary.BigEndian.Uint32(pkt[20:24]),
		OctetCount:  binary.BigEndian.Uint32(pkt[24:28]),
	}
	for i := 0; i < count; i++ {
		sr.Reports = append(sr.Reports, parseReportBlock(pkt[28+24*i:]))
	}
	return sr, nil
}

// ReceiverReport is an RTCP RR (RFC 3550 §6.4.2): reception report blocks
// from a member that is not (currently) sending.
type ReceiverReport struct {
	SSRC    uint32
	Reports []ReportBlock
}

// AppendTo appends the RR wire encoding (8 + 24·len(Reports) bytes). It
// panics on more than 31 report blocks (see Packet).
func (rr *ReceiverReport) AppendTo(b []byte) []byte {
	b, start := appendHeader(b, count5(len(rr.Reports), "report blocks"), TypeReceiverReport)
	b = appendU32(b, rr.SSRC)
	for i := range rr.Reports {
		b = rr.Reports[i].appendTo(b)
	}
	return finishPacket(b, start)
}

func parseReceiverReport(count int, pkt []byte) (*ReceiverReport, error) {
	if len(pkt) < 8+24*count {
		return nil, fmt.Errorf("%w: RR with %d report blocks needs %d bytes, have %d", ErrTruncated, count, 8+24*count, len(pkt))
	}
	rr := &ReceiverReport{SSRC: binary.BigEndian.Uint32(pkt[4:8])}
	for i := 0; i < count; i++ {
		rr.Reports = append(rr.Reports, parseReportBlock(pkt[8+24*i:]))
	}
	return rr, nil
}

// SDESItem is one source-description item (RFC 3550 §6.5): a type octet and
// up to 255 bytes of UTF-8 text. Type 0 is the chunk terminator and is not
// representable as an item.
type SDESItem struct {
	Type uint8
	Text string
}

// SDESChunk is one SDES chunk: an SSRC and its description items. On the
// wire the item list is terminated by a null octet and padded with nulls to
// the next 32-bit boundary (RFC 3550 §6.5).
type SDESChunk struct {
	SSRC  uint32
	Items []SDESItem
}

// SDES is an RTCP source-description packet (RFC 3550 §6.5). Every compound
// packet must contain an SDES with a CNAME item (§6.1); NewCNAME builds the
// minimal one.
type SDES struct {
	Chunks []SDESChunk
}

// NewCNAME returns an SDES carrying a single chunk with a single CNAME item
// — the packet MarshalCompound requires in every compound.
func NewCNAME(ssrc uint32, cname string) *SDES {
	return &SDES{Chunks: []SDESChunk{{
		SSRC:  ssrc,
		Items: []SDESItem{{Type: SDESCNAME, Text: cname}},
	}}}
}

// CNAME returns the first CNAME item's text, reporting whether one exists.
func (s *SDES) CNAME() (string, bool) {
	for _, ch := range s.Chunks {
		for _, it := range ch.Items {
			if it.Type == SDESCNAME {
				return it.Text, true
			}
		}
	}
	return "", false
}

// AppendTo appends the SDES wire encoding: each chunk's SSRC and items,
// null-terminated and null-padded to a 32-bit boundary (at least one null
// per chunk, four when the items already end on a boundary). It panics on
// more than 31 chunks, an item of type 0, or item text over 255 bytes (see
// Packet).
func (s *SDES) AppendTo(b []byte) []byte {
	b, start := appendHeader(b, count5(len(s.Chunks), "SDES chunks"), TypeSDES)
	for _, ch := range s.Chunks {
		b = appendU32(b, ch.SSRC)
		for _, it := range ch.Items {
			if it.Type == 0 {
				panic("rtcp: SDES item type 0 is the chunk terminator, not an item")
			}
			if len(it.Text) > 255 {
				panic(fmt.Sprintf("rtcp: SDES item text of %d bytes exceeds the 8-bit length field", len(it.Text)))
			}
			b = append(b, it.Type, uint8(len(it.Text)))
			b = append(b, it.Text...)
		}
		// Terminate the item list and pad to the next 32-bit boundary.
		b = append(b, 0)
		for (len(b)-start)%4 != 0 {
			b = append(b, 0)
		}
	}
	return finishPacket(b, start)
}

func parseSDES(count int, pkt []byte) (*SDES, error) {
	s := &SDES{}
	off := 4
	for c := 0; c < count; c++ {
		if len(pkt)-off < 4 {
			return nil, fmt.Errorf("%w: SDES chunk %d has no SSRC", ErrTruncated, c)
		}
		ch := SDESChunk{SSRC: binary.BigEndian.Uint32(pkt[off : off+4])}
		off += 4
		for {
			if off >= len(pkt) {
				return nil, fmt.Errorf("%w: SDES chunk %d is unterminated", ErrTruncated, c)
			}
			typ := pkt[off]
			if typ == 0 {
				// Terminator: skip it and the null padding to the boundary.
				off = (off + 4) &^ 3
				break
			}
			if len(pkt)-off < 2 {
				return nil, fmt.Errorf("%w: SDES item in chunk %d has no length octet", ErrTruncated, c)
			}
			l := int(pkt[off+1])
			if len(pkt)-off < 2+l {
				return nil, fmt.Errorf("%w: SDES item in chunk %d declares %d text bytes, %d remain", ErrTruncated, c, l, len(pkt)-off-2)
			}
			ch.Items = append(ch.Items, SDESItem{Type: typ, Text: string(pkt[off+2 : off+2+l])})
			off += 2 + l
		}
		s.Chunks = append(s.Chunks, ch)
	}
	return s, nil
}

// Bye is an RTCP goodbye packet (RFC 3550 §6.6): the SSRCs leaving the
// session and an optional reason.
type Bye struct {
	SSRCs  []uint32
	Reason string
}

// AppendTo appends the BYE wire encoding; a non-empty Reason is written as a
// length-prefixed string null-padded to a 32-bit boundary. It panics on more
// than 31 SSRCs or a reason over 255 bytes (see Packet).
func (y *Bye) AppendTo(b []byte) []byte {
	b, start := appendHeader(b, count5(len(y.SSRCs), "BYE SSRCs"), TypeBye)
	for _, ssrc := range y.SSRCs {
		b = appendU32(b, ssrc)
	}
	if y.Reason != "" {
		if len(y.Reason) > 255 {
			panic(fmt.Sprintf("rtcp: BYE reason of %d bytes exceeds the 8-bit length field", len(y.Reason)))
		}
		b = append(b, uint8(len(y.Reason)))
		b = append(b, y.Reason...)
		for (len(b)-start)%4 != 0 {
			b = append(b, 0)
		}
	}
	return finishPacket(b, start)
}

func parseBye(count int, pkt []byte) (*Bye, error) {
	if len(pkt) < 4+4*count {
		return nil, fmt.Errorf("%w: BYE with %d SSRCs needs %d bytes, have %d", ErrTruncated, count, 4+4*count, len(pkt))
	}
	y := &Bye{}
	for i := 0; i < count; i++ {
		y.SSRCs = append(y.SSRCs, binary.BigEndian.Uint32(pkt[4+4*i:]))
	}
	if off := 4 + 4*count; off < len(pkt) {
		l := int(pkt[off])
		if len(pkt)-off < 1+l {
			return nil, fmt.Errorf("%w: BYE reason declares %d bytes, %d remain", ErrTruncated, l, len(pkt)-off-1)
		}
		y.Reason = string(pkt[off+1 : off+1+l])
	}
	return y, nil
}

// count5 validates a 5-bit RC/SC count field value; larger sets must be
// split across packets (RFC 3550 §6.4.1), so exceeding it is a programming
// error and panics.
func count5(n int, what string) uint8 {
	if n > 31 {
		panic(fmt.Sprintf("rtcp: %d %s exceed the 5-bit count field (max 31; split across packets)", n, what))
	}
	return uint8(n)
}

// appendHeader appends a common header (version 2, no padding, the given
// 5-bit count and packet type) with a zero length field, returning the
// offset finishPacket patches.
func appendHeader(b []byte, count uint8, pt uint8) ([]byte, int) {
	start := len(b)
	return append(b, 2<<6|count, pt, 0, 0), start
}

// finishPacket back-patches the 16-bit length field: the packet size in
// 32-bit words minus one (RFC 3550 §6.4.1). A packet too large for the field
// panics (see Packet) — truncating the value would silently emit a
// structurally corrupt compound that no parser (including ParseCompound)
// could walk.
func finishPacket(b []byte, start int) []byte {
	n := len(b) - start
	if n%4 != 0 {
		panic("rtcp: internal error: packet not padded to a 32-bit boundary")
	}
	if n/4-1 > 0xFFFF {
		panic(fmt.Sprintf("rtcp: %d-byte packet exceeds the 16-bit RTCP length field (max %d bytes)", n, (0xFFFF+1)*4))
	}
	binary.BigEndian.PutUint16(b[start+2:start+4], uint16(n/4-1))
	return b
}

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendU16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}
