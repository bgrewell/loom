// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package datapath

import (
	"testing"
	"time"
)

// TestUDPListenerGROReceivesAllDatagrams sends a burst through a GSO-capable
// sender into a GRO-enabled listener and checks every datagram is recovered at
// the right size and in order — i.e. GRO coalescing is split back correctly.
func TestUDPListenerGROReceivesAllDatagrams(t *testing.T) {
	ln, err := ListenUDP("127.0.0.1:0", 2048)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !ln.gro {
		t.Skip("UDP GRO not available here")
	}

	const size = 1200
	tx, err := DialUDP(ln.conn.LocalAddr().String(), size)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()

	const n = 40
	frames := tx.TxReserve(n)
	if len(frames) < n {
		t.Fatalf("reserved %d, want %d", len(frames), n)
	}
	for i := 0; i < n; i++ {
		for j := 0; j < size; j++ {
			frames[i].Data[j] = byte(i)
		}
		frames[i].Len = size
	}
	if _, err := tx.TxCommit(frames[:n]); err != nil {
		t.Fatalf("TxCommit: %v", err)
	}

	got, deadline := 0, time.Now().Add(2*time.Second)
	for got < n && time.Now().Before(deadline) {
		rx, err := ln.RxPoll(64)
		if err != nil {
			continue
		}
		for i := range rx {
			if rx[i].Len != size {
				t.Fatalf("datagram %d size %d, want %d (GRO split wrong)", got, rx[i].Len, size)
			}
			if rx[i].Data[0] != byte(got) {
				t.Fatalf("datagram %d first byte %d, want %d (out of order)", got, rx[i].Data[0], got)
			}
			got++
		}
		ln.RxRelease(rx)
	}
	if got != n {
		t.Fatalf("received %d datagrams, want %d", got, n)
	}
}
