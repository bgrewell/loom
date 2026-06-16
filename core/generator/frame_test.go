// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package generator

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/bgrewell/loom/core/payload"
)

func testFrameOpts() *FrameOptions {
	return &FrameOptions{
		SrcMAC:  net.HardwareAddr{0x02, 0, 0, 0, 0, 0x01},
		DstMAC:  net.HardwareAddr{0x02, 0, 0, 0, 0, 0x02},
		SrcIP:   net.IPv4(10, 0, 0, 1),
		DstIP:   net.IPv4(10, 0, 0, 2),
		SrcPort: 40000,
		DstPort: 9999,
	}
}

// TestEthernetFrameValid parses a crafted frame and checks every header field so
// a real NIC/peer would accept and deliver it.
func TestEthernetFrameValid(t *testing.T) {
	pl, _ := payload.Registry.Build("random", payload.Options{Size: 1400})
	const size = 1400
	g, err := NewEthernet(testFrameOpts(), pl, size)
	if err != nil {
		t.Fatalf("NewEthernet: %v", err)
	}

	buf := make([]byte, 2048)
	n, done := g.Next(buf)
	if done || n != size {
		t.Fatalf("Next = (%d, %v), want (%d, false)", n, done, size)
	}
	f := buf[:n]

	// Ethernet.
	if string(f[0:6]) != string([]byte{0x02, 0, 0, 0, 0, 0x02}) {
		t.Errorf("dst MAC = %x, want 02:00:00:00:00:02", f[0:6])
	}
	if string(f[6:12]) != string([]byte{0x02, 0, 0, 0, 0, 0x01}) {
		t.Errorf("src MAC = %x, want 02:00:00:00:00:01", f[6:12])
	}
	if binary.BigEndian.Uint16(f[12:14]) != 0x0800 {
		t.Errorf("ethertype = %#x, want 0x0800", binary.BigEndian.Uint16(f[12:14]))
	}

	// IPv4.
	if f[14] != 0x45 {
		t.Errorf("IP version/IHL = %#x, want 0x45", f[14])
	}
	if total := binary.BigEndian.Uint16(f[16:18]); int(total) != n-14 {
		t.Errorf("IP total length = %d, want %d", total, n-14)
	}
	if f[23] != 17 {
		t.Errorf("IP protocol = %d, want 17 (UDP)", f[23])
	}
	if !net.IP(f[26:30]).Equal(net.IPv4(10, 0, 0, 1)) || !net.IP(f[30:34]).Equal(net.IPv4(10, 0, 0, 2)) {
		t.Errorf("IP src/dst = %v/%v", net.IP(f[26:30]), net.IP(f[30:34]))
	}
	// IPv4 header checksum must verify (sum over the 20-byte header == 0xffff).
	if c := ipChecksum(f[14:34]); c != 0 {
		t.Errorf("IP checksum does not verify: residual %#x", c)
	}

	// UDP.
	if sp, dp := binary.BigEndian.Uint16(f[34:36]), binary.BigEndian.Uint16(f[36:38]); sp != 40000 || dp != 9999 {
		t.Errorf("UDP src/dst port = %d/%d, want 40000/9999", sp, dp)
	}
	if ul := binary.BigEndian.Uint16(f[38:40]); int(ul) != n-34 {
		t.Errorf("UDP length = %d, want %d", ul, n-34)
	}

	// IP identification increments per frame.
	id1 := binary.BigEndian.Uint16(f[18:20])
	g.Next(buf)
	if id2 := binary.BigEndian.Uint16(buf[18:20]); id2 != id1+1 {
		t.Errorf("IP id did not increment: %d then %d", id1, id2)
	}
}

func TestEthernetRejectsBadOpts(t *testing.T) {
	pl, _ := payload.Registry.Build("random", payload.Options{Size: 100})
	if _, err := NewEthernet(nil, pl, 100); err == nil {
		t.Error("nil FrameOptions should error")
	}
	bad := testFrameOpts()
	bad.SrcIP = net.ParseIP("::1") // not IPv4
	if _, err := NewEthernet(bad, pl, 100); err == nil {
		t.Error("non-IPv4 src should error")
	}
}
