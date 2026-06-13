// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package latency

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// fakePinger returns a fixed RTT/error.
type fakePinger struct {
	rtt time.Duration
	err error
}

func (f fakePinger) Ping(context.Context, uint64) (time.Duration, error) { return f.rtt, f.err }

func TestSamplerEmitsBatches(t *testing.T) {
	s := &Sampler{
		Pinger:   fakePinger{rtt: 5 * time.Millisecond},
		Interval: 5 * time.Millisecond,
		Probes:   3,
		Timeout:  time.Second,
	}
	var mu sync.Mutex
	var batches [][]Result
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Millisecond)
	defer cancel()
	s.Run(ctx, func(b []Result) {
		mu.Lock()
		batches = append(batches, b)
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	if len(batches) == 0 {
		t.Fatal("expected at least one batch")
	}
	for _, b := range batches {
		if len(b) != 3 {
			t.Fatalf("batch size = %d, want 3", len(b))
		}
		for _, r := range b {
			if r.State != StateOK || r.RTT != 5*time.Millisecond {
				t.Fatalf("result = %+v, want OK 5ms", r)
			}
		}
	}
}

func TestUDPPingerAgainstEcho(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()

	p, err := NewUDPPinger(pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("pinger: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rtt, err := p.Ping(ctx, 1)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if rtt <= 0 {
		t.Fatalf("rtt = %v, want > 0", rtt)
	}
}

func TestSummarize(t *testing.T) {
	rs := []Result{
		{State: StateOK, RTT: 10 * time.Millisecond},
		{State: StateOK, RTT: 20 * time.Millisecond},
		{State: StateOK, RTT: 30 * time.Millisecond},
		{State: StateTimeout},
	}
	sum := Summarize(rs)
	if sum.Sent != 4 || sum.Received != 3 || sum.Lost != 1 {
		t.Fatalf("counts = %d/%d/%d, want 4/3/1", sum.Sent, sum.Received, sum.Lost)
	}
	if sum.LossPct != 25 {
		t.Fatalf("loss%% = %v, want 25", sum.LossPct)
	}
	if sum.Min != 10*time.Millisecond || sum.Max != 30*time.Millisecond || sum.Mean != 20*time.Millisecond {
		t.Fatalf("min/max/mean = %v/%v/%v", sum.Min, sum.Max, sum.Mean)
	}
}
