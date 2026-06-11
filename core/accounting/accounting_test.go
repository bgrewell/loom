// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package accounting

import (
	"sync"
	"testing"
	"time"
)

func TestCountersConcurrent(t *testing.T) {
	var c Counters
	const goroutines, each = 8, 1000
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				c.Add(100)
			}
		}()
	}
	wg.Wait()
	if got, want := c.Packets(), uint64(goroutines*each); got != want {
		t.Fatalf("packets = %d, want %d", got, want)
	}
	if got, want := c.Bytes(), uint64(goroutines*each*100); got != want {
		t.Fatalf("bytes = %d, want %d", got, want)
	}
}

func TestSamplerRate(t *testing.T) {
	var c Counters
	s := NewSampler(&c, 4)

	c.Add(1_000_000) // 1,000,000 bytes in the first second
	if bps := s.Sample(time.Second); bps != 8_000_000 {
		t.Fatalf("bps = %v, want 8000000", bps)
	}

	c.Add(500_000) // 500,000 more bytes in the next second
	if bps := s.Sample(time.Second); bps != 4_000_000 {
		t.Fatalf("bps = %v, want 4000000", bps)
	}

	if avg := s.Average(); avg != 6_000_000 {
		t.Fatalf("avg = %v, want 6000000", avg)
	}
}

func TestSamplerZeroElapsed(t *testing.T) {
	var c Counters
	s := NewSampler(&c, 2)
	c.Add(100)
	if bps := s.Sample(0); bps != 0 {
		t.Fatalf("zero elapsed should yield 0 bps, got %v", bps)
	}
}

func TestRingOverwrite(t *testing.T) {
	r := NewRing[int](3)
	for i := 1; i <= 5; i++ {
		r.Push(i)
	}
	got := r.Slice()
	want := []int{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slice = %v, want %v", got, want)
		}
	}
}
