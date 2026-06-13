// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestSync(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := NewGRPCServer(NewServer("test-1.0"))
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	client, conn, err := Dial(lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s, err := Sync(ctx, client)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// Round-trip delay against an in-process agent must be non-negative and
	// bounded; offset between two reads of the same wall clock is small.
	if s.Delay < 0 {
		t.Errorf("Delay = %v, want >= 0", s.Delay)
	}
	if s.Delay > 5*time.Second {
		t.Errorf("Delay = %v, implausibly large", s.Delay)
	}
}
