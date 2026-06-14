// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/control"
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
//
// Telemetry dials its own gRPC connections to each agent (keyed by control
// address), separate from the controller's control-plane connections, so the
// high-rate telemetry stream never contends with control RPCs (ADR-0013). Call
// Close to release those connections when collection is done.
type Telemetry struct {
	interval  time.Duration
	token     string
	mu        sync.Mutex
	latest    map[string]FlowSample
	observers []Observer

	connMu sync.Mutex
	conns  map[string]*grpc.ClientConn
	dialed map[string]loomv1.ControlClient
}

// TelemetryOption configures a Telemetry collector.
type TelemetryOption func(*Telemetry)

// WithTelemetryToken sets the control-plane token (ADR-0014) used when dialing
// agents for telemetry. An empty token is a no-op.
func WithTelemetryToken(token string) TelemetryOption {
	return func(t *Telemetry) { t.token = token }
}

// NewTelemetry returns a collector emitting aggregates every interval.
func NewTelemetry(interval time.Duration, opts ...TelemetryOption) *Telemetry {
	if interval <= 0 {
		interval = time.Second
	}
	t := &Telemetry{
		interval: interval,
		latest:   make(map[string]FlowSample),
		conns:    make(map[string]*grpc.ClientConn),
		dialed:   make(map[string]loomv1.ControlClient),
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// AddObserver registers o to receive aggregate snapshots. Call before Collect.
func (t *Telemetry) AddObserver(o Observer) { t.observers = append(t.observers, o) }

// Close releases all telemetry connections. It is safe to call once Collect has
// returned.
func (t *Telemetry) Close() {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	for _, conn := range t.conns {
		_ = conn.Close()
	}
	t.conns = make(map[string]*grpc.ClientConn)
	t.dialed = make(map[string]loomv1.ControlClient)
}

// clientFor returns a telemetry-dedicated control client for the agent at addr,
// dialing (and caching) a new connection on first use.
func (t *Telemetry) clientFor(addr string) (loomv1.ControlClient, error) {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	if cl, ok := t.dialed[addr]; ok {
		return cl, nil
	}
	cl, conn, err := control.Dial(addr, control.WithToken(t.token))
	if err != nil {
		return nil, err
	}
	t.conns[addr] = conn
	t.dialed[addr] = cl
	return cl, nil
}

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
	cl, err := t.clientFor(p.AgentAddr)
	if err != nil {
		return
	}
	stream, err := cl.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: p.FlowID})
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
