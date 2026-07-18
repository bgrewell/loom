// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"bytes"
	"reflect"
	"testing"
)

// goldenVectors are hand-built wire encodings, one per packet type and
// notable padding/edge shape. Each is checked both directions: AppendTo must
// produce exactly these bytes, and ParseCompound must reproduce exactly the
// source struct.
var goldenVectors = []struct {
	name string
	pkt  Packet
	want []byte
}{
	{
		name: "SR one report block, negative cumulative loss",
		pkt: &SenderReport{
			SSRC: 0x01020304, NTPSec: 0xA1A2A3A4, NTPFrac: 0xB1B2B3B4,
			RTPTime: 0xC1C2C3C4, PacketCount: 0x11121314, OctetCount: 0x21222324,
			Reports: []ReportBlock{{
				SSRC: 0xD1D2D3D4, FractionLost: 0x55, CumulativeLost: -2,
				ExtHighestSeq: 0xE1E2E3E4, Jitter: 0x00000777,
				LSR: 0xF1F2F3F4, DLSR: 0x00010203,
			}},
		},
		want: []byte{
			0x81, 0xC8, 0x00, 0x0C, // V=2, RC=1, PT=200, length 12 words
			0x01, 0x02, 0x03, 0x04, // SSRC
			0xA1, 0xA2, 0xA3, 0xA4, // NTP sec
			0xB1, 0xB2, 0xB3, 0xB4, // NTP frac
			0xC1, 0xC2, 0xC3, 0xC4, // RTP timestamp
			0x11, 0x12, 0x13, 0x14, // packet count
			0x21, 0x22, 0x23, 0x24, // octet count
			0xD1, 0xD2, 0xD3, 0xD4, // report: SSRC
			0x55, 0xFF, 0xFF, 0xFE, // fraction lost, cum lost −2 (24-bit two's complement)
			0xE1, 0xE2, 0xE3, 0xE4, // ext highest seq
			0x00, 0x00, 0x07, 0x77, // jitter
			0xF1, 0xF2, 0xF3, 0xF4, // LSR
			0x00, 0x01, 0x02, 0x03, // DLSR
		},
	},
	{
		name: "RR one report block",
		pkt: &ReceiverReport{
			SSRC: 0x11111111,
			Reports: []ReportBlock{{
				SSRC: 0xD1D2D3D4, FractionLost: 0x55, CumulativeLost: -2,
				ExtHighestSeq: 0xE1E2E3E4, Jitter: 0x00000777,
				LSR: 0xF1F2F3F4, DLSR: 0x00010203,
			}},
		},
		want: []byte{
			0x81, 0xC9, 0x00, 0x07,
			0x11, 0x11, 0x11, 0x11,
			0xD1, 0xD2, 0xD3, 0xD4,
			0x55, 0xFF, 0xFF, 0xFE,
			0xE1, 0xE2, 0xE3, 0xE4,
			0x00, 0x00, 0x07, 0x77,
			0xF1, 0xF2, 0xF3, 0xF4,
			0x00, 0x01, 0x02, 0x03,
		},
	},
	{
		name: "empty RR (RFC 3550 §6.1 header packet)",
		pkt:  &ReceiverReport{SSRC: 0x11111111},
		want: []byte{0x80, 0xC9, 0x00, 0x01, 0x11, 0x11, 0x11, 0x11},
	},
	{
		name: "SDES CNAME, one terminator null lands the boundary",
		pkt:  NewCNAME(0x22222222, "loom@host"),
		want: []byte{
			0x81, 0xCA, 0x00, 0x04, // V=2, SC=1
			0x22, 0x22, 0x22, 0x22,
			0x01, 0x09, 'l', 'o', 'o', 'm', '@', 'h', 'o', 's', 't',
			0x00, // terminator; already 32-bit aligned
		},
	},
	{
		name: "SDES CNAME ending on a boundary takes four nulls",
		pkt:  NewCNAME(0x22222222, "ab"),
		want: []byte{
			0x81, 0xCA, 0x00, 0x03,
			0x22, 0x22, 0x22, 0x22,
			0x01, 0x02, 'a', 'b',
			0x00, 0x00, 0x00, 0x00, // terminator + 3 pad nulls
		},
	},
	{
		name: "SDES two chunks, second with a non-CNAME item",
		pkt: &SDES{Chunks: []SDESChunk{
			{SSRC: 0x22222222, Items: []SDESItem{{Type: SDESCNAME, Text: "ab"}}},
			{SSRC: 0x23232323, Items: []SDESItem{{Type: 7, Text: "x"}}}, // NOTE
		}},
		want: []byte{
			0x82, 0xCA, 0x00, 0x05,
			0x22, 0x22, 0x22, 0x22,
			0x01, 0x02, 'a', 'b', 0x00, 0x00, 0x00, 0x00,
			0x23, 0x23, 0x23, 0x23,
			0x07, 0x01, 'x', 0x00,
		},
	},
	{
		name: "BYE without reason",
		pkt:  &Bye{SSRCs: []uint32{0x33333333}},
		want: []byte{0x81, 0xCB, 0x00, 0x01, 0x33, 0x33, 0x33, 0x33},
	},
	{
		name: "BYE with padded reason",
		pkt:  &Bye{SSRCs: []uint32{0x33333333}, Reason: "seeya"},
		want: []byte{
			0x81, 0xCB, 0x00, 0x03,
			0x33, 0x33, 0x33, 0x33,
			0x05, 's', 'e', 'e', 'y', 'a', 0x00, 0x00,
		},
	},
	{
		name: "BYE, two SSRCs, reason landing the boundary",
		pkt:  &Bye{SSRCs: []uint32{0x33333333, 0x34343434}, Reason: "bye"},
		want: []byte{
			0x82, 0xCB, 0x00, 0x03,
			0x33, 0x33, 0x33, 0x33,
			0x34, 0x34, 0x34, 0x34,
			0x03, 'b', 'y', 'e',
		},
	},
	{
		name: "XR receiver reference time (BT=4)",
		pkt: &XR{SSRC: 0x44444444, Blocks: []XRBlock{
			&XRReceiverRefTime{NTPSec: 0xA0B0C0D0, NTPFrac: 0x0F0E0D0C},
		}},
		want: []byte{
			0x80, 0xCF, 0x00, 0x04,
			0x44, 0x44, 0x44, 0x44,
			0x04, 0x00, 0x00, 0x02, // BT=4, block length 2 words
			0xA0, 0xB0, 0xC0, 0xD0,
			0x0F, 0x0E, 0x0D, 0x0C,
		},
	},
	{
		name: "XR DLRR (BT=5), two items",
		pkt: &XR{SSRC: 0x44444444, Blocks: []XRBlock{
			&XRDLRR{Items: []DLRRItem{
				{SSRC: 0x51515151, LastRR: 0x52525252, DLRR: 0x53535353},
				{SSRC: 0x61616161, LastRR: 0x62626262, DLRR: 0x63636363},
			}},
		}},
		want: []byte{
			0x80, 0xCF, 0x00, 0x08,
			0x44, 0x44, 0x44, 0x44,
			0x05, 0x00, 0x00, 0x06, // BT=5, block length 3n = 6 words
			0x51, 0x51, 0x51, 0x51, 0x52, 0x52, 0x52, 0x52, 0x53, 0x53, 0x53, 0x53,
			0x61, 0x61, 0x61, 0x61, 0x62, 0x62, 0x62, 0x62, 0x63, 0x63, 0x63, 0x63,
		},
	},
	{
		name: "XR VoIP metrics (BT=7), every field",
		pkt: &XR{SSRC: 0x44444444, Blocks: []XRBlock{
			&XRVoIPMetrics{
				SSRC:     0xAABBCCDD,
				LossRate: 25, DiscardRate: 3, BurstDensity: 200, GapDensity: 10,
				BurstDuration: 320, GapDuration: 5000, RoundTripDelay: 150, EndSystemDelay: 60,
				SignalLevel: Unavailable, NoiseLevel: Unavailable, RERL: Unavailable, Gmin: 16,
				RFactor: 82, ExtRFactor: Unavailable, MOSLQ: 41, MOSCQ: 40,
				RXConfig:  0,
				JBNominal: 40, JBMaximum: 120, JBAbsMax: 200,
			},
		}},
		want: []byte{
			0x80, 0xCF, 0x00, 0x0A,
			0x44, 0x44, 0x44, 0x44,
			0x07, 0x00, 0x00, 0x08, // BT=7, block length 8 words
			0xAA, 0xBB, 0xCC, 0xDD, // SSRC of source
			0x19, 0x03, 0xC8, 0x0A, // loss rate 25, discard 3, burst density 200, gap density 10
			0x01, 0x40, 0x13, 0x88, // burst duration 320 ms, gap duration 5000 ms
			0x00, 0x96, 0x00, 0x3C, // RTD 150 ms, end system delay 60 ms
			0x7F, 0x7F, 0x7F, 0x10, // signal/noise/RERL unavailable, Gmin 16
			0x52, 0x7F, 0x29, 0x28, // R 82, ext R unavailable, MOS-LQ 4.1, MOS-CQ 4.0 (×10)
			0x00, 0x00, 0x00, 0x28, // RX config, reserved, JB nominal 40 ms
			0x00, 0x78, 0x00, 0xC8, // JB maximum 120 ms, JB abs max 200 ms
		},
	},
	{
		name: "XR with reference time and VoIP metrics together",
		pkt: &XR{SSRC: 0x44444444, Blocks: []XRBlock{
			&XRReceiverRefTime{NTPSec: 1, NTPFrac: 2},
			&XRVoIPMetrics{SSRC: 3, Gmin: 16, RFactor: Unavailable, ExtRFactor: Unavailable, MOSLQ: Unavailable, MOSCQ: Unavailable, SignalLevel: Unavailable, NoiseLevel: Unavailable, RERL: Unavailable},
		}},
		want: []byte{
			0x80, 0xCF, 0x00, 0x0D,
			0x44, 0x44, 0x44, 0x44,
			0x04, 0x00, 0x00, 0x02,
			0x00, 0x00, 0x00, 0x01,
			0x00, 0x00, 0x00, 0x02,
			0x07, 0x00, 0x00, 0x08,
			0x00, 0x00, 0x00, 0x03,
			0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00,
			0x7F, 0x7F, 0x7F, 0x10,
			0x7F, 0x7F, 0x7F, 0x7F,
			0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00,
		},
	},
}

// TestGoldenVectors pins every packet type's wire encoding against
// hand-built byte slices, both marshalling and re-parsing.
func TestGoldenVectors(t *testing.T) {
	for _, tt := range goldenVectors {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.pkt.AppendTo(nil)
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("AppendTo:\n got %x\nwant %x", got, tt.want)
			}
			pkts, err := ParseCompound(tt.want)
			if err != nil {
				t.Fatalf("ParseCompound: %v", err)
			}
			if len(pkts) != 1 {
				t.Fatalf("ParseCompound returned %d packets, want 1", len(pkts))
			}
			if !reflect.DeepEqual(pkts[0], tt.pkt) {
				t.Fatalf("round trip:\n got %#v\nwant %#v", pkts[0], tt.pkt)
			}
		})
	}
}

// TestAppendToAppends pins that AppendTo extends the given slice rather than
// clobbering it.
func TestAppendToAppends(t *testing.T) {
	prefix := []byte{0xDE, 0xAD}
	rr := &ReceiverReport{SSRC: 1}
	got := rr.AppendTo(append([]byte(nil), prefix...))
	if !bytes.Equal(got[:2], prefix) {
		t.Fatalf("prefix clobbered: %x", got[:2])
	}
	if !bytes.Equal(got[2:], rr.AppendTo(nil)) {
		t.Fatalf("appended encoding differs from fresh encoding")
	}
}

// TestCumulativeLostWireClamp pins the A.3 24-bit wire clamp in the report
// block encoder and the sign extension in the parser: the clamp exists only
// on the wire, so out-of-range struct values marshal to the saturated field.
func TestCumulativeLostWireClamp(t *testing.T) {
	tests := []struct {
		name     string
		lost     int32
		wantWire [3]byte
		wantBack int32
	}{
		{"max positive", 0x7FFFFF, [3]byte{0x7F, 0xFF, 0xFF}, 0x7FFFFF},
		{"clamped positive", 0x800000, [3]byte{0x7F, 0xFF, 0xFF}, 0x7FFFFF},
		{"minus one", -1, [3]byte{0xFF, 0xFF, 0xFF}, -1},
		{"max negative", -0x800000, [3]byte{0x80, 0x00, 0x00}, -0x800000},
		{"clamped negative", -0x800001, [3]byte{0x80, 0x00, 0x00}, -0x800000},
		{"zero", 0, [3]byte{0x00, 0x00, 0x00}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rb := ReportBlock{CumulativeLost: tt.lost}
			b := rb.appendTo(nil)
			if got := [3]byte{b[5], b[6], b[7]}; got != tt.wantWire {
				t.Errorf("wire bytes = %x, want %x", got, tt.wantWire)
			}
			if got := parseReportBlock(b).CumulativeLost; got != tt.wantBack {
				t.Errorf("parsed = %d, want %d", got, tt.wantBack)
			}
		})
	}
}

// TestSDESCNAME pins the CNAME lookup across chunk and item positions.
func TestSDESCNAME(t *testing.T) {
	tests := []struct {
		name   string
		sdes   *SDES
		want   string
		wantOK bool
	}{
		{"NewCNAME", NewCNAME(1, "a@b"), "a@b", true},
		{"no items", &SDES{Chunks: []SDESChunk{{SSRC: 1}}}, "", false},
		{"non-CNAME only", &SDES{Chunks: []SDESChunk{{SSRC: 1, Items: []SDESItem{{Type: 7, Text: "n"}}}}}, "", false},
		{
			"CNAME in second chunk",
			&SDES{Chunks: []SDESChunk{
				{SSRC: 1, Items: []SDESItem{{Type: 7, Text: "n"}}},
				{SSRC: 2, Items: []SDESItem{{Type: SDESCNAME, Text: "c@d"}}},
			}},
			"c@d", true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.sdes.CNAME()
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("CNAME() = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestAppendToPanics pins the documented panic contract for unencodable
// values (5-bit count, 8-bit text length, and 16-bit packet length field
// overflows).
func TestAppendToPanics(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{"SR 32 report blocks", func() {
			(&SenderReport{Reports: make([]ReportBlock, 32)}).AppendTo(nil)
		}},
		{"RR 32 report blocks", func() {
			(&ReceiverReport{Reports: make([]ReportBlock, 32)}).AppendTo(nil)
		}},
		{"SDES 32 chunks", func() {
			(&SDES{Chunks: make([]SDESChunk, 32)}).AppendTo(nil)
		}},
		{"SDES item type 0", func() {
			(&SDES{Chunks: []SDESChunk{{Items: []SDESItem{{Type: 0}}}}}).AppendTo(nil)
		}},
		{"SDES text over 255", func() {
			NewCNAME(1, string(make([]byte, 256))).AppendTo(nil)
		}},
		{"BYE 32 SSRCs", func() {
			(&Bye{SSRCs: make([]uint32, 32)}).AppendTo(nil)
		}},
		{"BYE reason over 255", func() {
			(&Bye{SSRCs: []uint32{1}, Reason: string(make([]byte, 256))}).AppendTo(nil)
		}},
		// 21845 DLRR items pass the block's own 16-bit length guard
		// (3·21845 = 65535 words) but push the PACKET to 65538 words —
		// finishPacket must panic rather than wrap the 16-bit RTCP length
		// field and silently emit a compound no parser can walk.
		{"XR packet over 16-bit length field", func() {
			(&XR{Blocks: []XRBlock{&XRDLRR{Items: make([]DLRRItem, 21845)}}}).AppendTo(nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("AppendTo succeeded, want panic")
				}
			}()
			tt.fn()
		})
	}
}
