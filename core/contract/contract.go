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
	if !s.Pace(live) {
		t.Errorf("%s: Pace returned false with a live context", s.Name())
	}
	dead, c2 := context.WithCancel(context.Background())
	c2()
	if s.Pace(dead) {
		t.Errorf("%s: Pace returned true with a cancelled context", s.Name())
	}
}

// Datapath asserts a datapath honors the Datapath contract. The datapath must be
// usable without external setup (e.g. memory/discard).
func Datapath(t testing.TB, d datapath.Datapath) {
	t.Helper()
	if d.Name() == "" {
		t.Errorf("Datapath.Name() is empty")
	}
	d.Caps() // must not panic
	msg := []byte("contract")
	n, err := d.Send(msg)
	if err != nil {
		t.Errorf("%s: Send error: %v", d.Name(), err)
	}
	if n != len(msg) {
		t.Errorf("%s: Send wrote %d, want %d", d.Name(), n, len(msg))
	}
	_, _ = d.Recv(make([]byte, 32)) // must not panic
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
