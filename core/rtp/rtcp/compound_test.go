// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// appPacket is a well-formed APP packet (PT=204), a type this package
// length-walks but does not parse.
var appPacket = []byte{
	0x80, 0xCC, 0x00, 0x02,
	0x99, 0x99, 0x99, 0x99, // SSRC
	'l', 'o', 'o', 'm', // name
}

// TestMarshalCompoundEnforcement pins the RFC 3550 §6.1 rules: SR/RR first
// and an SDES CNAME present, with typed errors otherwise.
func TestMarshalCompoundEnforcement(t *testing.T) {
	sr := &SenderReport{SSRC: 1}
	rr := &ReceiverReport{SSRC: 2}
	cname := NewCNAME(1, "a@b")
	noCNAME := &SDES{Chunks: []SDESChunk{{SSRC: 1, Items: []SDESItem{{Type: 7, Text: "note"}}}}}
	bye := &Bye{SSRCs: []uint32{1}}

	tests := []struct {
		name    string
		pkts    []Packet
		wantErr error // nil means success
	}{
		{"SR + CNAME", []Packet{sr, cname}, nil},
		{"RR + CNAME", []Packet{rr, cname}, nil},
		{"RR + CNAME + BYE", []Packet{rr, cname, bye}, nil},
		{"CNAME in a later SDES", []Packet{sr, noCNAME, cname}, nil},
		{"empty", nil, ErrEmptyCompound},
		{"SDES first", []Packet{cname, sr}, ErrFirstPacket},
		{"BYE first", []Packet{bye, cname}, ErrFirstPacket},
		{"XR first", []Packet{&XR{SSRC: 1}, cname}, ErrFirstPacket},
		{"SR alone", []Packet{sr}, ErrNoCNAME},
		{"SR + SDES without CNAME", []Packet{sr, noCNAME}, ErrNoCNAME},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MarshalCompound(tt.pkts...)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want errors.Is %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MarshalCompound: %v", err)
			}
			var want []byte
			for _, p := range tt.pkts {
				want = p.AppendTo(want)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("compound is not the packet concatenation:\n got %x\nwant %x", got, want)
			}
		})
	}
}

// TestFullCompoundRoundTrip pins a complete SR + SDES + XR(VoIP metrics)
// compound — the shape loom's media sessions emit — against a hand-built
// wire image, marshal and parse.
func TestFullCompoundRoundTrip(t *testing.T) {
	sr := &SenderReport{
		SSRC: 0x01020304, NTPSec: 0xA1A2A3A4, NTPFrac: 0xB1B2B3B4,
		RTPTime: 0xC1C2C3C4, PacketCount: 0x11121314, OctetCount: 0x21222324,
		Reports: []ReportBlock{{
			SSRC: 0xD1D2D3D4, FractionLost: 0x55, CumulativeLost: -2,
			ExtHighestSeq: 0xE1E2E3E4, Jitter: 0x00000777,
			LSR: 0xF1F2F3F4, DLSR: 0x00010203,
		}},
	}
	sdes := NewCNAME(0x01020304, "loom@host")
	xr := &XR{SSRC: 0x01020304, Blocks: []XRBlock{
		&XRVoIPMetrics{
			SSRC:     0xD1D2D3D4,
			LossRate: 25, DiscardRate: 3, BurstDensity: 200, GapDensity: 10,
			BurstDuration: 320, GapDuration: 5000, RoundTripDelay: 150, EndSystemDelay: 60,
			SignalLevel: Unavailable, NoiseLevel: Unavailable, RERL: Unavailable, Gmin: 16,
			RFactor: 82, ExtRFactor: Unavailable, MOSLQ: 41, MOSCQ: 40,
			JBNominal: 40, JBMaximum: 120, JBAbsMax: 200,
		},
	}}

	want := []byte{
		// SR
		0x81, 0xC8, 0x00, 0x0C,
		0x01, 0x02, 0x03, 0x04,
		0xA1, 0xA2, 0xA3, 0xA4,
		0xB1, 0xB2, 0xB3, 0xB4,
		0xC1, 0xC2, 0xC3, 0xC4,
		0x11, 0x12, 0x13, 0x14,
		0x21, 0x22, 0x23, 0x24,
		0xD1, 0xD2, 0xD3, 0xD4,
		0x55, 0xFF, 0xFF, 0xFE,
		0xE1, 0xE2, 0xE3, 0xE4,
		0x00, 0x00, 0x07, 0x77,
		0xF1, 0xF2, 0xF3, 0xF4,
		0x00, 0x01, 0x02, 0x03,
		// SDES
		0x81, 0xCA, 0x00, 0x04,
		0x01, 0x02, 0x03, 0x04,
		0x01, 0x09, 'l', 'o', 'o', 'm', '@', 'h', 'o', 's', 't',
		0x00,
		// XR + VoIP metrics
		0x80, 0xCF, 0x00, 0x0A,
		0x01, 0x02, 0x03, 0x04,
		0x07, 0x00, 0x00, 0x08,
		0xD1, 0xD2, 0xD3, 0xD4,
		0x19, 0x03, 0xC8, 0x0A,
		0x01, 0x40, 0x13, 0x88,
		0x00, 0x96, 0x00, 0x3C,
		0x7F, 0x7F, 0x7F, 0x10,
		0x52, 0x7F, 0x29, 0x28,
		0x00, 0x00, 0x00, 0x28,
		0x00, 0x78, 0x00, 0xC8,
	}

	got, err := MarshalCompound(sr, sdes, xr)
	if err != nil {
		t.Fatalf("MarshalCompound: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("compound wire image:\n got %x\nwant %x", got, want)
	}

	pkts, err := ParseCompound(want)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	if len(pkts) != 3 {
		t.Fatalf("parsed %d packets, want 3", len(pkts))
	}
	for i, wantPkt := range []Packet{sr, sdes, xr} {
		if !reflect.DeepEqual(pkts[i], wantPkt) {
			t.Errorf("packet %d:\n got %#v\nwant %#v", i, pkts[i], wantPkt)
		}
	}
}

// TestParseCompoundSkipsUnknownTypes pins the tolerant length-walk: unknown
// packet types (APP here, plus an unassigned type) are framed and skipped,
// and the known packets around them still parse.
func TestParseCompoundSkipsUnknownTypes(t *testing.T) {
	rr := &ReceiverReport{SSRC: 0x11111111}
	bye := &Bye{SSRCs: []uint32{0x33333333}}

	unknown := []byte{0x80, 0xCD, 0x00, 0x01, 0xAB, 0xCD, 0xEF, 0x01} // PT=205 (RTPFB), skipped

	var b []byte
	b = rr.AppendTo(b)
	b = append(b, appPacket...)
	b = append(b, unknown...)
	b = bye.AppendTo(b)

	pkts, err := ParseCompound(b)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	if len(pkts) != 2 {
		t.Fatalf("parsed %d packets, want 2 (unknown types skipped)", len(pkts))
	}
	if !reflect.DeepEqual(pkts[0], rr) || !reflect.DeepEqual(pkts[1], bye) {
		t.Fatalf("parsed packets = %#v, %#v; want the RR and BYE", pkts[0], pkts[1])
	}
}

// TestParseCompoundSkipsUnknownXRBlocks pins that an unknown XR block type
// is skipped by its block length while later blocks still parse.
func TestParseCompoundSkipsUnknownXRBlocks(t *testing.T) {
	b := []byte{
		0x80, 0xCF, 0x00, 0x06,
		0x44, 0x44, 0x44, 0x44,
		0x06, 0x00, 0x00, 0x01, // BT=6 (statistics summary), 8 bytes, unknown here
		0xAA, 0xAA, 0xAA, 0xAA,
		0x04, 0x00, 0x00, 0x02, // BT=4 follows and must still parse
		0x00, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x02,
	}
	pkts, err := ParseCompound(b)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	want := &XR{SSRC: 0x44444444, Blocks: []XRBlock{&XRReceiverRefTime{NTPSec: 1, NTPFrac: 2}}}
	if len(pkts) != 1 || !reflect.DeepEqual(pkts[0], want) {
		t.Fatalf("parsed = %#v, want %#v", pkts, want)
	}
}

// TestParseCompoundErrors pins the framing errors: truncation at every
// level, bad versions, and known packets whose contents overrun their
// declared length.
func TestParseCompoundErrors(t *testing.T) {
	rr := (&ReceiverReport{SSRC: 1}).AppendTo(nil)
	tests := []struct {
		name    string
		b       []byte
		wantErr error
	}{
		{"empty", nil, ErrTruncated},
		{"three bytes", []byte{0x80, 0xC9, 0x00}, ErrTruncated},
		{"trailing bytes after packet", append(append([]byte{}, rr...), 0x80, 0xC9), ErrTruncated},
		{"version 0", []byte{0x00, 0xC9, 0x00, 0x01, 0, 0, 0, 0}, ErrVersion},
		{"version 1", []byte{0x40, 0xC9, 0x00, 0x01, 0, 0, 0, 0}, ErrVersion},
		{"version 3", []byte{0xC0, 0xC9, 0x00, 0x01, 0, 0, 0, 0}, ErrVersion},
		{"bad version on second packet", append(append([]byte{}, rr...), 0x00, 0xC9, 0x00, 0x01, 0, 0, 0, 0), ErrVersion},
		{"declared length overruns datagram", []byte{0x80, 0xC9, 0x00, 0x02, 0, 0, 0, 0}, ErrTruncated},
		{
			// Length says 28 bytes but RC=1 needs 52: fields overrun.
			"SR fields overrun declared length",
			[]byte{
				0x81, 0xC8, 0x00, 0x06,
				0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3,
				0, 0, 0, 4, 0, 0, 0, 5, 0, 0, 0, 6,
			},
			ErrTruncated,
		},
		{
			"RR fields overrun declared length",
			[]byte{0x81, 0xC9, 0x00, 0x01, 0, 0, 0, 1},
			ErrTruncated,
		},
		{
			"SDES chunk unterminated",
			[]byte{0x81, 0xCA, 0x00, 0x02, 0, 0, 0, 1, 0x01, 0x02, 'a', 'b'},
			ErrTruncated,
		},
		{
			"SDES item text overruns",
			[]byte{0x81, 0xCA, 0x00, 0x02, 0, 0, 0, 1, 0x01, 0xFF, 'a', 'b'},
			ErrTruncated,
		},
		{
			"SDES chunk without SSRC",
			[]byte{0x81, 0xCA, 0x00, 0x00},
			ErrTruncated,
		},
		{
			"BYE SSRC list overruns",
			[]byte{0x82, 0xCB, 0x00, 0x01, 0, 0, 0, 1},
			ErrTruncated,
		},
		{
			"BYE reason overruns",
			[]byte{0x81, 0xCB, 0x00, 0x02, 0, 0, 0, 1, 0x07, 'x', 'y', 'z'},
			ErrTruncated,
		},
		{
			"XR without reporter SSRC",
			[]byte{0x80, 0xCF, 0x00, 0x00},
			ErrTruncated,
		},
		{
			"XR block overruns packet",
			[]byte{0x80, 0xCF, 0x00, 0x02, 0, 0, 0, 1, 0x04, 0x00, 0x00, 0x02},
			ErrTruncated,
		},
		{
			"XR DLRR contents not whole items",
			[]byte{0x80, 0xCF, 0x00, 0x03, 0, 0, 0, 1, 0x05, 0x00, 0x00, 0x01, 0, 0, 0, 2},
			ErrTruncated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCompound(tt.b)
			if err == nil {
				t.Fatal("ParseCompound succeeded, want error")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want errors.Is %v", err, tt.wantErr)
			}
		})
	}
}

// TestIsRTCP pins the RFC 5761 §4 classification: version 2 with the second
// octet in [192, 223] is RTCP (equivalently, RTP payload types 64–95 with
// the marker bit — the values RFC 5761 forbids RTP from using); everything
// else, including RTP with any legal payload type and non-RTP protocols
// whose leading bits are not 10, is not.
func TestIsRTCP(t *testing.T) {
	// pkt builds a 4-byte prefix with the given first two octets.
	pkt := func(b0, b1 byte) []byte { return []byte{b0, b1, 0x00, 0x01} }
	tests := []struct {
		name string
		b    []byte
		want bool
	}{
		{"SR (200)", pkt(0x80, 200), true},
		{"RR (201)", pkt(0x81, 201), true},
		{"SDES (202)", pkt(0x81, 202), true},
		{"BYE (203)", pkt(0x81, 203), true},
		{"APP (204)", pkt(0x80, 204), true},
		{"XR (207)", pkt(0x80, 207), true},
		{"lower bound (192)", pkt(0x80, 192), true},
		{"upper bound (223)", pkt(0x80, 223), true},
		{"below range (191)", pkt(0x80, 191), false},
		{"above range (224 = marker + PT 96)", pkt(0x80, 224), false},
		{"RTP PT 0, no marker", pkt(0x80, 0), false},
		{"RTP PT 0, marker (128)", pkt(0x80, 128), false},
		{"RTP PT 127, no marker", pkt(0x80, 127), false},
		{"forbidden RTP PT 72 with marker collides with SR", pkt(0x80, 0x80|72), true},
		{"version 0 (STUN-like)", pkt(0x00, 200), false},
		{"version 1", pkt(0x40, 200), false},
		{"version 3", pkt(0xC0, 200), false},
		{"too short", []byte{0x80, 200, 0x00}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRTCP(tt.b); got != tt.want {
				t.Fatalf("IsRTCP(%x) = %v, want %v", tt.b, got, tt.want)
			}
		})
	}
}
