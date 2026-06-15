// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/scenario"
)

// TestExactIntervalLines is the headline guarantee end to end: a D-long run at
// interval I produces exactly floor(D/I) complete, consolidated interval lines —
// the agents own the interval clock, the controller just sums by index. A
// non-multiple D avoids the last-boundary-vs-completion race.
func TestExactIntervalLines(t *testing.T) {
	clientAddr, stopClient := startAgent(t)
	defer stopClient()
	serverAddr, stopServer := startAgent(t)
	defer stopServer()

	const interval = 250 * time.Millisecond
	const dur = 1100 * time.Millisecond // floor(1.1s / 0.25s) = 4 full intervals
	s := &scenario.Scenario{
		Name: "exact",
		Seed: 1,
		Endpoints: []scenario.Endpoint{
			{Name: "client"},
			{Name: "server"},
		},
		Timeline: []scenario.Event{{
			Name:  "blast",
			Flow:  scenario.Flow{Kind: "udp", Params: map[string]any{"packet_size": 1200, "rate": "50Mbps"}},
			From:  scenario.Selector{Raw: "client"},
			To:    scenario.Selector{Raw: "server"},
			Start: scenario.Start{Offset: 0},
			Stop:  scenario.Stop{After: dur},
		}},
	}

	c := New(s, map[string]string{"client": clientAddr, "server": serverAddr}, WithInterval(interval))
	defer c.Close()

	tel := NewTelemetry(interval)
	defer tel.Close()

	var mu sync.Mutex
	var lines, complete int
	tel.AddObserver(ObserverFunc(func(a Aggregate) {
		mu.Lock()
		lines++
		if a.Complete && a.TxBytes > 0 && a.RxBytes > 0 {
			complete++
		}
		mu.Unlock()
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go tel.Collect(ctx, c)

	if err := c.Run(ctx, 5*time.Second); err != nil {
		t.Fatalf("controller Run: %v", err)
	}
	tel.WaitSources(ctx, c)
	time.Sleep(300 * time.Millisecond) // let the last interval flush
	c.Teardown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	want := int(dur / interval) // 4
	if lines != want || complete != want {
		t.Fatalf("emitted %d lines (%d complete), want exactly %d complete", lines, complete, want)
	}
}
