// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package generator

import (
	"testing"

	"github.com/bgrewell/loom/core/payload"
)

func TestStreamNext(t *testing.T) {
	s := NewStream(payload.NewPatterned(), 8)
	if s.Name() != "stream" {
		t.Fatalf("name = %q", s.Name())
	}
	buf := make([]byte, 16)
	n, done := s.Next(buf)
	if n != 8 || done {
		t.Fatalf("Next = %d, done=%v; want 8, false", n, done)
	}
}

func TestStreamRespectsBuf(t *testing.T) {
	s := NewStream(payload.NewRandom(32, 1), 100)
	buf := make([]byte, 10) // smaller than packet size
	if n, _ := s.Next(buf); n != 10 {
		t.Fatalf("Next bounded by buf = %d, want 10", n)
	}
}

func TestRegistryBuild(t *testing.T) {
	g, err := Registry.Build("stream", Options{Payload: "patterned", PacketSize: 64})
	if err != nil || g.Name() != "stream" {
		t.Fatalf("build stream = %v, %v", g, err)
	}
	// Default payload ("random") when unset.
	if _, err := Registry.Build("stream", Options{}); err != nil {
		t.Fatalf("build stream with defaults: %v", err)
	}
	if _, err := Registry.Build("stream", Options{Payload: "nope"}); err == nil {
		t.Fatal("unknown payload should propagate an error")
	}
	if _, err := Registry.Build("nope", Options{}); err == nil {
		t.Fatal("unknown generator should error")
	}
}
