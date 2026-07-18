// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"encoding/binary"
	"fmt"
)

// RFC 3611 XR block types loom emits and parses.
const (
	BlockReceiverRefTime = 4 // Receiver Reference Time, RFC 3611 §4.4
	BlockDLRR            = 5 // DLRR, RFC 3611 §4.5
	BlockVoIPMetrics     = 7 // VoIP Metrics, RFC 3611 §4.7
)

// Unavailable is RFC 3611 §4.7's "value unavailable" sentinel (127) for the
// VoIP Metrics signal level, noise level, RERL, R factor, external R factor,
// MOS-LQ, and MOS-CQ fields.
const Unavailable uint8 = 127

// XRBlock is one extended report block carried inside an XR packet.
// AppendBlock appends the block's wire encoding — 4-byte block header
// (block type, type-specific octet, length in 32-bit words minus one)
// followed by the contents — to b.
type XRBlock interface {
	BlockType() uint8
	AppendBlock(b []byte) []byte
}

// XR is an RTCP extended report packet (RFC 3611 §2): the reporter's SSRC
// followed by report blocks. ParseCompound returns unknown block types
// skipped, mirroring its treatment of unknown packet types.
type XR struct {
	// SSRC identifies the member originating this XR packet.
	SSRC   uint32
	Blocks []XRBlock
}

// AppendTo appends the XR wire encoding: common header (the 5-bit count
// field is reserved and written as zero), reporter SSRC, and each block.
func (x *XR) AppendTo(b []byte) []byte {
	b, start := appendHeader(b, 0, TypeXR)
	b = appendU32(b, x.SSRC)
	for _, blk := range x.Blocks {
		b = blk.AppendBlock(b)
	}
	return finishPacket(b, start)
}

// XRReceiverRefTime is the Receiver Reference Time block (RFC 3611 §4.4,
// BT=4): the reporter's full 64-bit NTP timestamp, which lets non-senders
// anchor DLRR-based RTT measurement exactly as SRs anchor LSR/DLSR.
type XRReceiverRefTime struct {
	NTPSec, NTPFrac uint32
}

// BlockType returns BlockReceiverRefTime.
func (*XRReceiverRefTime) BlockType() uint8 { return BlockReceiverRefTime }

// AppendBlock appends the 12-byte wire encoding (block length field 2).
func (r *XRReceiverRefTime) AppendBlock(b []byte) []byte {
	b = append(b, BlockReceiverRefTime, 0)
	b = appendU16(b, 2)
	b = appendU32(b, r.NTPSec)
	return appendU32(b, r.NTPFrac)
}

// DLRRItem is one sub-block of a DLRR report (RFC 3611 §4.5): the SSRC of a
// receiver whose Receiver Reference Time block was seen, the compact NTP
// timestamp of that block (LastRR), and the delay since it was received in
// 1/65536-second units (DLRR).
type DLRRItem struct {
	SSRC   uint32
	LastRR uint32
	DLRR   uint32
}

// XRDLRR is the DLRR block (RFC 3611 §4.5, BT=5): a sender's response to
// Receiver Reference Time blocks, closing the non-sender RTT loop
// (RTT = A − LastRR − DLRR at the reference-time originator, the same
// 16.16 arithmetic as RTTFromReport).
type XRDLRR struct {
	Items []DLRRItem
}

// BlockType returns BlockDLRR.
func (*XRDLRR) BlockType() uint8 { return BlockDLRR }

// AppendBlock appends the 4 + 12·len(Items) byte wire encoding (block
// length field 3·len(Items)). It panics when the item count overflows the
// 16-bit block length field (>21845 items), a programming error. The
// enclosing XR packet's own 16-bit length field bounds the usable count
// slightly lower (21844 items in a minimal XR); XR.AppendTo panics on that
// overflow.
func (d *XRDLRR) AppendBlock(b []byte) []byte {
	words := 3 * len(d.Items)
	if words > 0xFFFF {
		panic(fmt.Sprintf("rtcp: %d DLRR items exceed the 16-bit block length field", len(d.Items)))
	}
	b = append(b, BlockDLRR, 0)
	b = appendU16(b, uint16(words))
	for _, it := range d.Items {
		b = appendU32(b, it.SSRC)
		b = appendU32(b, it.LastRR)
		b = appendU32(b, it.DLRR)
	}
	return b
}

// XRVoIPMetrics is the VoIP Metrics block (RFC 3611 §4.7, BT=7), 36 bytes on
// the wire. Field encodings per the RFC:
//
//   - LossRate/DiscardRate: fraction of ALL packets lost / discarded by the
//     jitter buffer, in 1/256 fixed point (rate·256).
//   - BurstDensity/GapDensity: fraction of packets within bursts / gaps that
//     were lost or discarded, in 1/256 fixed point (core/quality/gilbert
//     computes these with Gmin=16).
//   - BurstDuration/GapDuration: mean burst/gap duration in MILLISECONDS.
//   - RoundTripDelay/EndSystemDelay: most recent RTP-interface round-trip
//     delay and total internal (codec + jitter buffer + playout) delay, in
//     MILLISECONDS.
//   - SignalLevel/NoiseLevel (dBm) and RERL (dB): analog-plane measurements
//     loom does not make; emit Unavailable (127).
//   - Gmin: the gap threshold the densities were computed with (16 per the
//     RFC's recommendation and loom's gilbert default).
//   - RFactor/ExtRFactor: ITU-T G.107 R factors 0–100 (94 nominal narrowband
//     max), 127 = unavailable.
//   - MOSLQ/MOSCQ: MOS ×10 (10–50), 127 = unavailable.
//   - RXConfig: receiver configuration byte (PLC in bits 7–6, jitter-buffer
//     adaptive in 5–4, JB rate in 3–0).
//   - JBNominal/JBMaximum/JBAbsMax: jitter-buffer nominal, current maximum,
//     and absolute maximum delay in MILLISECONDS.
type XRVoIPMetrics struct {
	// SSRC identifies the media source this block describes (not the
	// reporter — that is XR.SSRC).
	SSRC uint32

	LossRate, DiscardRate, BurstDensity, GapDensity uint8

	BurstDuration, GapDuration, RoundTripDelay, EndSystemDelay uint16

	SignalLevel, NoiseLevel, RERL uint8

	Gmin uint8

	RFactor, ExtRFactor, MOSLQ, MOSCQ uint8

	RXConfig uint8

	JBNominal, JBMaximum, JBAbsMax uint16
}

// BlockType returns BlockVoIPMetrics.
func (*XRVoIPMetrics) BlockType() uint8 { return BlockVoIPMetrics }

// AppendBlock appends the 36-byte wire encoding (block length field 8).
func (v *XRVoIPMetrics) AppendBlock(b []byte) []byte {
	b = append(b, BlockVoIPMetrics, 0)
	b = appendU16(b, 8)
	b = appendU32(b, v.SSRC)
	b = append(b, v.LossRate, v.DiscardRate, v.BurstDensity, v.GapDensity)
	b = appendU16(b, v.BurstDuration)
	b = appendU16(b, v.GapDuration)
	b = appendU16(b, v.RoundTripDelay)
	b = appendU16(b, v.EndSystemDelay)
	b = append(b, v.SignalLevel, v.NoiseLevel, v.RERL, v.Gmin)
	b = append(b, v.RFactor, v.ExtRFactor, v.MOSLQ, v.MOSCQ)
	b = append(b, v.RXConfig, 0)
	b = appendU16(b, v.JBNominal)
	b = appendU16(b, v.JBMaximum)
	return appendU16(b, v.JBAbsMax)
}

func parseXR(pkt []byte) (*XR, error) {
	if len(pkt) < 8 {
		return nil, fmt.Errorf("%w: XR needs at least 8 bytes, have %d", ErrTruncated, len(pkt))
	}
	x := &XR{SSRC: binary.BigEndian.Uint32(pkt[4:8])}
	for off := 8; off < len(pkt); {
		if len(pkt)-off < 4 {
			return nil, fmt.Errorf("%w: %d trailing bytes cannot hold an XR block header", ErrTruncated, len(pkt)-off)
		}
		bt := pkt[off]
		blen := (int(binary.BigEndian.Uint16(pkt[off+2:off+4])) + 1) * 4
		if off+blen > len(pkt) {
			return nil, fmt.Errorf("%w: XR block type %d declares %d bytes, %d remain", ErrTruncated, bt, blen, len(pkt)-off)
		}
		block := pkt[off : off+blen]
		switch bt {
		case BlockReceiverRefTime:
			if blen < 12 {
				return nil, fmt.Errorf("%w: receiver reference time block is %d bytes, need 12", ErrTruncated, blen)
			}
			x.Blocks = append(x.Blocks, &XRReceiverRefTime{
				NTPSec:  binary.BigEndian.Uint32(block[4:8]),
				NTPFrac: binary.BigEndian.Uint32(block[8:12]),
			})
		case BlockDLRR:
			if (blen-4)%12 != 0 {
				return nil, fmt.Errorf("%w: DLRR contents of %d bytes are not whole 12-byte items", ErrTruncated, blen-4)
			}
			d := &XRDLRR{}
			for i := 4; i < blen; i += 12 {
				d.Items = append(d.Items, DLRRItem{
					SSRC:   binary.BigEndian.Uint32(block[i : i+4]),
					LastRR: binary.BigEndian.Uint32(block[i+4 : i+8]),
					DLRR:   binary.BigEndian.Uint32(block[i+8 : i+12]),
				})
			}
			x.Blocks = append(x.Blocks, d)
		case BlockVoIPMetrics:
			if blen < 36 {
				return nil, fmt.Errorf("%w: VoIP metrics block is %d bytes, need 36", ErrTruncated, blen)
			}
			x.Blocks = append(x.Blocks, &XRVoIPMetrics{
				SSRC:           binary.BigEndian.Uint32(block[4:8]),
				LossRate:       block[8],
				DiscardRate:    block[9],
				BurstDensity:   block[10],
				GapDensity:     block[11],
				BurstDuration:  binary.BigEndian.Uint16(block[12:14]),
				GapDuration:    binary.BigEndian.Uint16(block[14:16]),
				RoundTripDelay: binary.BigEndian.Uint16(block[16:18]),
				EndSystemDelay: binary.BigEndian.Uint16(block[18:20]),
				SignalLevel:    block[20],
				NoiseLevel:     block[21],
				RERL:           block[22],
				Gmin:           block[23],
				RFactor:        block[24],
				ExtRFactor:     block[25],
				MOSLQ:          block[26],
				MOSCQ:          block[27],
				RXConfig:       block[28],
				JBNominal:      binary.BigEndian.Uint16(block[30:32]),
				JBMaximum:      binary.BigEndian.Uint16(block[32:34]),
				JBAbsMax:       binary.BigEndian.Uint16(block[34:36]),
			})
		default:
			// Unknown block type: skip it, like unknown packet types.
		}
		off += blen
	}
	return x, nil
}
