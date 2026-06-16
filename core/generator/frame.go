// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package generator

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/bgrewell/loom/core/payload"
)

// frameHeaderLen is Ethernet(14) + IPv4(20) + UDP(8).
const frameHeaderLen = 14 + 20 + 8

// FrameOptions carries the layer-2/3/4 addressing the ethernet generator stamps
// into every frame. It is required for raw datapaths (AF_XDP) where the generator
// must emit a complete, correctly-addressed Ethernet frame — the kernel stack is
// bypassed, so there is no one to build the headers.
type FrameOptions struct {
	SrcMAC, DstMAC   net.HardwareAddr
	SrcIP, DstIP     net.IP // IPv4
	SrcPort, DstPort uint16
}

// Ethernet emits complete Ethernet/IPv4/UDP frames for raw datapaths. The headers
// are built once into a template; Next copies it and stamps only the per-packet
// fields (lengths, IP id, checksums) plus the payload, so the hot path stays
// allocation-free.
type Ethernet struct {
	template [frameHeaderLen]byte
	pl       payload.Payloader
	size     int // total frame size (headers + payload)
	id       uint16
}

// NewEthernet builds an Ethernet/IPv4/UDP frame generator. size is the whole
// frame including headers; it is clamped to leave at least one payload byte.
func NewEthernet(fo *FrameOptions, pl payload.Payloader, size int) (*Ethernet, error) {
	if fo == nil {
		return nil, fmt.Errorf("ethernet generator: frame addressing required")
	}
	if len(fo.SrcMAC) != 6 || len(fo.DstMAC) != 6 {
		return nil, fmt.Errorf("ethernet generator: src/dst MAC must be 6 bytes")
	}
	src, dst := fo.SrcIP.To4(), fo.DstIP.To4()
	if src == nil || dst == nil {
		return nil, fmt.Errorf("ethernet generator: src/dst IP must be IPv4")
	}
	if size < frameHeaderLen+1 {
		size = frameHeaderLen + 1
	}
	e := &Ethernet{pl: pl, size: size}
	b := e.template[:]

	// Ethernet.
	copy(b[0:6], fo.DstMAC)
	copy(b[6:12], fo.SrcMAC)
	binary.BigEndian.PutUint16(b[12:14], 0x0800) // IPv4

	// IPv4 (header at offset 14). Length/id/checksum are stamped per packet.
	b[14] = 0x45 // version 4, IHL 5
	b[15] = 0    // DSCP/ECN
	b[20] = 0x40 // flags: DF
	b[21] = 0
	b[22] = 64 // TTL
	b[23] = 17 // protocol UDP
	copy(b[26:30], src)
	copy(b[30:34], dst)

	// UDP (header at offset 34). Length/checksum stamped per packet.
	binary.BigEndian.PutUint16(b[34:36], fo.SrcPort)
	binary.BigEndian.PutUint16(b[36:38], fo.DstPort)
	return e, nil
}

// Name implements Generator.
func (*Ethernet) Name() string { return "ethernet" }

// Next writes the next frame: the header template plus payload, with the
// per-packet length, IP identification, and IPv4 header checksum filled in. The
// UDP checksum is left 0 (optional for IPv4).
func (e *Ethernet) Next(buf []byte) (int, bool) {
	n := e.size
	if n > len(buf) {
		n = len(buf)
	}
	if n < frameHeaderLen+1 {
		return 0, true // no room for a valid frame
	}
	copy(buf[:frameHeaderLen], e.template[:])
	payloadLen := n - frameHeaderLen
	_, _ = e.pl.Read(buf[frameHeaderLen:n])

	// IPv4 total length, identification, and checksum.
	binary.BigEndian.PutUint16(buf[16:18], uint16(20+8+payloadLen))
	e.id++
	binary.BigEndian.PutUint16(buf[18:20], e.id)
	buf[24], buf[25] = 0, 0
	binary.BigEndian.PutUint16(buf[24:26], ipChecksum(buf[14:34]))

	// UDP length (checksum stays 0).
	binary.BigEndian.PutUint16(buf[38:40], uint16(8+payloadLen))
	buf[40], buf[41] = 0, 0
	return n, false
}

// ipChecksum is the standard ones-complement IPv4 header checksum.
func ipChecksum(hdr []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(hdr); i += 2 {
		sum += uint32(hdr[i])<<8 | uint32(hdr[i+1])
	}
	if len(hdr)%2 == 1 {
		sum += uint32(hdr[len(hdr)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
