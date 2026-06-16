// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package flow

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/payload"
	"github.com/bgrewell/loom/core/scheduler"
)

// TestBuildTCPDecouplesBlockFromPacketSize: a TCP flow ignores the (wire-
// meaningless) packet_size for its write granularity and uses the large
// tcpWriteBlock, so throughput isn't throttled to MTU-sized writes. A non-TCP
// datapath keeps packet_size as its frame size.
func TestBuildTCPDecouplesBlockFromPacketSize(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	tcp, err := Build(Spec{Datapath: "tcp", Target: ln.Addr().String(), PacketSize: 1400}, nil)
	if err != nil {
		t.Fatalf("build tcp: %v", err)
	}
	defer tcp.Datapath.Close()
	if tcp.MTU != tcpWriteBlock {
		t.Errorf("tcp block = %d, want %d (decoupled from packet_size 1400)", tcp.MTU, tcpWriteBlock)
	}

	udp, err := Build(Spec{Datapath: "udp", Target: "127.0.0.1:9", PacketSize: 1400}, nil)
	if err != nil {
		t.Fatalf("build udp: %v", err)
	}
	defer udp.Datapath.Close()
	if udp.MTU != 1400 {
		t.Errorf("udp block = %d, want 1400 (packet_size preserved)", udp.MTU)
	}
}

func newStream(size int) generator.Generator {
	return generator.NewStream(payload.NewRandom(2048, 1), size)
}

func TestFlowStopCount(t *testing.T) {
	f := &Flow{
		Generator: newStream(100),
		Scheduler: scheduler.Soak{},
		Datapath:  datapath.NewDiscard(1500),
		Stop:      Stop{Count: 100},
	}
	if err := f.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.Counters().Packets(); got != 100 {
		t.Fatalf("packets = %d, want 100", got)
	}
}

func TestFlowStopVolume(t *testing.T) {
	f := &Flow{
		Generator: newStream(100),
		Scheduler: scheduler.Soak{},
		Datapath:  datapath.NewDiscard(1500),
		Stop:      Stop{Volume: 1000},
	}
	if err := f.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Stops at the first packet that reaches/exceeds 1000 bytes; with 100-byte
	// packets that's exactly 1000 (overshoot at most one packet).
	b := f.Counters().Bytes()
	if b < 1000 || b >= 1100 {
		t.Fatalf("bytes = %d, want in [1000,1100)", b)
	}
}

func TestFlowStopAfter(t *testing.T) {
	f := &Flow{
		Generator: newStream(1400),
		Scheduler: scheduler.Soak{},
		Datapath:  datapath.NewDiscard(1500),
		Stop:      Stop{After: 20 * time.Millisecond},
	}
	start := time.Now()
	if err := f.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("duration-bounded flow ran too long: %v", elapsed)
	}
	if f.Counters().Packets() == 0 {
		t.Fatal("expected some packets to be sent")
	}
}

func TestFlowUntilStopped(t *testing.T) {
	f := &Flow{
		Generator: newStream(1400),
		Scheduler: scheduler.Soak{},
		Datapath:  datapath.NewDiscard(1500),
		// zero Stop → runs until ctx is cancelled
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.Counters().Packets() == 0 {
		t.Fatal("expected some packets before cancellation")
	}
}
