// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/scenario"
)

func TestTelemetryAggregatesAcrossAgents(t *testing.T) {
	clientAddr, stopClient := startAgent(t)
	defer stopClient()
	serverAddr, stopServer := startAgent(t)
	defer stopServer()

	s := &scenario.Scenario{
		Name: "tel",
		Seed: 3,
		Endpoints: []scenario.Endpoint{
			{Name: "client"},
			{Name: "server"},
		},
		Timeline: []scenario.Event{{
			Name:  "blast",
			Flow:  scenario.Flow{Kind: "udp", Params: map[string]any{"packet_size": 1000, "rate": "50Mbps"}},
			From:  scenario.Selector{Raw: "client"},
			To:    scenario.Selector{Raw: "server"},
			Start: scenario.Start{Offset: 0},
			Stop:  scenario.Stop{After: 800 * time.Millisecond},
		}},
	}

	const interval = 150 * time.Millisecond
	c := New(s, map[string]string{"client": clientAddr, "server": serverAddr}, WithInterval(interval))
	defer c.Close()

	tel := NewTelemetry(interval)
	defer tel.Close()
	rxSeen := make(chan struct{}, 1)
	tel.AddObserver(ObserverFunc(func(a Aggregate) {
		if a.RxBytes > 0 {
			select {
			case rxSeen <- struct{}{}:
			default:
			}
		}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go tel.Collect(ctx, c)

	if err := c.Run(ctx, time.Second); err != nil {
		t.Fatalf("controller Run: %v", err)
	}

	select {
	case <-rxSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("no aggregate with received bytes was observed")
	}
	c.Teardown(context.Background())
}

type emptySource struct{}

func (emptySource) Placed() []Placed { return nil }

// TestTelemetryAddObserverConcurrent: AddObserver must be safe to call while
// Collect/emit reads the observer slice, and Collect must return (joining its
// subscribers) after ctx is cancelled. Run with -race.
func TestTelemetryAddObserverConcurrent(t *testing.T) {
	tel := NewTelemetry(time.Millisecond)
	defer tel.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tel.Collect(ctx, emptySource{}); close(done) }()
	for i := 0; i < 200; i++ {
		tel.AddObserver(ObserverFunc(func(Aggregate) {}))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Collect did not return after cancel")
	}
}

func TestObserversRender(t *testing.T) {
	a := Aggregate{
		At:           time.Now(),
		TxBitsPerSec: 94e6,
		RxBitsPerSec: 88e6,
		Flows:        []FlowSample{{Role: Sender}, {Role: Receiver}},
	}
	var human, jsonOut bytes.Buffer
	NewTextObserver(&human).Observe(a)
	NewJSONObserver(&jsonOut).Observe(a)
	if !strings.Contains(human.String(), "Mbps") || !strings.Contains(human.String(), "2 flows") {
		t.Fatalf("text observer output: %q", human.String())
	}
	if !strings.Contains(jsonOut.String(), `"rx_bits_per_sec"`) {
		t.Fatalf("json observer output: %q", jsonOut.String())
	}
}

// TestObserverLabelsDirection: a labeled aggregate (a concurrent/bidir flow)
// renders its event and from→to direction so lines are distinguishable, while an
// unlabeled aggregate stays on the plain one-flow format.
func TestObserverLabelsDirection(t *testing.T) {
	labeled := Aggregate{
		At: time.Now(), Event: "down", From: "server", To: "client",
		TxBitsPerSec: 8e9, RxBitsPerSec: 7e9,
		Flows: []FlowSample{{Role: Sender}, {Role: Receiver}},
	}
	var human, jsonOut bytes.Buffer
	NewTextObserver(&human).Observe(labeled)
	NewJSONObserver(&jsonOut).Observe(labeled)
	if !strings.Contains(human.String(), "down") || !strings.Contains(human.String(), "server→client") {
		t.Fatalf("labeled line missing event/direction: %q", human.String())
	}
	if !strings.Contains(jsonOut.String(), `"event":"down"`) || !strings.Contains(jsonOut.String(), `"to":"client"`) {
		t.Fatalf("labeled json missing event/direction: %q", jsonOut.String())
	}

	var plain bytes.Buffer
	NewTextObserver(&plain).Observe(Aggregate{At: time.Now(), TxBitsPerSec: 1e6, Flows: []FlowSample{{Role: Sender}}})
	if strings.Contains(plain.String(), "→") {
		t.Fatalf("unlabeled line should have no direction arrow: %q", plain.String())
	}
}
