// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package contract holds shared conformance checks that every implementation of
// a core interface must pass. Registry plugins call these from their tests so a
// new backend or protocol can't silently drift from the interface contract. See
// docs/testing.md (Tier 2).
package contract

import (
	"context"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
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
