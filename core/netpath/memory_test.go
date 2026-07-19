// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netpath_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/netpath"
)

// TestMemoryNetworkCloseUnblocksAccept verifies Network.Close (not just
// Listener.Close) unblocks a pending Accept and fails later listens.
func TestMemoryNetworkCloseUnblocksAccept(t *testing.T) {
	a, b := netpath.Memory()
	defer a.Close()

	ln, err := b.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	got := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		got <- err
	}()
	time.Sleep(10 * time.Millisecond) // let Accept block
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-got:
		if err == nil {
			t.Error("Accept returned nil error after network Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not unblock within 2s of network Close")
	}
	if _, err := b.Listen("tcp", ":0"); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Listen after Close = %v, want net.ErrClosed", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := b.DialContext(ctx, "tcp", "mem:1"); !errors.Is(err, net.ErrClosed) {
		t.Errorf("DialContext after Close = %v, want net.ErrClosed", err)
	}
}

// TestMemoryConnectedUDP verifies DialContext("udp") gives connected-socket
// semantics: Write reaches the bound endpoint, Read sees only the remote's
// replies, and the remote observes the dialer's ephemeral source address.
func TestMemoryConnectedUDP(t *testing.T) {
	a, b := netpath.Memory()
	defer a.Close()
	defer b.Close()

	srv, err := b.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer srv.Close()
	_ = srv.SetDeadline(time.Now().Add(5 * time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := a.DialContext(ctx, "udp", srv.LocalAddr().String())
	if err != nil {
		t.Fatalf("DialContext(udp): %v", err)
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
	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("payload = %q, want %q", buf[:n], msg)
	}
	if from.String() != c.LocalAddr().String() {
		t.Errorf("source addr = %v, want dialer's %v", from, c.LocalAddr())
	}
	if _, err := srv.WriteTo(buf[:n], from); err != nil {
		t.Fatalf("reply WriteTo: %v", err)
	}
	n, err = c.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Errorf("reply = %q, want %q", buf[:n], msg)
	}
}

// TestMemoryAddrInUse verifies the shared port namespace rejects a duplicate
// bind from either handle, while TCP and UDP ports stay independent.
func TestMemoryAddrInUse(t *testing.T) {
	a, b := netpath.Memory()
	defer a.Close()
	defer b.Close()

	ln, err := b.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	if _, err := a.Listen("tcp", ln.Addr().String()); err == nil {
		t.Error("duplicate tcp bind succeeded, want address-in-use error")
	}
	// Same numeric port on udp is a distinct namespace, as in a kernel stack.
	pc, err := a.ListenPacket("udp", ln.Addr().String())
	if err != nil {
		t.Fatalf("udp bind on tcp's port: %v", err)
	}
	pc.Close()
}

// TestMemoryReadDeadline verifies a packet-conn read deadline expires with a
// net.Error whose Timeout() is true.
func TestMemoryReadDeadline(t *testing.T) {
	a, _ := netpath.Memory()
	defer a.Close()
	pc, err := a.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()
	_ = pc.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	buf := make([]byte, 16)
	_, _, err = pc.ReadFrom(buf)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("ReadFrom past deadline = %v, want net.Error with Timeout()", err)
	}
}
