// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"net"
	"testing"
	"time"
)

func TestMemoryRoundTrip(t *testing.T) {
	m := NewMemory(4)
	if m.Name() != "memory" {
		t.Fatalf("name = %q", m.Name())
	}
	want := []byte("hello")
	if n, err := m.Send(want); err != nil || n != len(want) {
		t.Fatalf("Send = %d, %v", n, err)
	}
	// Mutating the caller's buffer must not affect the queued copy.
	want[0] = 'H'

	buf := make([]byte, 16)
	n, err := m.Recv(buf)
	if err != nil {
		t.Fatalf("Recv error: %v", err)
	}
	if got := string(buf[:n]); got != "hello" {
		t.Fatalf("Recv = %q, want hello", got)
	}
}

func TestMemoryFullAndEmpty(t *testing.T) {
	m := NewMemory(1)
	if _, err := m.Send([]byte("a")); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := m.Send([]byte("b")); err != ErrFull {
		t.Fatalf("second send err = %v, want ErrFull", err)
	}
	if _, err := m.Recv(make([]byte, 4)); err != nil {
		t.Fatalf("recv: %v", err)
	}
	if _, err := m.Recv(make([]byte, 4)); err != ErrEmpty {
		t.Fatalf("empty recv err = %v, want ErrEmpty", err)
	}
}

func TestUDPSocketLoopback(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	s, err := DialUDP(pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer s.Close()

	if _, err := s.Send([]byte("ping")); err != nil {
		t.Fatalf("send: %v", err)
	}

	buf := make([]byte, 16)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("readfrom: %v", err)
	}
	if got := string(buf[:n]); got != "ping" {
		t.Fatalf("received %q, want ping", got)
	}
}
