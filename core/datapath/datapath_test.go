// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"net"
	"testing"
	"time"
)

func TestMemoryRoundTrip(t *testing.T) {
	m := NewMemory(4, 64)
	if m.Name() != "memory" {
		t.Fatalf("name = %q", m.Name())
	}
	tx := m.TxReserve(1)
	if len(tx) != 1 {
		t.Fatalf("TxReserve returned %d frames", len(tx))
	}
	n := copy(tx[0].Data, []byte("hello"))
	tx[0].Len = n
	if sent, err := m.TxCommit(tx[:1]); err != nil || sent != 1 {
		t.Fatalf("TxCommit = %d, %v", sent, err)
	}
	rx, err := m.RxPoll(1)
	if err != nil || len(rx) != 1 {
		t.Fatalf("RxPoll = %d, %v", len(rx), err)
	}
	if got := string(rx[0].Data[:rx[0].Len]); got != "hello" {
		t.Fatalf("RxPoll = %q, want hello", got)
	}
	m.RxRelease(rx)
}

func TestMemoryFullReservesFewer(t *testing.T) {
	m := NewMemory(1, 64) // a single frame
	tx := m.TxReserve(1)
	if len(tx) != 1 {
		t.Fatalf("first TxReserve = %d, want 1", len(tx))
	}
	// With the only frame reserved (not yet committed), a second reserve gets none.
	if more := m.TxReserve(1); len(more) != 0 {
		t.Fatalf("second TxReserve = %d, want 0 (exhausted)", len(more))
	}
}

func TestUDPSocketLoopback(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	s, err := DialUDP(pc.LocalAddr().String(), 1500)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer s.Close()

	tx := s.TxReserve(1)
	n := copy(tx[0].Data, []byte("ping"))
	tx[0].Len = n
	if _, err := s.TxCommit(tx[:1]); err != nil {
		t.Fatalf("commit: %v", err)
	}

	buf := make([]byte, 16)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("readfrom: %v", err)
	}
	if g := string(buf[:got]); g != "ping" {
		t.Fatalf("received %q, want ping", g)
	}
}

// TestUDPSocketBatch sends a multi-datagram batch in one TxCommit (one sendmmsg)
// and checks every datagram arrives — exercising the batched send path (#56).
func TestUDPSocketBatch(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	s, err := DialUDP(pc.LocalAddr().String(), 1500)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer s.Close()

	const batch = 16
	tx := s.TxReserve(batch)
	if len(tx) < batch {
		t.Fatalf("TxReserve = %d, want %d", len(tx), batch)
	}
	for i := 0; i < batch; i++ {
		tx[i].Len = copy(tx[i].Data, []byte{byte(i), 'x', 'y', 'z'})
	}
	if sent, err := s.TxCommit(tx[:batch]); err != nil || sent != batch {
		t.Fatalf("TxCommit = (%d, %v), want %d sent in one sendmmsg", sent, err, batch)
	}

	// All datagrams must be received (loopback is reliable for a small burst).
	seen := 0
	buf := make([]byte, 64)
	for seen < batch {
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, rerr := pc.ReadFrom(buf)
		if rerr != nil {
			t.Fatalf("after %d datagrams: %v", seen, rerr)
		}
		if n != 4 {
			t.Fatalf("datagram %d had %d bytes, want 4", seen, n)
		}
		seen++
	}
}

func TestUDPListenerReceives(t *testing.T) {
	l, err := ListenUDP("127.0.0.1:0", 1500)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	sender, err := net.Dial("udp", l.conn.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sender.Close()
	if _, err := sender.Write([]byte("data")); err != nil {
		t.Fatalf("write: %v", err)
	}

	frames, err := l.RxPoll(8)
	if err != nil {
		t.Fatalf("RxPoll: %v", err)
	}
	if len(frames) != 1 || string(frames[0].Data[:frames[0].Len]) != "data" {
		t.Fatalf("RxPoll returned %d frames: %q", len(frames), frames)
	}
	if frames[0].Meta.Nanos == 0 {
		t.Error("frame missing receive timestamp")
	}
	if !frames[0].Meta.Src.IsValid() {
		t.Error("frame missing source address")
	}
	l.RxRelease(frames)
}
