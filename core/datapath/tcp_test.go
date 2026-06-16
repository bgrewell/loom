// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"fmt"
	"testing"
	"time"
)

// TestTCPDatapathRoundTrip sends a batch through the TCP transmit datapath and
// reads it back through the TCP listener, checking every byte arrives (the Tx
// writes the whole batch in one vectored write; the stream may recombine it).
func TestTCPDatapathRoundTrip(t *testing.T) {
	lis, err := ListenTCP("127.0.0.1:0", 1400)
	if err != nil {
		t.Fatalf("ListenTCP: %v", err)
	}
	defer lis.Close()

	tx, err := DialTCP(fmt.Sprintf("127.0.0.1:%d", lis.Port()), 1400)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	defer tx.Close()

	const (
		batch   = 8
		perPkt  = 1000
		wantTot = batch * perPkt
	)
	frames := tx.TxReserve(batch)
	if len(frames) < batch {
		t.Fatalf("TxReserve = %d, want %d", len(frames), batch)
	}
	for i := 0; i < batch; i++ {
		for j := 0; j < perPkt; j++ {
			frames[i].Data[j] = byte(i)
		}
		frames[i].Len = perPkt
	}
	if sent, err := tx.TxCommit(frames[:batch]); err != nil || sent != batch {
		t.Fatalf("TxCommit = (%d, %v), want %d", sent, err, batch)
	}

	got := 0
	deadline := time.Now().Add(3 * time.Second)
	for got < wantTot && time.Now().Before(deadline) {
		fr, err := lis.RxPoll(64)
		if err != nil {
			t.Fatalf("RxPoll: %v", err)
		}
		for i := range fr {
			got += fr[i].Len
		}
		lis.RxRelease(fr)
	}
	if got != wantTot {
		t.Fatalf("received %d bytes, want %d", got, wantTot)
	}
}

// TestTCPListenerIdleTimeout: with no connection, RxPoll returns (nil, nil) so the
// receiver loop can check for cancellation between polls.
func TestTCPListenerIdleTimeout(t *testing.T) {
	lis, err := ListenTCP("127.0.0.1:0", 1400)
	if err != nil {
		t.Fatalf("ListenTCP: %v", err)
	}
	defer lis.Close()
	start := time.Now()
	fr, err := lis.RxPoll(64)
	if err != nil || fr != nil {
		t.Fatalf("idle RxPoll = (%v, %v), want (nil, nil)", fr, err)
	}
	if d := time.Since(start); d < recvDeadline/2 {
		t.Errorf("RxPoll returned too fast (%v); should block ~%v", d, recvDeadline)
	}
}
