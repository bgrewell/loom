// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"sync"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// FlowSample is the latest telemetry for one placed flow.
type FlowSample struct {
	Event      string
	FlowID     string
	Role       Role
	Bytes      uint64
	Packets    uint64
	BitsPerSec float64
}

// Aggregate is a fleet-wide telemetry snapshot at an instant: tx (senders) and
// rx (receivers) rolled up, plus the per-flow detail.
type Aggregate struct {
	At           time.Time
	TxBitsPerSec float64
	RxBitsPerSec float64
	TxBytes      uint64
	RxBytes      uint64
	Flows        []FlowSample
}

// Observer receives aggregate telemetry snapshots in real time. The CLI is one
// observer; a websocket/SSE API for live dashboards is just another Observer.
type Observer interface {
	Observe(Aggregate)
}

// ObserverFunc adapts a function to an Observer.
type ObserverFunc func(Aggregate)

// Observe calls f.
func (f ObserverFunc) Observe(a Aggregate) { f(a) }

// placedSource yields the flows to collect from (the Controller satisfies it).
type placedSource interface{ Placed() []Placed }

// Telemetry subscribes to placed flows' telemetry streams, aggregates them, and
// pushes snapshots to its observers on an interval — the realtime path.
type Telemetry struct {
	interval  time.Duration
	mu        sync.Mutex
	latest    map[string]FlowSample
	observers []Observer
}

// NewTelemetry returns a collector emitting aggregates every interval.
func NewTelemetry(interval time.Duration) *Telemetry {
	if interval <= 0 {
		interval = time.Second
	}
	return &Telemetry{interval: interval, latest: make(map[string]FlowSample)}
}

// AddObserver registers o to receive aggregate snapshots. Call before Collect.
func (t *Telemetry) AddObserver(o Observer) { t.observers = append(t.observers, o) }

// Collect subscribes to every flow src has placed (including ones placed later)
// and emits aggregate snapshots until ctx is cancelled.
func (t *Telemetry) Collect(ctx context.Context, src placedSource) {
	subscribed := make(map[string]bool)
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		for _, p := range src.Placed() {
			if !subscribed[p.Key()] {
				subscribed[p.Key()] = true
				go t.subscribe(ctx, p)
			}
		}
		select {
		case <-ctx.Done():
			t.emit(time.Now())
			return
		case now := <-ticker.C:
			t.emit(now)
		}
	}
}

func (t *Telemetry) subscribe(ctx context.Context, p Placed) {
	stream, err := p.Agent.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: p.FlowID})
	if err != nil {
		return
	}
	for {
		s, err := stream.Recv()
		if err != nil {
			return
		}
		t.mu.Lock()
		t.latest[p.Key()] = FlowSample{
			Event: p.Event, FlowID: p.FlowID, Role: p.Role,
			Bytes: s.GetBytes(), Packets: s.GetPackets(), BitsPerSec: s.GetBitsPerSec(),
		}
		t.mu.Unlock()
	}
}

func (t *Telemetry) emit(now time.Time) {
	t.mu.Lock()
	agg := Aggregate{At: now, Flows: make([]FlowSample, 0, len(t.latest))}
	for _, fs := range t.latest {
		agg.Flows = append(agg.Flows, fs)
		if fs.Role == Receiver {
			agg.RxBitsPerSec += fs.BitsPerSec
			agg.RxBytes += fs.Bytes
		} else {
			agg.TxBitsPerSec += fs.BitsPerSec
			agg.TxBytes += fs.Bytes
		}
	}
	obs := t.observers
	t.mu.Unlock()
	for _, o := range obs {
		o.Observe(agg)
	}
}
