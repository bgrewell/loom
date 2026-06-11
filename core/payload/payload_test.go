// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package payload

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestPatternedSequence(t *testing.T) {
	p := NewPatterned()
	if p.Name() != "patterned" {
		t.Fatalf("name = %q", p.Name())
	}
	buf := make([]byte, 24) // three 8-byte records
	n, err := p.Read(buf)
	if err != nil || n != len(buf) {
		t.Fatalf("Read = %d, %v", n, err)
	}
	for i := 0; i < 3; i++ {
		got := binary.BigEndian.Uint64(buf[i*8 : i*8+8])
		if got != uint64(i) {
			t.Fatalf("record %d = %d, want %d", i, got, i)
		}
	}
}

func TestPatternedSpansReads(t *testing.T) {
	p := NewPatterned()
	// Read 4 bytes at a time; the sequence must continue across calls.
	all := make([]byte, 0, 16)
	for i := 0; i < 4; i++ {
		b := make([]byte, 4)
		p.Read(b)
		all = append(all, b...)
	}
	if binary.BigEndian.Uint64(all[0:8]) != 0 || binary.BigEndian.Uint64(all[8:16]) != 1 {
		t.Fatalf("sequence not continuous across reads: %x", all)
	}
}

func TestRandomDeterministic(t *testing.T) {
	a := NewRandom(64, 42)
	b := NewRandom(64, 42)
	pa := make([]byte, 100)
	pb := make([]byte, 100)
	a.Read(pa)
	b.Read(pb)
	if !bytes.Equal(pa, pb) {
		t.Fatal("same seed should produce identical bytes")
	}
}

func TestRandomRingWraps(t *testing.T) {
	r := NewRandom(8, 1)
	p := make([]byte, 20) // larger than the buffer → must wrap
	if n, _ := r.Read(p); n != 20 {
		t.Fatalf("Read = %d, want 20", n)
	}
}

func TestNewRandomClampsSize(t *testing.T) {
	r := NewRandom(0, 1)
	if len(r.buf) < 1 {
		t.Fatal("size should be clamped to at least 1")
	}
	if n, _ := r.Read(make([]byte, 4)); n != 4 {
		t.Fatalf("Read = %d, want 4", n)
	}
}
