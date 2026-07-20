// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package dgram_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/components"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/netpath/dgram"
	"github.com/bgrewell/loom/core/registry"
)

var (
	addrA = netip.MustParseAddr("10.0.0.1")
	addrB = netip.MustParseAddr("10.0.0.2")
)

// l3mem adapts the in-process memory datapath into a raw-L3 backend for these
// tests: it advertises Capabilities.RawL3 (the arena's frames carry the
// complete IP packets dgram writes) and adds a mutex, because the arena is
// single-producer/single-consumer while a dgram network's writers and receive
// loop — and, in a two-network pair, two different networks — share it.
type l3mem struct {
	mu sync.Mutex
	m  *datapath.Memory
}

func newL3Mem() *l3mem { return &l3mem{m: datapath.NewMemory(256, 2048)} }

func (d *l3mem) Name() string { return "l3mem" }
func (d *l3mem) Caps() datapath.Capabilities {
	return datapath.Capabilities{RawL3: true}
}
func (d *l3mem) TxReserve(n int) []datapath.Frame {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m.TxReserve(n)
}
func (d *l3mem) TxCommit(frames []datapath.Frame) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m.TxCommit(frames)
}
func (d *l3mem) RxPoll(max int) ([]datapath.Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m.RxPoll(max)
}
func (d *l3mem) RxRelease(frames []datapath.Frame) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.m.RxRelease(frames)
}
func (d *l3mem) Close() error { return d.m.Close() }

// newPair wires two dgram networks back to back through two shared memory
// datapaths: what A transmits, B receives, and vice versa.
func newPair(t *testing.T) (na, nb netpath.Network) {
	t.Helper()
	mAB, mBA := newL3Mem(), newL3Mem()
	na, err := dgram.New(mAB, mBA, addrA, 1500)
	if err != nil {
		t.Fatalf("New(a): %v", err)
	}
	nb, err = dgram.New(mBA, mAB, addrB, 1500)
	if err != nil {
		t.Fatalf("New(b): %v", err)
	}
	t.Cleanup(func() { na.Close(); nb.Close() })
	return na, nb
}

// csum is the tests' independent internet checksum (RFC 1071): big-endian
// 16-bit one's-complement sum, complemented. Only the final chunk may have odd
// length.
func csum(chunks ...[]byte) uint16 {
	var sum uint32
	for _, b := range chunks {
		for ; len(b) >= 2; b = b[2:] {
			sum += uint32(b[0])<<8 | uint32(b[1])
		}
		if len(b) == 1 {
			sum += uint32(b[0]) << 8
		}
	}
	for sum>>16 != 0 {
		sum = sum&0xFFFF + sum>>16
	}
	return ^uint16(sum)
}

// buildPacket is the tests' independent IPv4+UDP encoder, used to inject
// crafted frames.
func buildPacket(src, dst netip.AddrPort, payload []byte) []byte {
	b := make([]byte, 28+len(payload))
	b[0] = 0x45
	binary.BigEndian.PutUint16(b[2:], uint16(len(b)))
	binary.BigEndian.PutUint16(b[4:], 0x1234)
	binary.BigEndian.PutUint16(b[6:], 0x4000) // DF
	b[8] = 64
	b[9] = 17
	sa, da := src.Addr().As4(), dst.Addr().As4()
	copy(b[12:16], sa[:])
	copy(b[16:20], da[:])
	binary.BigEndian.PutUint16(b[10:], csum(b[:20]))
	u := b[20:]
	binary.BigEndian.PutUint16(u[0:], src.Port())
	binary.BigEndian.PutUint16(u[2:], dst.Port())
	binary.BigEndian.PutUint16(u[4:], uint16(8+len(payload)))
	copy(u[8:], payload)
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], sa[:])
	copy(pseudo[4:8], da[:])
	pseudo[9] = 17
	binary.BigEndian.PutUint16(pseudo[10:], uint16(len(u)))
	ck := csum(pseudo, u)
	if ck == 0 {
		ck = 0xFFFF
	}
	binary.BigEndian.PutUint16(u[6:], ck)
	return b
}

// inject commits a hand-built packet into a datapath as if a peer sent it.
func inject(t *testing.T, dp *l3mem, pkt []byte) {
	t.Helper()
	frames := dp.TxReserve(1)
	if len(frames) == 0 {
		t.Fatal("inject: no tx frames")
	}
	frames[0].Len = copy(frames[0].Data, pkt)
	if sent, err := dp.TxCommit(frames[:1]); err != nil || sent != 1 {
		t.Fatalf("inject: TxCommit = %d, %v", sent, err)
	}
}

// waitFor polls cond until it holds or the test deadline budget elapses.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestNewValidation verifies constructor rejections: datapaths without
// Capabilities.RawL3, non-IPv4 local addresses, and unusable MTUs.
func TestNewValidation(t *testing.T) {
	raw := datapath.NewMemory(8, 512) // built-in memory: RawL3 is false
	if _, err := dgram.New(raw, raw, addrA, 1500); err == nil || !strings.Contains(err.Error(), "RawL3") {
		t.Errorf("New with non-RawL3 tx = %v, want RawL3 error", err)
	}
	if _, err := dgram.New(newL3Mem(), raw, addrA, 1500); err == nil || !strings.Contains(err.Error(), "RawL3") {
		t.Errorf("New with non-RawL3 rx = %v, want RawL3 error", err)
	}
	if _, err := dgram.New(newL3Mem(), newL3Mem(), netip.MustParseAddr("::1"), 1500); err == nil || !strings.Contains(err.Error(), "IPv4") {
		t.Errorf("New with IPv6 local = %v, want IPv4-only error", err)
	}
	if _, err := dgram.New(newL3Mem(), newL3Mem(), netip.Addr{}, 1500); err == nil {
		t.Error("New with zero local address succeeded")
	}
	if _, err := dgram.New(newL3Mem(), newL3Mem(), addrA, 27); err == nil {
		t.Error("New with mtu below the headers succeeded")
	}
	// The IPv4 total-length (and UDP length) fields are 16-bit: an MTU above
	// 65535 could only emit packets whose length fields wrap, so it is refused.
	if _, err := dgram.New(newL3Mem(), newL3Mem(), addrA, 65536); err == nil || !strings.Contains(err.Error(), "65535") {
		t.Errorf("New with mtu 65536 = %v, want IPv4 maximum error", err)
	}
	if _, err := dgram.FromOptions(nil, netpath.Options{Local: addrA, MTU: 70000, TxDatapath: "memory", RxDatapath: "udp"}); err == nil {
		t.Error("FromOptions with MTU 70000 succeeded")
	}
	if _, err := dgram.New(nil, nil, addrA, 1500); err == nil {
		t.Error("New with nil datapaths succeeded")
	}
	// A 4-in-6-mapped local address is IPv4 and must be accepted.
	n, err := dgram.New(newL3Mem(), newL3Mem(), netip.MustParseAddr("::ffff:10.0.0.1"), 1500)
	if err != nil {
		t.Fatalf("New with 4-in-6 local: %v", err)
	}
	n.Close()
}

// TestFrameEncoding hand-decodes the frame WriteTo commits: header fields,
// lengths, and both checksums recomputed independently must match.
func TestFrameEncoding(t *testing.T) {
	out := newL3Mem()
	n, err := dgram.New(out, newL3Mem(), addrA, 1500)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	pc, err := n.ListenPacket("udp", ":40000")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()

	payload := []byte("hello, loom")
	dst := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2).To4(), Port: 5060}
	if wn, err := pc.WriteTo(payload, dst); err != nil || wn != len(payload) {
		t.Fatalf("WriteTo = %d, %v", wn, err)
	}

	frames, err := out.RxPoll(1)
	if err != nil || len(frames) != 1 {
		t.Fatalf("RxPoll = %d frames, %v", len(frames), err)
	}
	pkt := append([]byte(nil), frames[0].Data[:frames[0].Len]...)
	out.RxRelease(frames)

	want := 20 + 8 + len(payload)
	if len(pkt) != want {
		t.Fatalf("frame length = %d, want %d", len(pkt), want)
	}
	if pkt[0] != 0x45 {
		t.Errorf("version/IHL = %#02x, want 0x45", pkt[0])
	}
	if got := binary.BigEndian.Uint16(pkt[2:]); got != uint16(want) {
		t.Errorf("IPv4 total length = %d, want %d", got, want)
	}
	if got := binary.BigEndian.Uint16(pkt[6:]); got != 0x4000 {
		t.Errorf("flags/fragment = %#04x, want 0x4000 (DF, no fragmentation)", got)
	}
	if pkt[8] == 0 {
		t.Error("TTL is 0")
	}
	if pkt[9] != 17 {
		t.Errorf("protocol = %d, want 17 (UDP)", pkt[9])
	}
	ipck := binary.BigEndian.Uint16(pkt[10:])
	hdr := append([]byte(nil), pkt[:20]...)
	hdr[10], hdr[11] = 0, 0
	if recomputed := csum(hdr); recomputed != ipck {
		t.Errorf("IPv4 checksum = %#04x, recomputed %#04x", ipck, recomputed)
	}
	if !bytes.Equal(pkt[12:16], []byte{10, 0, 0, 1}) || !bytes.Equal(pkt[16:20], []byte{10, 0, 0, 2}) {
		t.Errorf("addresses = %v → %v, want 10.0.0.1 → 10.0.0.2", pkt[12:16], pkt[16:20])
	}
	u := pkt[20:]
	if got := binary.BigEndian.Uint16(u[0:]); got != 40000 {
		t.Errorf("source port = %d, want 40000", got)
	}
	if got := binary.BigEndian.Uint16(u[2:]); got != 5060 {
		t.Errorf("destination port = %d, want 5060", got)
	}
	if got := binary.BigEndian.Uint16(u[4:]); got != uint16(8+len(payload)) {
		t.Errorf("UDP length = %d, want %d", got, 8+len(payload))
	}
	udpck := binary.BigEndian.Uint16(u[6:])
	if udpck == 0 {
		t.Error("UDP checksum is 0; it must always be computed")
	}
	seg := append([]byte(nil), u...)
	seg[6], seg[7] = 0, 0
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], pkt[12:16])
	copy(pseudo[4:8], pkt[16:20])
	pseudo[9] = 17
	binary.BigEndian.PutUint16(pseudo[10:], uint16(len(seg)))
	if recomputed := csum(pseudo, seg); recomputed != udpck {
		t.Errorf("UDP checksum = %#04x, recomputed %#04x", udpck, recomputed)
	}
	if !bytes.Equal(u[8:], payload) {
		t.Errorf("payload = %q, want %q", u[8:], payload)
	}

	// Oversized writes are refused, never fragmented.
	big := make([]byte, 1500-28+1)
	if _, err := pc.WriteTo(big, dst); err == nil {
		t.Error("oversized WriteTo succeeded, want message-too-long error")
	}
}

// TestRoundTrip sends a datagram between two networks and back, checking
// payloads, source addresses, and the preserved arrival timestamp.
func TestRoundTrip(t *testing.T) {
	na, nb := newPair(t)
	srv, err := nb.ListenPacket("udp", ":9000")
	if err != nil {
		t.Fatalf("ListenPacket(b): %v", err)
	}
	defer srv.Close()
	cli, err := na.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket(a): %v", err)
	}
	defer cli.Close()
	deadline := time.Now().Add(5 * time.Second)
	_ = srv.SetDeadline(deadline)
	_ = cli.SetDeadline(deadline)

	msg := []byte("ping")
	if _, err := cli.WriteTo(msg, srv.LocalAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	mc, ok := srv.(dgram.MetaConn)
	if !ok {
		t.Fatal("packet conn does not implement dgram.MetaConn")
	}
	buf := make([]byte, 64)
	n, from, arrival, err := mc.ReadFromMeta(buf)
	if err != nil {
		t.Fatalf("ReadFromMeta: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Errorf("payload = %q, want %q", buf[:n], msg)
	}
	if from.String() != cli.LocalAddr().String() {
		t.Errorf("source = %v, want %v", from, cli.LocalAddr())
	}
	if arrival.IsZero() || time.Since(arrival) > 5*time.Second {
		t.Errorf("arrival = %v, want a recent datapath timestamp", arrival)
	}

	if _, err := srv.WriteTo(buf[:n], from); err != nil {
		t.Fatalf("reply WriteTo: %v", err)
	}
	n, replyFrom, err := cli.ReadFrom(buf)
	if err != nil {
		t.Fatalf("reply ReadFrom: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Errorf("reply payload = %q, want %q", buf[:n], msg)
	}
	if replyFrom.String() != srv.LocalAddr().String() {
		t.Errorf("reply source = %v, want %v", replyFrom, srv.LocalAddr())
	}
}

// TestConnectedDial verifies DialContext("udp") gives connected-socket
// semantics: Write reaches the target, Read filters to the remote's
// datagrams, dropping an interloper's.
func TestConnectedDial(t *testing.T) {
	na, nb := newPair(t)
	srv, err := nb.ListenPacket("udp", ":9100")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer srv.Close()
	_ = srv.SetDeadline(time.Now().Add(5 * time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := na.DialContext(ctx, "udp", "10.0.0.2:9100")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	msg := []byte("ping")
	if _, err := c.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 64)
	n, from, err := srv.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if from.String() != c.LocalAddr().String() {
		t.Errorf("source = %v, want dialer's %v", from, c.LocalAddr())
	}

	// An interloper writes to the dialer's port first; the connected Read
	// must skip it and return the remote's reply.
	stray, err := nb.ListenPacket("udp", ":9101")
	if err != nil {
		t.Fatalf("ListenPacket(stray): %v", err)
	}
	defer stray.Close()
	if _, err := stray.WriteTo([]byte("stray"), c.LocalAddr()); err != nil {
		t.Fatalf("stray WriteTo: %v", err)
	}
	if _, err := srv.WriteTo([]byte("pong"), from); err != nil {
		t.Fatalf("reply WriteTo: %v", err)
	}
	n, err = c.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "pong" {
		t.Errorf("connected Read = %q, want %q (stray datagram not filtered)", buf[:n], "pong")
	}
	if c.RemoteAddr().String() != "10.0.0.2:9100" {
		t.Errorf("RemoteAddr = %v, want 10.0.0.2:9100", c.RemoteAddr())
	}

	// Dial targets must be IPv4 ip:port literals.
	if _, err := na.DialContext(ctx, "udp", "example.com:53"); err == nil {
		t.Error("DialContext with a hostname succeeded")
	}
	if _, err := na.DialContext(ctx, "udp", "[::1]:53"); err == nil {
		t.Error("DialContext to IPv6 succeeded")
	}
}

// TestPortDemux verifies multiple conns on one network each receive only the
// datagrams addressed to their port.
func TestPortDemux(t *testing.T) {
	na, nb := newPair(t)
	b1, err := nb.ListenPacket("udp", ":7001")
	if err != nil {
		t.Fatalf("ListenPacket(7001): %v", err)
	}
	defer b1.Close()
	b2, err := nb.ListenPacket("udp", ":7002")
	if err != nil {
		t.Fatalf("ListenPacket(7002): %v", err)
	}
	defer b2.Close()
	cli, err := na.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket(a): %v", err)
	}
	defer cli.Close()

	if _, err := cli.WriteTo([]byte("one"), b1.LocalAddr()); err != nil {
		t.Fatalf("WriteTo(7001): %v", err)
	}
	if _, err := cli.WriteTo([]byte("two"), b2.LocalAddr()); err != nil {
		t.Fatalf("WriteTo(7002): %v", err)
	}
	buf := make([]byte, 64)
	_ = b1.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = b2.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, _, err := b1.ReadFrom(buf); err != nil || string(buf[:n]) != "one" {
		t.Errorf("b1 ReadFrom = %q, %v; want %q", buf[:n], err, "one")
	}
	if n, _, err := b2.ReadFrom(buf); err != nil || string(buf[:n]) != "two" {
		t.Errorf("b2 ReadFrom = %q, %v; want %q", buf[:n], err, "two")
	}
	// No cross-delivery: b1 must now be empty.
	_ = b1.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := b1.ReadFrom(buf); err == nil {
		t.Error("b1 received a second datagram, want timeout")
	}
}

// TestEphemeralAllocation verifies port-0 binds allocate distinct dynamic
// ports and explicit rebinds of a taken port fail.
func TestEphemeralAllocation(t *testing.T) {
	na, _ := newPair(t)
	p1, err := na.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer p1.Close()
	p2, err := na.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer p2.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := na.DialContext(ctx, "udp", "10.0.0.2:9000")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer c.Close()

	ports := make(map[int]bool)
	for _, a := range []net.Addr{p1.LocalAddr(), p2.LocalAddr(), c.LocalAddr()} {
		ua, ok := a.(*net.UDPAddr)
		if !ok {
			t.Fatalf("LocalAddr type = %T, want *net.UDPAddr", a)
		}
		if ua.Port < 49152 {
			t.Errorf("ephemeral port %d below the dynamic range", ua.Port)
		}
		if ports[ua.Port] {
			t.Errorf("ephemeral port %d allocated twice", ua.Port)
		}
		ports[ua.Port] = true
	}

	// Explicit duplicate binds fail; a closed port can be rebound.
	pe, err := na.ListenPacket("udp", ":7100")
	if err != nil {
		t.Fatalf("ListenPacket(7100): %v", err)
	}
	if _, err := na.ListenPacket("udp", ":7100"); err == nil {
		t.Error("duplicate bind of :7100 succeeded")
	}
	pe.Close()
	pe2, err := na.ListenPacket("udp", ":7100")
	if err != nil {
		t.Errorf("rebind of closed :7100: %v", err)
	} else {
		pe2.Close()
	}
}

// TestDropCounting injects malformed and misaddressed packets and verifies
// they are dropped silently but counted, while a valid packet still delivers.
func TestDropCounting(t *testing.T) {
	inj := newL3Mem()
	n, err := dgram.New(newL3Mem(), inj, addrA, 1500)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	pc, err := n.ListenPacket("udp", ":6000")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()

	src := netip.AddrPortFrom(addrB, 555)
	bound := netip.AddrPortFrom(addrA, 6000)

	// Valid control packet: must be delivered.
	inject(t, inj, buildPacket(src, bound, []byte("ok")))
	// Corrupted UDP checksum.
	bad := buildPacket(src, bound, []byte("bad-udp"))
	bad[len(bad)-1] ^= 0xFF
	inject(t, inj, bad)
	// Not UDP (protocol 6), IPv4 checksum refreshed so only the protocol fails.
	tcp := buildPacket(src, bound, []byte("tcp?"))
	tcp[9] = 6
	tcp[10], tcp[11] = 0, 0
	binary.BigEndian.PutUint16(tcp[10:], csum(tcp[:20]))
	inject(t, inj, tcp)
	// Corrupted IPv4 header checksum.
	badIP := buildPacket(src, bound, []byte("bad-ip"))
	badIP[10] ^= 0xFF
	inject(t, inj, badIP)
	// Valid but for an unbound port, and for another host's address.
	inject(t, inj, buildPacket(src, netip.AddrPortFrom(addrA, 6001), []byte("noport")))
	inject(t, inj, buildPacket(src, netip.AddrPortFrom(netip.MustParseAddr("10.0.0.9"), 6000), []byte("nothere")))

	dr, ok := n.(dgram.DropReporter)
	if !ok {
		t.Fatal("network does not implement dgram.DropReporter")
	}
	want := dgram.DropStats{BadIPHeader: 1, NotUDP: 1, BadUDPChecksum: 1, NoEndpoint: 2}
	waitFor(t, func() bool { return dr.Drops() == want },
		"drop counters never reached expectation")

	// Only the control packet was delivered.
	buf := make([]byte, 64)
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	nr, _, err := pc.ReadFrom(buf)
	if err != nil || string(buf[:nr]) != "ok" {
		t.Fatalf("ReadFrom = %q, %v; want %q", buf[:nr], err, "ok")
	}
	_ = pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := pc.ReadFrom(buf); err == nil {
		t.Error("a dropped packet was delivered")
	}
}

// TestUnsupportedNetworks verifies TCP (and everything non-UDP/IPv4) is
// refused with the netpath sentinel.
func TestUnsupportedNetworks(t *testing.T) {
	na, _ := newPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := na.DialContext(ctx, "tcp", "10.0.0.2:80"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("DialContext(tcp) = %v, want ErrUnsupportedNetwork", err)
	}
	if _, err := na.Listen("tcp", ":0"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("Listen(tcp) = %v, want ErrUnsupportedNetwork", err)
	}
	if _, err := na.Listen("udp", ":0"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("Listen(udp) = %v, want ErrUnsupportedNetwork", err)
	}
	if _, err := na.ListenPacket("tcp", ":0"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("ListenPacket(tcp) = %v, want ErrUnsupportedNetwork", err)
	}
	if _, err := na.ListenPacket("udp6", ":0"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("ListenPacket(udp6) = %v, want ErrUnsupportedNetwork (IPv4-only)", err)
	}
	if _, err := na.DialContext(ctx, "udp6", "[::1]:53"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("DialContext(udp6) = %v, want ErrUnsupportedNetwork (IPv4-only)", err)
	}

	// Stream networks additionally match the ErrTCPUnsupported sentinel
	// (design §2.1), so callers can route "UDP-only backend" separately from
	// an unknown network name.
	if _, err := na.DialContext(ctx, "tcp", "10.0.0.2:80"); !errors.Is(err, dgram.ErrTCPUnsupported) {
		t.Errorf("DialContext(tcp) = %v, want ErrTCPUnsupported", err)
	}
	if _, err := na.Listen("tcp", ":0"); !errors.Is(err, dgram.ErrTCPUnsupported) {
		t.Errorf("Listen(tcp) = %v, want ErrTCPUnsupported", err)
	}
	if _, err := na.ListenPacket("tcp4", ":0"); !errors.Is(err, dgram.ErrTCPUnsupported) {
		t.Errorf("ListenPacket(tcp4) = %v, want ErrTCPUnsupported", err)
	}
	// Non-stream refusals must NOT carry the TCP sentinel.
	if _, err := na.Listen("udp", ":0"); errors.Is(err, dgram.ErrTCPUnsupported) {
		t.Errorf("Listen(udp) = %v, must not match ErrTCPUnsupported", err)
	}
	if _, err := na.DialContext(ctx, "unix", "/nonexistent"); errors.Is(err, dgram.ErrTCPUnsupported) {
		t.Errorf("DialContext(unix) = %v, must not match ErrTCPUnsupported", err)
	}
}

// TestReadDeadlineWakesBlockedRead pins the net.Conn deadline contract on
// dgram packet conns: SetReadDeadline must wake a currently-blocked ReadFrom
// (the standard interrupt idiom used for reader-goroutine teardown).
func TestReadDeadlineWakesBlockedRead(t *testing.T) {
	na, _ := newPair(t)
	pc, err := na.ListenPacket("udp", ":7000")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()
	got := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, _, err := pc.ReadFrom(buf)
		got <- err
	}()
	time.Sleep(10 * time.Millisecond) // let ReadFrom block
	_ = pc.SetReadDeadline(time.Now())
	select {
	case err := <-got:
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Errorf("blocked ReadFrom woke with %v, want timeout error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked ReadFrom not woken within 2s of SetReadDeadline(now)")
	}

	// Extending the deadline mid-read re-arms rather than firing early.
	_ = pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	go func() {
		buf := make([]byte, 16)
		_, _, err := pc.ReadFrom(buf)
		got <- err
	}()
	time.Sleep(10 * time.Millisecond)
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	select {
	case err := <-got:
		t.Errorf("read returned %v before the extended deadline", err)
	case <-time.After(200 * time.Millisecond):
		// Still blocked past the original 50ms deadline: the extension took
		// effect. Unblock it.
		_ = pc.SetReadDeadline(time.Now())
		<-got
	}
}

// TestSendBoundedByFrameLen verifies a datagram larger than the datapath's
// frame size errors instead of writing past the frame: slab-backed backends
// hand out frames whose cap runs into the neighboring frame's memory, so the
// bound must be the frame's len, not its cap.
func TestSendBoundedByFrameLen(t *testing.T) {
	small := &l3mem{m: datapath.NewMemory(8, 512)} // frame size 512 < mtu 1500
	n, err := dgram.New(small, newL3Mem(), addrA, 1500)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	pc, err := n.ListenPacket("udp", ":7100")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()
	dst := &net.UDPAddr{IP: addrB.AsSlice(), Port: 7200}
	if _, err := pc.WriteTo(make([]byte, 600), dst); err == nil || !strings.Contains(err.Error(), "frame size") {
		t.Errorf("WriteTo(600B over 512B frames) = %v, want frame-size error", err)
	}
	// A datagram that fits the frame still goes through.
	if _, err := pc.WriteTo(make([]byte, 400), dst); err != nil {
		t.Errorf("WriteTo(400B) = %v, want success", err)
	}
}

// TestBindValidation verifies the bind host part must be this network's local
// address (or unspecified/empty).
func TestBindValidation(t *testing.T) {
	na, _ := newPair(t)
	if _, err := na.ListenPacket("udp", "10.0.0.9:7500"); err == nil {
		t.Error("bind to a foreign address succeeded")
	}
	pc, err := na.ListenPacket("udp", "10.0.0.1:7500")
	if err != nil {
		t.Fatalf("bind to the local address: %v", err)
	}
	if pc.LocalAddr().String() != "10.0.0.1:7500" {
		t.Errorf("LocalAddr = %v, want 10.0.0.1:7500", pc.LocalAddr())
	}
	pc.Close()
	pc, err = na.ListenPacket("udp", "0.0.0.0:7501")
	if err != nil {
		t.Fatalf("bind to the unspecified address: %v", err)
	}
	pc.Close()
}

// TestNetworkClose verifies Close unblocks pending reads and fails later
// binds and writes with net.ErrClosed.
func TestNetworkClose(t *testing.T) {
	na, _ := newPair(t)
	pc, err := na.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	got := make(chan error, 1)
	go func() {
		_, _, err := pc.ReadFrom(make([]byte, 16))
		got <- err
	}()
	time.Sleep(10 * time.Millisecond) // let ReadFrom block
	if err := na.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-got:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("ReadFrom after Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadFrom did not unblock within 2s of network Close")
	}
	if _, err := pc.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("WriteTo after Close = %v, want net.ErrClosed", err)
	}
	if _, err := na.ListenPacket("udp", ":0"); !errors.Is(err, net.ErrClosed) {
		t.Errorf("ListenPacket after Close = %v, want net.ErrClosed", err)
	}
	if err := na.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestFromOptions verifies the registry factory resolves datapath names from
// an injected component set and enforces RawL3 on what it builds.
func TestFromOptions(t *testing.T) {
	// A self-connected raw-L3 loopback: tx and rx resolve to one shared arena,
	// so the network receives its own transmissions.
	shared := newL3Mem()
	txReg := registry.New[datapath.TxDatapath, datapath.Options]()
	rxReg := registry.New[datapath.RxDatapath, datapath.Options]()
	txReg.Register("l3mem", func(datapath.Options) (datapath.TxDatapath, error) { return shared, nil })
	rxReg.Register("l3mem", func(datapath.Options) (datapath.RxDatapath, error) { return shared, nil })
	c := &components.Components{TxDatapaths: txReg, RxDatapaths: rxReg}

	n, err := dgram.FromOptions(c, netpath.Options{Local: addrA, TxDatapath: "l3mem", RxDatapath: "l3mem"})
	if err != nil {
		t.Fatalf("FromOptions: %v", err)
	}
	defer n.Close()
	if n.Name() != "dgram" {
		t.Errorf("Name = %q, want dgram", n.Name())
	}
	a, err := n.ListenPacket("udp", ":8001")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer a.Close()
	b, err := n.ListenPacket("udp", ":8002")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer b.Close()
	_ = b.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := a.WriteTo([]byte("loop"), b.LocalAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	buf := make([]byte, 16)
	nr, from, err := b.ReadFrom(buf)
	if err != nil || string(buf[:nr]) != "loop" {
		t.Fatalf("ReadFrom = %q, %v; want %q", buf[:nr], err, "loop")
	}
	if from.String() != a.LocalAddr().String() {
		t.Errorf("source = %v, want %v", from, a.LocalAddr())
	}

	// Missing datapath names are rejected.
	if _, err := dgram.FromOptions(c, netpath.Options{Local: addrA}); err == nil {
		t.Error("FromOptions without datapath names succeeded")
	}
	// Unknown names surface the registry error.
	if _, err := dgram.FromOptions(c, netpath.Options{Local: addrA, TxDatapath: "nope", RxDatapath: "l3mem"}); err == nil {
		t.Error("FromOptions with an unknown tx datapath succeeded")
	}
	// Default components' built-in datapaths carry opaque payloads (no RawL3),
	// so building over them is refused.
	if _, err := dgram.FromOptions(nil, netpath.Options{Local: addrA, TxDatapath: "memory", RxDatapath: "udp"}); err == nil || !strings.Contains(err.Error(), "RawL3") {
		t.Errorf("FromOptions over built-in datapaths = %v, want RawL3 error", err)
	}
}

// TestRegistered verifies importing this package registers "dgram" in
// netpath.Registry, wired to the default component set.
func TestRegistered(t *testing.T) {
	found := false
	for _, name := range netpath.Registry.Names() {
		if name == "dgram" {
			found = true
		}
	}
	if !found {
		t.Fatalf("netpath.Registry lacks dgram; have %v", netpath.Registry.Names())
	}
	// The factory reaches FromOptions: built-in datapaths fail the RawL3 gate.
	if _, err := netpath.Registry.Build("dgram", netpath.Options{Local: addrA, TxDatapath: "memory", RxDatapath: "udp"}); err == nil || !strings.Contains(err.Error(), "RawL3") {
		t.Errorf("Registry.Build(dgram) over built-ins = %v, want RawL3 error", err)
	}
}
