// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"testing"
)

// TestArenaZeroCopy proves the TxDatapath/RxDatapath contract permits true zero
// copy: a packet transmitted through the arena is received from the *same*
// backing memory, never copied. This is the property AF_XDP/DPDK rely on.
func TestArenaZeroCopy(t *testing.T) {
	a := NewArena(4, 1500)

	// Transmit one packet, remembering where its bytes live.
	tx := a.TxReserve(1)
	if len(tx) != 1 {
		t.Fatalf("TxReserve returned %d frames, want 1", len(tx))
	}
	msg := []byte("zero-copy")
	copy(tx[0].Data, msg)
	tx[0].Len = len(msg)
	txAddr := &tx[0].Data[0] // address of the first transmitted byte
	if sent, err := a.TxCommit(tx[:1]); err != nil || sent != 1 {
		t.Fatalf("TxCommit = %d, %v; want 1, nil", sent, err)
	}

	// Receive it and confirm the bytes come from the identical address — no copy.
	rx, err := a.RxPoll(1)
	if err != nil || len(rx) != 1 {
		t.Fatalf("RxPoll = %d frames, %v; want 1, nil", len(rx), err)
	}
	if rx[0].Len != len(msg) || string(rx[0].Data) != string(msg) {
		t.Fatalf("RxPoll data = %q (len %d), want %q", rx[0].Data, rx[0].Len, msg)
	}
	if &rx[0].Data[0] != txAddr {
		t.Fatal("received bytes are at a different address than transmitted — a copy occurred")
	}
	if rx[0].Meta.Nanos == 0 {
		t.Error("RX frame missing receive timestamp")
	}
	a.RxRelease(rx)
}

// TestArenaReuseAfterRelease checks frames cycle back to the free list so the
// arena can run indefinitely without growing.
func TestArenaReuseAfterRelease(t *testing.T) {
	a := NewArena(2, 64) // only 2 frames
	for i := 0; i < 100; i++ {
		tx := a.TxReserve(1)
		if len(tx) == 0 {
			t.Fatalf("iter %d: TxReserve starved — frames not recycled", i)
		}
		tx[0].Len = 8
		_, _ = a.TxCommit(tx[:1])
		rx, _ := a.RxPoll(1)
		a.RxRelease(rx)
	}
}

// TestArenaAllocFree asserts the arena's hot-path ops allocate nothing.
func TestArenaAllocFree(t *testing.T) {
	a := NewArena(8, 1500)
	allocs := testing.AllocsPerRun(1000, func() {
		tx := a.TxReserve(1)
		tx[0].Len = 100
		_, _ = a.TxCommit(tx[:1])
		rx, _ := a.RxPoll(1)
		a.RxRelease(rx)
	})
	if allocs != 0 {
		t.Fatalf("arena hot-path allocs = %v, want 0", allocs)
	}
}
