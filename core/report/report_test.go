// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/accounting"
)

type capture struct {
	mu      sync.Mutex
	samples []Sample
	summary *Summary
}

func (c *capture) Sample(s Sample) { c.mu.Lock(); c.samples = append(c.samples, s); c.mu.Unlock() }
func (c *capture) Summary(s Summary) {
	c.mu.Lock()
	c.summary = &s
	c.mu.Unlock()
}

func TestCollectSummary(t *testing.T) {
	var c accounting.Counters
	c.Add(1000)
	c.Add(400)

	cap := &capture{}
	done := make(chan struct{})
	close(done) // finish immediately → no samples, just the summary

	sum := Collect(context.Background(), &c, time.Hour, cap, done)
	if sum.Bytes != 1400 || sum.Packets != 2 {
		t.Fatalf("summary = %+v, want 1400 bytes / 2 packets", sum)
	}
	if cap.summary == nil {
		t.Fatal("summary was not emitted to the reporter")
	}
}

func TestCollectSamples(t *testing.T) {
	var c accounting.Counters
	cap := &capture{}
	done := make(chan struct{})

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				c.Add(1000)
				time.Sleep(time.Millisecond)
			}
		}
	}()

	go func() { time.Sleep(50 * time.Millisecond); close(done) }()
	Collect(context.Background(), &c, 5*time.Millisecond, cap, done)
	close(stop)

	cap.mu.Lock()
	n := len(cap.samples)
	cap.mu.Unlock()
	if n == 0 {
		t.Fatal("expected at least one interval sample")
	}
}

func TestHumanOutput(t *testing.T) {
	var b bytes.Buffer
	h := NewHuman(&b)
	h.Sample(Sample{Elapsed: time.Second, Bytes: 2 << 20, Packets: 1000, BitsPerSec: 94e6})
	h.Summary(Summary{Duration: 2 * time.Second, Bytes: 4 << 20, Packets: 2000, AvgBitsPerSec: 88e6})
	out := b.String()
	if !strings.Contains(out, "Mbps") || !strings.Contains(out, "summary") {
		t.Fatalf("human output missing expected content:\n%s", out)
	}
}

func TestJSONOutput(t *testing.T) {
	var b bytes.Buffer
	j := NewJSON(&b)
	j.Sample(Sample{Elapsed: time.Second, Bytes: 100, Packets: 1, BitsPerSec: 800})
	j.Summary(Summary{Duration: time.Second, Bytes: 100, Packets: 1, AvgBitsPerSec: 800})
	out := b.String()
	if !strings.Contains(out, `"type":"sample"`) || !strings.Contains(out, `"type":"summary"`) {
		t.Fatalf("json output missing type tags:\n%s", out)
	}
}
