// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package log

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRingFIFOAndFull(t *testing.T) {
	r := NewRing(4) // power of two
	for i := 0; i < r.Cap(); i++ {
		if !r.Push(Event{Seq: uint64(i)}) {
			t.Fatalf("push %d should fit", i)
		}
	}
	// Now full → next push drops, never blocks.
	if r.Push(Event{Seq: 999}) {
		t.Fatal("push into full ring should return false")
	}
	if r.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", r.Dropped())
	}
	// FIFO order.
	for i := 0; i < r.Cap(); i++ {
		e, ok := r.Pop()
		if !ok || e.Seq != uint64(i) {
			t.Fatalf("pop %d = %+v ok=%v", i, e, ok)
		}
	}
	if _, ok := r.Pop(); ok {
		t.Fatal("pop on empty ring should be false")
	}
}

func TestRingZeroAllocPush(t *testing.T) {
	r := NewRing(1024)
	e := Event{Code: EventSent, Value: 1400}
	allocs := testing.AllocsPerRun(1000, func() {
		if !r.Push(e) {
			r.Pop() // keep it from staying full
			r.Push(e)
		}
	})
	if allocs != 0 {
		t.Fatalf("ring push allocs = %v, want 0", allocs)
	}
}

func TestRingSPSCConcurrent(t *testing.T) {
	const n = 100000
	r := NewRing(1024)
	var got uint64
	var wg sync.WaitGroup
	wg.Add(2)

	go func() { // producer
		defer wg.Done()
		for i := 0; i < n; i++ {
			for !r.Push(Event{Seq: uint64(i)}) {
				// ring full; spin (consumer will drain)
			}
		}
	}()
	go func() { // consumer
		defer wg.Done()
		for got < n {
			if _, ok := r.Pop(); ok {
				got++
			}
		}
	}()
	wg.Wait()
	if got != n {
		t.Fatalf("consumed %d, want %d", got, n)
	}
}

func TestDrainer(t *testing.T) {
	r := NewRing(16)
	var mu sync.Mutex
	var seen []uint64
	d := NewDrainer(r, func(e Event) {
		mu.Lock()
		seen = append(seen, e.Seq)
		mu.Unlock()
	}, 200*time.Microsecond)

	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	for i := 0; i < 5; i++ {
		for !r.Push(Event{Seq: uint64(i)}) {
		}
	}
	// Let it drain, then stop.
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(5 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 5 {
		t.Fatalf("drained %d events, want 5: %v", len(seen), seen)
	}
}

func TestLogger(t *testing.T) {
	var b bytes.Buffer
	l := New(&b, LevelInfo)
	l.Debug("hidden") // below level → filtered
	l.Info("hello")
	l.Error("boom")
	l.Close() // flush

	out := b.String()
	if strings.Contains(out, "hidden") {
		t.Fatalf("debug line should be filtered:\n%s", out)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "boom") {
		t.Fatalf("missing expected lines:\n%s", out)
	}
}
