// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build !loom_nonetstack

package netstack_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/netstack"
)

var (
	addrA  = netip.MustParseAddr("10.0.0.1")
	addrA2 = netip.MustParseAddr("10.0.0.3")
	addrB  = netip.MustParseAddr("10.0.0.2")
)

// l3mem adapts the in-process memory datapath into a raw-L3 backend for these
// tests (the same shape dgram's tests use): it advertises Capabilities.RawL3
// and adds a mutex, because the arena is single-producer/single-consumer
// while a netstack endpoint's writers and receive loop — and, in a paired
// two-stack fabric, two different stacks — share it. Unlike dgram's variant
// it also blocks RxPoll briefly on a commit notification, so latency numbers
// measure the stack rather than the receive loop's idle backoff.
type l3mem struct {
	mu     sync.Mutex
	m      *datapath.Memory
	closed bool
	notify chan struct{}
	done   chan struct{}
	onceCl sync.Once

	commitHook func(frames []datapath.Frame) // optional, called after TxCommit accepts
}

func newL3Mem() *l3mem {
	return &l3mem{
		m:      datapath.NewMemory(512, 2048),
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

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
	n, err := d.m.TxCommit(frames)
	d.mu.Unlock()
	if n > 0 {
		if d.commitHook != nil {
			d.commitHook(frames[:n])
		}
		select {
		case d.notify <- struct{}{}:
		default:
		}
	}
	return n, err
}

// pollTimeout is the timeout net.Error a blocking backend's RxPoll returns.
type pollTimeout struct{}

func (pollTimeout) Error() string   { return "rx poll timeout" }
func (pollTimeout) Timeout() bool   { return true }
func (pollTimeout) Temporary() bool { return true }

func (d *l3mem) RxPoll(max int) ([]datapath.Frame, error) {
	for {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return nil, net.ErrClosed
		}
		frames, err := d.m.RxPoll(max)
		d.mu.Unlock()
		if len(frames) > 0 || err != nil {
			return frames, err
		}
		select {
		case <-d.notify:
		case <-d.done:
			return nil, net.ErrClosed
		case <-time.After(5 * time.Millisecond):
			return nil, pollTimeout{}
		}
	}
}
func (d *l3mem) RxRelease(frames []datapath.Frame) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.m.RxRelease(frames)
}
func (d *l3mem) Close() error {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	d.onceCl.Do(func() { close(d.done) })
	return d.m.Close()
}

// newStackPair wires two Stacks back to back through two shared memory
// datapaths — what A transmits, B receives, and vice versa — with locals
// added on each: A gets aAddrs, B gets bAddrs.
func newStackPair(t *testing.T, cfg netstack.Config, aAddrs, bAddrs []netip.Addr) (sa, sb *netstack.Stack) {
	t.Helper()
	mAB, mBA := newL3Mem(), newL3Mem()
	sa, err := netstack.New(cfg, mAB, mBA)
	if err != nil {
		t.Fatalf("New(a): %v", err)
	}
	sb, err = netstack.New(cfg, mBA, mAB)
	if err != nil {
		t.Fatalf("New(b): %v", err)
	}
	t.Cleanup(func() { sa.Close(); sb.Close() })
	for _, a := range aAddrs {
		if err := sa.AddAddress(a); err != nil {
			t.Fatalf("a.AddAddress(%s): %v", a, err)
		}
	}
	for _, a := range bAddrs {
		if err := sb.AddAddress(a); err != nil {
			t.Fatalf("b.AddAddress(%s): %v", a, err)
		}
	}
	return sa, sb
}

// TestHTTPOverNetstack is the CI acceptance round trip: a stdlib http.Server
// on one Stack's view, a stdlib http.Client dialing through the other's —
// the whole real HTTP/TCP stack riding the paired memory datapath.
func TestHTTPOverNetstack(t *testing.T) {
	sa, sb := newStackPair(t, netstack.Config{}, []netip.Addr{addrA}, []netip.Addr{addrB})

	nb := sb.Network(addrB)
	ln, err := nb.Listen("tcp", "10.0.0.2:8080")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s to %s", r.Host, r.RemoteAddr)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	na := sa.Network(addrA)
	client := &http.Client{
		Transport: &http.Transport{DialContext: na.DialContext},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get("http://10.0.0.2:8080/hello")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	got := string(body)
	if !strings.HasPrefix(got, "hello from 10.0.0.2:8080 to 10.0.0.1:") {
		t.Errorf("body = %q, want prefix %q (server must see the view's bound source)", got, "hello from 10.0.0.2:8080 to 10.0.0.1:")
	}
}

// echoServer accepts on ln and echoes bytes back on every conn.
func echoServer(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() { defer c.Close(); io.Copy(c, c) }()
	}
}

// roundTrip writes msg and reads it back, failing on mismatch.
func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

// TestMultiAddressIsolation checks the one-Stack-many-addresses shape: two
// views bind their own sources, and closing one view kills only its conns.
func TestMultiAddressIsolation(t *testing.T) {
	sa, sb := newStackPair(t, netstack.Config{}, []netip.Addr{addrA, addrA2}, []netip.Addr{addrB})

	nb := sb.Network(addrB)
	ln, err := nb.Listen("tcp", ":7000")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go echoServer(ln)

	v1, v2 := sa.Network(addrA), sa.Network(addrA2)
	ctx := context.Background()
	c1, err := v1.DialContext(ctx, "tcp", "10.0.0.2:7000")
	if err != nil {
		t.Fatalf("dial from %s: %v", addrA, err)
	}
	c2, err := v2.DialContext(ctx, "tcp", "10.0.0.2:7000")
	if err != nil {
		t.Fatalf("dial from %s: %v", addrA2, err)
	}
	if got := c1.LocalAddr().String(); !strings.HasPrefix(got, "10.0.0.1:") {
		t.Errorf("view1 conn local addr = %s, want 10.0.0.1:*", got)
	}
	if got := c2.LocalAddr().String(); !strings.HasPrefix(got, "10.0.0.3:") {
		t.Errorf("view2 conn local addr = %s, want 10.0.0.3:*", got)
	}
	roundTrip(t, c1, "one")
	roundTrip(t, c2, "two")

	// Closing view1 closes its conn but leaves view2's conn — and the Stack —
	// live.
	if err := v1.Close(); err != nil {
		t.Fatalf("view1.Close: %v", err)
	}
	c1.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c1.Read(make([]byte, 1)); err == nil {
		t.Error("read on view1's conn after view Close succeeded, want error")
	}
	roundTrip(t, c2, "still alive")

	// New dials through the closed view are refused.
	if _, err := v1.DialContext(ctx, "tcp", "10.0.0.2:7000"); !errors.Is(err, net.ErrClosed) {
		t.Errorf("dial on closed view = %v, want net.ErrClosed", err)
	}
}

// TestUDPOverNetstack exercises ListenPacket and a udp DialContext through
// source-bound views.
func TestUDPOverNetstack(t *testing.T) {
	sa, sb := newStackPair(t, netstack.Config{}, []netip.Addr{addrA}, []netip.Addr{addrB})

	nb := sb.Network(addrB)
	pc, err := nb.ListenPacket("udp", ":9000")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()
	go func() { // UDP echo
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buf[:n], from)
		}
	}()

	na := sa.Network(addrA)
	c, err := na.DialContext(context.Background(), "udp", "10.0.0.2:9000")
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer c.Close()
	if got := c.LocalAddr().String(); !strings.HasPrefix(got, "10.0.0.1:") {
		t.Errorf("udp conn local addr = %s, want 10.0.0.1:*", got)
	}
	roundTrip(t, c, "udp ping")
}

// TestAddRemoveAddressUnderLiveTraffic adds and removes a second address
// while a first-address stream is running: the stream must not notice, added
// addresses must work immediately, and removed addresses must refuse new
// dials.
func TestAddRemoveAddressUnderLiveTraffic(t *testing.T) {
	sa, sb := newStackPair(t, netstack.Config{}, []netip.Addr{addrA}, []netip.Addr{addrB})

	nb := sb.Network(addrB)
	ln, err := nb.Listen("tcp", "10.0.0.2:7100")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go echoServer(ln)

	na := sa.Network(addrA)
	ctx := context.Background()
	steady, err := na.DialContext(ctx, "tcp", "10.0.0.2:7100")
	if err != nil {
		t.Fatalf("dial steady conn: %v", err)
	}
	defer steady.Close()
	roundTrip(t, steady, "before")

	// Attach: the new address dials as soon as it is added.
	if err := sa.AddAddress(addrA2); err != nil {
		t.Fatalf("AddAddress(%s): %v", addrA2, err)
	}
	v2 := sa.Network(addrA2)
	c2, err := v2.DialContext(ctx, "tcp", "10.0.0.2:7100")
	if err != nil {
		t.Fatalf("dial from added address: %v", err)
	}
	roundTrip(t, c2, "on the new address")
	roundTrip(t, steady, "mid")

	// Release: the removed address refuses new dials, conns still bound to it
	// are closed by the Stack (gVisor alone would leave them as zombies
	// retransmitting for minutes), and the steady stream and the stack
	// survive.
	c2.Close()
	c3, err := v2.DialContext(ctx, "tcp", "10.0.0.2:7100")
	if err != nil {
		t.Fatalf("dial pre-removal conn: %v", err)
	}
	if err := sa.RemoveAddress(addrA2); err != nil {
		t.Fatalf("RemoveAddress(%s): %v", addrA2, err)
	}
	c3.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c3.Read(make([]byte, 1)); err == nil {
		t.Error("read on a removed address's conn succeeded, want error (RemoveAddress must close it)")
	}
	if _, err := v2.DialContext(ctx, "tcp", "10.0.0.2:7100"); err == nil || !strings.Contains(err.Error(), "not on the stack") {
		t.Errorf("dial from removed address = %v, want 'not on the stack'", err)
	}
	for i := 0; i < 10; i++ {
		roundTrip(t, steady, fmt.Sprintf("after %d", i))
	}

	// Removing an address that is not there reports it.
	if err := sa.RemoveAddress(addrA2); err == nil || !strings.Contains(err.Error(), "not on the stack") {
		t.Errorf("second RemoveAddress = %v, want 'not on the stack'", err)
	}
}

// TestNewRejectsBadInputs pins the constructor's validation: RawL3 on both
// datapaths, sane MTU, known congestion control, IPv4-only addresses.
func TestNewRejectsBadInputs(t *testing.T) {
	raw := datapath.NewMemory(8, 512) // built-in memory: RawL3 is false
	if _, err := netstack.New(netstack.Config{}, raw, raw); err == nil || !strings.Contains(err.Error(), "RawL3") {
		t.Errorf("New with non-RawL3 tx = %v, want RawL3 error", err)
	}
	if _, err := netstack.New(netstack.Config{}, newL3Mem(), raw); err == nil || !strings.Contains(err.Error(), "RawL3") {
		t.Errorf("New with non-RawL3 rx = %v, want RawL3 error", err)
	}
	if _, err := netstack.New(netstack.Config{}, nil, nil); err == nil {
		t.Error("New with nil datapaths succeeded")
	}
	if _, err := netstack.New(netstack.Config{MTU: 100}, newL3Mem(), newL3Mem()); err == nil || !strings.Contains(err.Error(), "mtu") {
		t.Errorf("New with tiny MTU = %v, want mtu error", err)
	}
	if _, err := netstack.New(netstack.Config{CongestionControl: "bbr2"}, newL3Mem(), newL3Mem()); err == nil || !strings.Contains(err.Error(), "congestion") {
		t.Errorf("New with unknown CC = %v, want congestion control error", err)
	}

	s, err := netstack.New(netstack.Config{CongestionControl: "reno"}, newL3Mem(), newL3Mem())
	if err != nil {
		t.Fatalf("New with reno: %v", err)
	}
	defer s.Close()
	if err := s.AddAddress(netip.MustParseAddr("fd00::1")); err == nil || !strings.Contains(err.Error(), "IPv6") {
		t.Errorf("AddAddress(IPv6) = %v, want IPv6 rejection", err)
	}
	if err := s.AddAddress(addrA); err != nil {
		t.Fatalf("AddAddress: %v", err)
	}
	if err := s.AddAddress(addrA); err == nil || !strings.Contains(err.Error(), "already added") {
		t.Errorf("duplicate AddAddress = %v, want 'already added'", err)
	}

	v := s.Network(addrA)
	if _, err := v.DialContext(context.Background(), "tcp6", "[fd00::2]:80"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("DialContext(tcp6) = %v, want ErrUnsupportedNetwork", err)
	}
	if _, err := v.Listen("udp", ":1"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("Listen(udp) = %v, want ErrUnsupportedNetwork", err)
	}
	if _, err := v.ListenPacket("tcp", ":1"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("ListenPacket(tcp) = %v, want ErrUnsupportedNetwork", err)
	}
	if _, err := v.DialContext(context.Background(), "tcp", "example.com:80"); err == nil {
		t.Error("DialContext with a hostname succeeded, want numeric-literal error")
	}

	// A view of an address that was never added fails with a clear error.
	stray := s.Network(addrB)
	if _, err := stray.Listen("tcp", ":80"); err == nil || !strings.Contains(err.Error(), "not on the stack") {
		t.Errorf("Listen on un-added address = %v, want 'not on the stack'", err)
	}
}

// TestStackCloseTearsDown checks deterministic teardown: conns die, further
// use is refused, and Close is idempotent.
func TestStackCloseTearsDown(t *testing.T) {
	sa, sb := newStackPair(t, netstack.Config{}, []netip.Addr{addrA}, []netip.Addr{addrB})

	nb := sb.Network(addrB)
	ln, err := nb.Listen("tcp", ":7200")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go echoServer(ln)

	na := sa.Network(addrA)
	c, err := na.DialContext(context.Background(), "tcp", "10.0.0.2:7200")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	roundTrip(t, c, "up")

	if err := sa.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("read after Stack.Close succeeded, want error")
	}
	if _, err := na.DialContext(context.Background(), "tcp", "10.0.0.2:7200"); err == nil {
		t.Error("dial after Stack.Close succeeded, want error")
	}
	if err := sa.AddAddress(addrA2); !errors.Is(err, net.ErrClosed) {
		t.Errorf("AddAddress after Close = %v, want net.ErrClosed", err)
	}
	if err := sa.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

// TestStackCloseUnderTraffic tears a stack down while a bidirectional bulk
// transfer is in flight. Regression guard for the teardown deadlock: gVisor's
// Destroy holds stack.mu (write) while waiting out the link endpoint's
// receive goroutine, which can itself be blocked on stack.mu (read) in
// FindRoute serving an RST/ICMP reply — Stack.Close must stop the receive
// loop first. The test only has to complete.
func TestStackCloseUnderTraffic(t *testing.T) {
	sa, sb := newStackPair(t, netstack.Config{}, []netip.Addr{addrA}, []netip.Addr{addrB})

	nb := sb.Network(addrB)
	ln, err := nb.Listen("tcp", ":7300")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go echoServer(ln)

	na := sa.Network(addrA)
	c, err := na.DialContext(context.Background(), "tcp", "10.0.0.2:7300")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	go func() { // keep segments in flight both ways until teardown kills the conn
		buf := make([]byte, 16<<10)
		for {
			if _, err := c.Write(buf); err != nil {
				return
			}
		}
	}()
	go io.Copy(io.Discard, c)
	time.Sleep(20 * time.Millisecond)

	closed := make(chan struct{})
	go func() {
		sa.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("Stack.Close deadlocked under live traffic")
	}
}

// TestRegistryFactory builds "netstack" through netpath.Registry: built-in
// datapaths fail the RawL3 gate (there is no raw-L3 built-in yet), and the
// options are validated first.
func TestRegistryFactory(t *testing.T) {
	names := netpath.Registry.Names()
	found := false
	for _, n := range names {
		if n == "netstack" {
			found = true
		}
	}
	if !found {
		t.Fatalf("netpath.Registry.Names() = %v, want to include netstack", names)
	}
	if _, err := netpath.Registry.Build("netstack", netpath.Options{Local: addrA, TxDatapath: "memory", RxDatapath: "udp"}); err == nil || !strings.Contains(err.Error(), "RawL3") {
		t.Errorf("Registry.Build(netstack) over built-ins = %v, want RawL3 error", err)
	}
	if _, err := netstack.FromOptions(nil, netpath.Options{Local: addrA}); err == nil || !strings.Contains(err.Error(), "TxDatapath") {
		t.Errorf("FromOptions without datapath names = %v, want naming error", err)
	}
	if _, err := netstack.FromOptions(nil, netpath.Options{TxDatapath: "memory", RxDatapath: "memory"}); err == nil || !strings.Contains(err.Error(), "Local") {
		t.Errorf("FromOptions without Local = %v, want Local error", err)
	}
}
