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

// FlowSample is the latest telemetry for one placed flow. Bytes/Packets are
// cumulative; Nanos is the agent's timestamp for that cumulative reading;
// BitsPerSec is the rate the controller computed for the most recent display
// interval (see emit).
type FlowSample struct {
	Event      string
	FlowID     string
	Role       Role
	Bytes      uint64
	Packets    uint64
	Nanos      int64
	BitsPerSec float64
}

// baseline is a per-flow rate anchor: the cumulative bytes and agent timestamp at
// the previous display tick. The next tick's rate is the delta from here.
type baseline struct {
	nanos int64
	bytes uint64
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
	latest    map[string]FlowSample // most recent cumulative reading per flow
	prev      map[string]baseline   // rate anchor advanced each display tick
	ended     map[string]bool       // flows whose telemetry stream has finished
	lastRate  map[string]float64    // last computed rate per flow (for snapshots)
	frozen    bool                  // when set, emit stops notifying observers
	observers []Observer

	connMu sync.Mutex
	conns  map[string]*grpc.ClientConn
	dialed map[string]loomv1.ControlClient

	wg sync.WaitGroup // tracks subscribe goroutines so Collect can join them
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
		prev:     make(map[string]baseline),
		ended:    make(map[string]bool),
		lastRate: make(map[string]float64),
		conns:    make(map[string]*grpc.ClientConn),
		dialed:   make(map[string]loomv1.ControlClient),
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// AddObserver registers o to receive aggregate snapshots. Safe to call
// concurrently with Collect; emit reads observers under the same lock.
func (t *Telemetry) AddObserver(o Observer) {
	t.mu.Lock()
	t.observers = append(t.observers, o)
	t.mu.Unlock()
}

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
// and emits an aggregate every interval until ctx is cancelled.
//
// Subscription is eager (a fast poll, decoupled from the display interval) so a
// flow's telemetry stream opens essentially at flow start — otherwise the first
// second or two of traffic would be missed and the run would show fewer lines
// than its duration. The display clock is a single ticker that starts on the
// first received sample and aligns subsequent ticks to it, so an N-second run at
// interval I shows ~N/I lines. There is no emit on cancel: the run's summary
// already reports the final totals, and a trailing zero-rate line would just be
// noise.
func (t *Telemetry) Collect(ctx context.Context, src placedSource) {
	subscribed := make(map[string]bool)
	sub := time.NewTicker(25 * time.Millisecond) // eager subscription poll
	defer sub.Stop()
	var emit *time.Ticker
	var emitC <-chan time.Time
	defer func() {
		if emit != nil {
			emit.Stop()
		}
	}()
	for {
		for _, p := range src.Placed() {
			if !subscribed[p.Key()] {
				subscribed[p.Key()] = true
				t.wg.Add(1)
				go t.subscribe(ctx, p)
			}
		}
		// Start the display clock only once data is flowing, so the first line is
		// real traffic and ticks line up with the agents' samples.
		if emitC == nil && t.hasData() {
			emit = time.NewTicker(t.interval)
			emitC = emit.C
		}
		select {
		case <-ctx.Done():
			t.wg.Wait() // join subscribers before returning
			return
		case <-sub.C:
			// loop to pick up newly placed flows
		case now := <-emitC:
			t.emit(now)
		}
	}
}

// hasData reports whether any flow has produced a sample yet.
func (t *Telemetry) hasData() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.latest) > 0
}

func (t *Telemetry) subscribe(ctx context.Context, p Placed) {
	defer t.wg.Done()
	defer func() { _ = recover() }() // a stream/codec panic must not crash the collector
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
			// The stream ended: the flow finished (or the agent went away). Mark it
			// ended so the run can stop once every source flow has finished. The rate
			// settles to 0 on its own — emit sees no newer sample and reports no
			// movement — while cumulative bytes remain for the totals.
			t.mu.Lock()
			t.ended[p.Key()] = true
			t.mu.Unlock()
			return
		}
		t.mu.Lock()
		key := p.Key()
		// Seed the rate anchor on the first sample so the first interval measures
		// from here, not from a zero baseline at the unix epoch.
		if _, ok := t.prev[key]; !ok {
			t.prev[key] = baseline{nanos: s.GetNanos(), bytes: s.GetBytes()}
		}
		t.latest[key] = FlowSample{
			Event: p.Event, FlowID: p.FlowID, Role: p.Role,
			Bytes: s.GetBytes(), Packets: s.GetPackets(), Nanos: s.GetNanos(),
		}
		t.mu.Unlock()
	}
}

// WaitSources blocks until every source flow (Sender/Requester) currently placed
// by src has finished streaming, or ctx is done. It returns true if all sources
// completed, false on ctx cancellation. A scenario with no bounded source flows
// (e.g. an all-receiver or end-of-test run) never completes on its own, so this
// waits for ctx.
func (t *Telemetry) WaitSources(ctx context.Context, src placedSource) bool {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		placed := src.Placed()
		sources, done := 0, 0
		t.mu.Lock()
		for _, p := range placed {
			if p.Role == Sender || p.Role == Requester {
				sources++
				if t.ended[p.Key()] {
					done++
				}
			}
		}
		t.mu.Unlock()
		if sources > 0 && done == sources {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

// emit computes each flow's rate for this display interval from the cumulative
// counters (using the agents' own timestamps, so the rate is accurate regardless
// of clock drift), advances the rate anchors, and notifies observers. This is the
// single clock that paces the display.
// Freeze stops emit from notifying observers, so no more live lines print. The
// collector keeps ingesting samples (for an accurate final Snapshot); this just
// ends the display once the run is over, keeping the line count equal to the
// run's duration/interval regardless of drain/teardown timing.
func (t *Telemetry) Freeze() {
	t.mu.Lock()
	t.frozen = true
	t.mu.Unlock()
}

func (t *Telemetry) emit(now time.Time) {
	t.mu.Lock()
	if t.frozen {
		t.mu.Unlock()
		return
	}
	agg := Aggregate{At: now, Flows: make([]FlowSample, 0, len(t.latest))}
	for key, fs := range t.latest {
		bps := 0.0
		if p, ok := t.prev[key]; ok && fs.Nanos > p.nanos && fs.Bytes >= p.bytes {
			if dt := float64(fs.Nanos-p.nanos) / 1e9; dt > 0 {
				bps = float64(fs.Bytes-p.bytes) * 8 / dt
			}
		}
		t.prev[key] = baseline{nanos: fs.Nanos, bytes: fs.Bytes} // advance the anchor
		t.lastRate[key] = bps
		fs.BitsPerSec = bps
		agg.Flows = append(agg.Flows, fs)
		if fs.Role == Receiver || fs.Role == Requester {
			agg.RxBitsPerSec += bps
			agg.RxBytes += fs.Bytes
		} else {
			agg.TxBitsPerSec += bps
			agg.TxBytes += fs.Bytes
		}
	}
	obs := t.observers
	t.mu.Unlock()
	for _, o := range obs {
		o.Observe(agg)
	}
}

// Snapshot returns the current cumulative totals without advancing the rate
// anchors or notifying observers — used for the end-of-run summary, which reports
// totals and averages over elapsed (not an instantaneous rate).
func (t *Telemetry) Snapshot() Aggregate {
	t.mu.Lock()
	defer t.mu.Unlock()
	agg := Aggregate{At: time.Now(), Flows: make([]FlowSample, 0, len(t.latest))}
	for key, fs := range t.latest {
		fs.BitsPerSec = t.lastRate[key]
		agg.Flows = append(agg.Flows, fs)
		if fs.Role == Receiver || fs.Role == Requester {
			agg.RxBytes += fs.Bytes
		} else {
			agg.TxBytes += fs.Bytes
		}
	}
	return agg
}
