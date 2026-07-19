// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package contract holds shared conformance checks that every implementation of
// a core interface must pass. Registry plugins call these from their tests so a
// new backend or protocol can't silently drift from the interface contract. See
// docs/testing.md (Tier 2).
package contract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/payload"
	"github.com/bgrewell/loom/core/scheduler"
)

// Scheduler asserts a scheduler honors the Scheduler contract.
func Scheduler(t testing.TB, s scheduler.Scheduler) {
	t.Helper()
	if s.Name() == "" {
		t.Errorf("Scheduler.Name() is empty")
	}
	live, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if n, ok := s.Pace(live, 4); !ok || n < 1 || n > 4 {
		t.Errorf("%s: Pace(live,4) = (%d,%v), want n in [1,4], ok", s.Name(), n, ok)
	}
	dead, c2 := context.WithCancel(context.Background())
	c2()
	if _, ok := s.Pace(dead, 4); ok {
		t.Errorf("%s: Pace returned ok with a cancelled context", s.Name())
	}
}

// TxDatapath asserts a transmit datapath honors the contract. The datapath must
// be usable without external setup (e.g. memory/discard).
func TxDatapath(t testing.TB, d datapath.TxDatapath) {
	t.Helper()
	if d.Name() == "" {
		t.Errorf("TxDatapath.Name() is empty")
	}
	d.Caps() // must not panic
	frames := d.TxReserve(1)
	if len(frames) == 0 {
		t.Fatalf("%s: TxReserve(1) returned no frames", d.Name())
	}
	msg := []byte("contract")
	if cap(frames[0].Data) < len(msg) {
		t.Fatalf("%s: reserved frame too small (%d)", d.Name(), cap(frames[0].Data))
	}
	n := copy(frames[0].Data, msg)
	frames[0].Len = n
	sent, err := d.TxCommit(frames[:1])
	if err != nil {
		t.Errorf("%s: TxCommit error: %v", d.Name(), err)
	}
	if sent != 1 {
		t.Errorf("%s: TxCommit sent %d, want 1", d.Name(), sent)
	}
	if err := d.Close(); err != nil {
		t.Errorf("%s: Close error: %v", d.Name(), err)
	}
}

// RxDatapath asserts a receive datapath honors the contract: polling and
// releasing must not panic, and an empty poll must not block indefinitely (the
// backend bounds it with a deadline).
func RxDatapath(t testing.TB, d datapath.RxDatapath) {
	t.Helper()
	if d.Name() == "" {
		t.Errorf("RxDatapath.Name() is empty")
	}
	d.Caps() // must not panic
	frames, _ := d.RxPoll(4)
	d.RxRelease(frames)
	if err := d.Close(); err != nil {
		t.Errorf("%s: Close error: %v", d.Name(), err)
	}
}

// Generator asserts a generator honors the Generator contract.
func Generator(t testing.TB, g generator.Generator) {
	t.Helper()
	if g.Name() == "" {
		t.Errorf("Generator.Name() is empty")
	}
	buf := make([]byte, 100)
	n, _ := g.Next(buf)
	if n <= 0 || n > len(buf) {
		t.Errorf("%s: Next wrote %d, want within (0,%d]", g.Name(), n, len(buf))
	}
	small := make([]byte, 4)
	if n, _ := g.Next(small); n > len(small) {
		t.Errorf("%s: Next overran a %d-byte buffer (%d)", g.Name(), len(small), n)
	}
}

// Network asserts a netpath.Network honors the connection-factory contract:
// stream and packet round trips, listener close unblocking Accept, and the
// ErrUnsupportedNetwork sentinel. dial and listen are the two ends — pass the
// same Network twice when it is symmetric (e.g. Host on loopback). listenAddr
// is a port-0 bind address valid on listen (e.g. "127.0.0.1:0" for Host, ":0"
// for Memory).
func Network(t testing.TB, dial, listen netpath.Network, listenAddr string) {
	t.Helper()
	if dial.Name() == "" || listen.Name() == "" {
		t.Errorf("Network.Name() is empty")
	}
	networkStream(t, dial, listen, listenAddr)
	networkPacket(t, dial, listen, listenAddr)
	networkPacketDeadlineWake(t, listen, listenAddr)
	networkAcceptUnblock(t, listen, listenAddr)
	networkUnsupported(t, dial, listen, listenAddr)
}

// networkStream checks a Dial/Listen("tcp") echo round trip.
func networkStream(t testing.TB, dial, listen netpath.Network, listenAddr string) {
	t.Helper()
	name := dial.Name()
	ln, err := listen.Listen("tcp", listenAddr)
	if err != nil {
		t.Errorf("%s: Listen(tcp, %s): %v", name, listenAddr, err)
		return
	}
	defer ln.Close()
	served := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			served <- err
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			served <- err
			return
		}
		_, err = c.Write(buf)
		served <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dial.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Errorf("%s: DialContext(tcp, %s): %v", name, ln.Addr(), err)
		return
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	msg := []byte("ping")
	if _, err := c.Write(msg); err != nil {
		t.Errorf("%s: stream Write: %v", name, err)
		return
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(c, echo); err != nil {
		t.Errorf("%s: stream Read: %v", name, err)
		return
	}
	if !bytes.Equal(echo, msg) {
		t.Errorf("%s: stream echo = %q, want %q", name, echo, msg)
	}
	if err := <-served; err != nil {
		t.Errorf("%s: stream serve: %v", name, err)
	}
}

// networkPacket checks a ListenPacket("udp") echo round trip with addressing:
// the server replies to ReadFrom's source address, and the client sees the
// reply come from the server's bound address.
func networkPacket(t testing.TB, dial, listen netpath.Network, listenAddr string) {
	t.Helper()
	name := dial.Name()
	srv, err := listen.ListenPacket("udp", listenAddr)
	if err != nil {
		t.Errorf("%s: ListenPacket(udp, %s): %v", name, listenAddr, err)
		return
	}
	defer srv.Close()
	cli, err := dial.ListenPacket("udp", listenAddr)
	if err != nil {
		t.Errorf("%s: ListenPacket(udp, %s): %v", name, listenAddr, err)
		return
	}
	defer cli.Close()
	deadline := time.Now().Add(5 * time.Second)
	_ = srv.SetDeadline(deadline)
	_ = cli.SetDeadline(deadline)
	msg := []byte("ping")
	if _, err := cli.WriteTo(msg, srv.LocalAddr()); err != nil {
		t.Errorf("%s: packet WriteTo(%s): %v", name, srv.LocalAddr(), err)
		return
	}
	buf := make([]byte, 64)
	n, from, err := srv.ReadFrom(buf)
	if err != nil {
		t.Errorf("%s: packet ReadFrom: %v", name, err)
		return
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Errorf("%s: packet payload = %q, want %q", name, buf[:n], msg)
	}
	if from == nil {
		t.Errorf("%s: packet ReadFrom returned nil source addr", name)
		return
	}
	if _, err := srv.WriteTo(buf[:n], from); err != nil {
		t.Errorf("%s: packet reply WriteTo(%s): %v", name, from, err)
		return
	}
	n, replyFrom, err := cli.ReadFrom(buf)
	if err != nil {
		t.Errorf("%s: packet reply ReadFrom: %v", name, err)
		return
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Errorf("%s: packet reply payload = %q, want %q", name, buf[:n], msg)
	}
	if replyFrom == nil || replyFrom.String() != srv.LocalAddr().String() {
		t.Errorf("%s: packet reply from %v, want %v", name, replyFrom, srv.LocalAddr())
	}
}

// networkPacketDeadlineWake checks the net package's deadline contract on
// packet conns: SetReadDeadline applies to "any currently-blocked Read call",
// so the standard conn.SetReadDeadline(time.Now()) interrupt idiom must wake a
// blocked ReadFrom with a timeout error (os.ErrDeadlineExceeded).
func networkPacketDeadlineWake(t testing.TB, listen netpath.Network, listenAddr string) {
	t.Helper()
	name := listen.Name()
	pc, err := listen.ListenPacket("udp", listenAddr)
	if err != nil {
		t.Errorf("%s: ListenPacket(udp, %s): %v", name, listenAddr, err)
		return
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
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("%s: blocked ReadFrom woke with %v, want os.ErrDeadlineExceeded", name, err)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("%s: blocked ReadFrom not woken within 2s of SetReadDeadline(now)", name)
	}
}

// networkAcceptUnblock checks that closing a listener unblocks a pending
// Accept with an error.
func networkAcceptUnblock(t testing.TB, listen netpath.Network, listenAddr string) {
	t.Helper()
	name := listen.Name()
	ln, err := listen.Listen("tcp", listenAddr)
	if err != nil {
		t.Errorf("%s: Listen(tcp, %s): %v", name, listenAddr, err)
		return
	}
	got := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		got <- err
	}()
	time.Sleep(10 * time.Millisecond) // let Accept block
	_ = ln.Close()
	select {
	case err := <-got:
		if err == nil {
			t.Errorf("%s: Accept returned nil error after Close", name)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("%s: Accept did not unblock within 2s of Close", name)
	}
}

// networkUnsupported checks the ErrUnsupportedNetwork sentinel: an unknown
// network name, a datagram network passed to Listen, and a stream network
// passed to ListenPacket must all match via errors.Is.
func networkUnsupported(t testing.TB, dial, listen netpath.Network, listenAddr string) {
	t.Helper()
	name := dial.Name()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := dial.DialContext(ctx, "unix", "/nonexistent"); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("%s: DialContext(unix) error = %v, want ErrUnsupportedNetwork", name, err)
	}
	if _, err := listen.Listen("udp", listenAddr); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("%s: Listen(udp) error = %v, want ErrUnsupportedNetwork", name, err)
	}
	if _, err := listen.ListenPacket("tcp", listenAddr); !errors.Is(err, netpath.ErrUnsupportedNetwork) {
		t.Errorf("%s: ListenPacket(tcp) error = %v, want ErrUnsupportedNetwork", name, err)
	}
}

// Payloader asserts a payloader honors the Payloader contract.
func Payloader(t testing.TB, p payload.Payloader) {
	t.Helper()
	if p.Name() == "" {
		t.Errorf("Payloader.Name() is empty")
	}
	buf := make([]byte, 50)
	n, err := p.Read(buf)
	if err != nil {
		t.Errorf("%s: Read error: %v", p.Name(), err)
	}
	if n != len(buf) {
		t.Errorf("%s: Read filled %d, want %d", p.Name(), n, len(buf))
	}
}
