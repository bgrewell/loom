// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package dgram

import (
	"encoding/binary"
	"net/netip"
)

// Wire-format constants for the IPv4+UDP headers this package encodes.
const (
	ipv4HeaderLen = 20                           // IHL 5, no options
	udpHeaderLen  = 8                            // RFC 768 header
	headersLen    = ipv4HeaderLen + udpHeaderLen // total overhead per datagram

	ipv4Version = 4
	protoUDP    = 17
	ipv4TTL     = 64     // conventional default TTL
	ipv4FlagDF  = 0x4000 // Don't Fragment: this package never fragments
	// ipv4FragMask covers the MF flag (0x2000) and the 13-bit fragment
	// offset: any set bit means the packet is a fragment (or expects more).
	ipv4FragMask = 0x3FFF
)

// encodePacket writes one complete IPv4+UDP packet — 20-byte optionless IPv4
// header, 8-byte UDP header, then payload — into b and returns the total
// length. Both checksums are computed: the IPv4 header checksum (RFC 791) and
// the UDP checksum over the pseudo-header (RFC 768; a computed zero is
// transmitted as 0xFFFF, so the wire checksum is never the "not computed"
// value). The caller guarantees len(b) >= headersLen+len(payload) and that
// src and dst are IPv4.
func encodePacket(b []byte, src, dst netip.AddrPort, id uint16, payload []byte) int {
	total := headersLen + len(payload)
	sa, da := src.Addr().As4(), dst.Addr().As4()

	// IPv4 header.
	b[0] = ipv4Version<<4 | ipv4HeaderLen/4
	b[1] = 0 // DSCP/ECN
	binary.BigEndian.PutUint16(b[2:], uint16(total))
	binary.BigEndian.PutUint16(b[4:], id)
	binary.BigEndian.PutUint16(b[6:], ipv4FlagDF)
	b[8] = ipv4TTL
	b[9] = protoUDP
	b[10], b[11] = 0, 0 // checksum field zero while summing
	copy(b[12:16], sa[:])
	copy(b[16:20], da[:])
	binary.BigEndian.PutUint16(b[10:], onesComplement(sumBytes(0, b[:ipv4HeaderLen])))

	// UDP header + payload.
	u := b[ipv4HeaderLen:total]
	binary.BigEndian.PutUint16(u[0:], src.Port())
	binary.BigEndian.PutUint16(u[2:], dst.Port())
	binary.BigEndian.PutUint16(u[4:], uint16(udpHeaderLen+len(payload)))
	u[6], u[7] = 0, 0 // checksum field zero while summing
	copy(u[udpHeaderLen:], payload)
	sum := udpChecksum(sa, da, u)
	if sum == 0 {
		sum = 0xFFFF // RFC 768: computed zero is transmitted as all-ones
	}
	binary.BigEndian.PutUint16(u[6:], sum)
	return total
}

// dropReason classifies why an inbound frame was discarded, mapping 1:1 onto
// the DropStats counters (dropNone means the packet is deliverable).
type dropReason int

const (
	dropNone dropReason = iota
	dropBadIPHeader
	dropFragmented
	dropNotUDP
	dropBadUDPHeader
	dropBadUDPChecksum
)

// parsePacket validates b as one IPv4+UDP packet and returns the source and
// destination endpoints plus the UDP payload (aliasing b — the caller copies
// before releasing the frame). Validation order: IPv4 header shape (length,
// version, IHL, total length) and header checksum, then fragmentation, then
// protocol, then UDP header shape and checksum. A UDP checksum of zero is
// accepted without verification — "not computed" is legal for IPv4 (RFC 768).
func parsePacket(b []byte) (src, dst netip.AddrPort, payload []byte, reason dropReason) {
	if len(b) < ipv4HeaderLen || b[0]>>4 != ipv4Version {
		return src, dst, nil, dropBadIPHeader
	}
	ihl := int(b[0]&0x0F) * 4
	if ihl < ipv4HeaderLen || ihl > len(b) {
		return src, dst, nil, dropBadIPHeader
	}
	total := int(binary.BigEndian.Uint16(b[2:]))
	if total < ihl+udpHeaderLen || total > len(b) {
		return src, dst, nil, dropBadIPHeader
	}
	if onesComplement(sumBytes(0, b[:ihl])) != 0 {
		return src, dst, nil, dropBadIPHeader
	}
	if binary.BigEndian.Uint16(b[6:])&ipv4FragMask != 0 {
		return src, dst, nil, dropFragmented
	}
	if b[9] != protoUDP {
		return src, dst, nil, dropNotUDP
	}

	seg := b[ihl:total]
	udpLen := int(binary.BigEndian.Uint16(seg[4:]))
	if udpLen < udpHeaderLen || udpLen > len(seg) {
		return src, dst, nil, dropBadUDPHeader
	}
	seg = seg[:udpLen]
	var sa, da [4]byte
	copy(sa[:], b[12:16])
	copy(da[:], b[16:20])
	if binary.BigEndian.Uint16(seg[6:]) != 0 && udpChecksum(sa, da, seg) != 0 {
		return src, dst, nil, dropBadUDPChecksum
	}
	src = netip.AddrPortFrom(netip.AddrFrom4(sa), binary.BigEndian.Uint16(seg[0:]))
	dst = netip.AddrPortFrom(netip.AddrFrom4(da), binary.BigEndian.Uint16(seg[2:]))
	return src, dst, seg[udpHeaderLen:], dropNone
}

// udpChecksum returns the one's-complement UDP checksum over the RFC 768
// pseudo-header (src, dst, zero, protocol, UDP length) and the UDP segment as
// given. With the segment's checksum field zeroed it computes the value to
// transmit; over a received segment (checksum field in place) a valid packet
// yields zero.
func udpChecksum(src, dst [4]byte, seg []byte) uint16 {
	var pseudo [12]byte
	copy(pseudo[0:4], src[:])
	copy(pseudo[4:8], dst[:])
	pseudo[9] = protoUDP
	binary.BigEndian.PutUint16(pseudo[10:], uint16(len(seg)))
	return onesComplement(sumBytes(sumBytes(0, pseudo[:]), seg))
}

// sumBytes accumulates b into an internet-checksum partial sum (RFC 1071):
// big-endian 16-bit words, an odd trailing byte padded with zero.
func sumBytes(sum uint32, b []byte) uint32 {
	for ; len(b) >= 2; b = b[2:] {
		sum += uint32(b[0])<<8 | uint32(b[1])
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	return sum
}

// onesComplement folds a partial sum and returns its one's complement — the
// checksum to transmit, or zero when verifying an intact packet.
func onesComplement(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = sum&0xFFFF + sum>>16
	}
	return ^uint16(sum)
}
