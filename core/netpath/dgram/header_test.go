// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package dgram

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
)

var (
	testSrc = netip.MustParseAddrPort("10.0.0.1:40000")
	testDst = netip.MustParseAddrPort("10.0.0.2:5060")
)

// TestEncodeParseRoundTrip verifies parsePacket accepts what encodePacket
// produces, across payload sizes (odd lengths exercise checksum padding), and
// returns the encoded endpoints and payload.
func TestEncodeParseRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, 2, 3, 160, 1471, 1472} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i * 7)
		}
		b := make([]byte, headersLen+size)
		n := encodePacket(b, testSrc, testDst, 42, payload)
		if n != headersLen+size {
			t.Fatalf("size %d: encoded length = %d, want %d", size, n, headersLen+size)
		}
		src, dst, got, reason := parsePacket(b[:n])
		if reason != dropNone {
			t.Fatalf("size %d: parse reason = %d, want dropNone", size, reason)
		}
		if src != testSrc || dst != testDst {
			t.Fatalf("size %d: endpoints = %v→%v, want %v→%v", size, src, dst, testSrc, testDst)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("size %d: payload mismatch", size)
		}
	}
}

// TestEncodeChecksumNeverZero verifies the wire UDP checksum is never the
// "not computed" zero value: a computed zero must be transmitted as 0xFFFF
// (RFC 768). The sweep varies the payload so some sums land near the
// wrap-around values.
func TestEncodeChecksumNeverZero(t *testing.T) {
	b := make([]byte, headersLen+2)
	for i := 0; i < 1<<16; i += 3 {
		payload := []byte{byte(i >> 8), byte(i)}
		n := encodePacket(b, testSrc, testDst, uint16(i), payload)
		if ck := binary.BigEndian.Uint16(b[ipv4HeaderLen+6:]); ck == 0 {
			t.Fatalf("payload %#04x: wire UDP checksum is 0", i)
		}
		if _, _, _, reason := parsePacket(b[:n]); reason != dropNone {
			t.Fatalf("payload %#04x: parse reason = %d, want dropNone", i, reason)
		}
	}
}

// TestEncodeZeroChecksumBecomesFFFF constructs a payload whose computed UDP
// checksum is zero and verifies it is transmitted as 0xFFFF (RFC 768) and
// still parses as valid.
func TestEncodeZeroChecksumBecomesFFFF(t *testing.T) {
	// With a zero 2-byte payload, the wire checksum c reveals the folded sum
	// S0 of everything else (c = ^S0). A payload word of 0xFFFF-S0 then makes
	// the total sum 0xFFFF, whose complement is the computed zero.
	b := make([]byte, headersLen+2)
	encodePacket(b, testSrc, testDst, 9, []byte{0, 0})
	s0 := ^binary.BigEndian.Uint16(b[ipv4HeaderLen+6:])
	w := 0xFFFF - s0
	n := encodePacket(b, testSrc, testDst, 9, []byte{byte(w >> 8), byte(w)})
	if ck := binary.BigEndian.Uint16(b[ipv4HeaderLen+6:]); ck != 0xFFFF {
		t.Fatalf("wire UDP checksum = %#04x, want 0xFFFF for a computed zero", ck)
	}
	if _, _, _, reason := parsePacket(b[:n]); reason != dropNone {
		t.Fatalf("parse reason = %d, want dropNone", reason)
	}
}

// TestParseAcceptsZeroUDPChecksum verifies a zero UDP checksum (legal for
// IPv4: "not computed") passes validation.
func TestParseAcceptsZeroUDPChecksum(t *testing.T) {
	b := make([]byte, headersLen+4)
	n := encodePacket(b, testSrc, testDst, 1, []byte("ping"))
	b[ipv4HeaderLen+6], b[ipv4HeaderLen+7] = 0, 0 // clear the UDP checksum
	if _, _, _, reason := parsePacket(b[:n]); reason != dropNone {
		t.Fatalf("parse reason = %d, want dropNone for zero UDP checksum", reason)
	}
}

// TestParseRejects table-drives the drop classifications.
func TestParseRejects(t *testing.T) {
	valid := func() []byte {
		b := make([]byte, headersLen+4)
		encodePacket(b, testSrc, testDst, 7, []byte("ping"))
		return b
	}
	// reencodeIPChecksum fixes the IPv4 header checksum after a mutation, so
	// the packet fails only the later check under test.
	reencodeIPChecksum := func(b []byte) {
		b[10], b[11] = 0, 0
		binary.BigEndian.PutUint16(b[10:], onesComplement(sumBytes(0, b[:ipv4HeaderLen])))
	}
	cases := []struct {
		name   string
		mutate func(b []byte) []byte
		want   dropReason
	}{
		{"short", func(b []byte) []byte { return b[:10] }, dropBadIPHeader},
		{"ipv6-version", func(b []byte) []byte { b[0] = 0x65; return b }, dropBadIPHeader},
		{"bad-ihl", func(b []byte) []byte { b[0] = 0x44; return b }, dropBadIPHeader},
		{"total-length-overrun", func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[2:], uint16(len(b)+1))
			reencodeIPChecksum(b)
			return b
		}, dropBadIPHeader},
		{"ip-checksum", func(b []byte) []byte { b[10] ^= 0xFF; return b }, dropBadIPHeader},
		{"fragment-offset", func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[6:], 0x0001)
			reencodeIPChecksum(b)
			return b
		}, dropFragmented},
		{"more-fragments", func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[6:], 0x2000)
			reencodeIPChecksum(b)
			return b
		}, dropFragmented},
		{"tcp-protocol", func(b []byte) []byte {
			b[9] = 6
			reencodeIPChecksum(b)
			return b
		}, dropNotUDP},
		{"udp-length-short", func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[ipv4HeaderLen+4:], udpHeaderLen-1)
			return b
		}, dropBadUDPHeader},
		{"udp-length-overrun", func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[ipv4HeaderLen+4:], uint16(len(b)-ipv4HeaderLen+1))
			return b
		}, dropBadUDPHeader},
		{"udp-checksum", func(b []byte) []byte { b[len(b)-1] ^= 0xFF; return b }, dropBadUDPChecksum},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, reason := parsePacket(tc.mutate(valid()))
			if reason != tc.want {
				t.Errorf("parse reason = %d, want %d", reason, tc.want)
			}
		})
	}
}
