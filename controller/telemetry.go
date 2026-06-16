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

// FlowSample is one flow's contribution to an aggregate. For a live interval line,
// Bytes/Packets are that interval's delta and BitsPerSec is its rate; for the
// end-of-run Snapshot they are cumulative.
type FlowSample struct {
	Event      string
	FlowID     string
	Role       Role
	From       string
	To         string
	Datapath   string
	Bytes      uint64
	Packets    uint64
	BitsPerSec float64
	TCP        *TCPStats // sender-side TCP_INFO, nil for non-TCP / receiver flows
}

// TCPStats is a sender socket's TCP_INFO snapshot, surfaced for link profiling.
type TCPStats struct {
	Retrans  uint32
	Lost     uint32
	RttUs    uint32
	RttvarUs uint32
	Cwnd     uint32
	Ssthresh uint32
}

// Aggregate is a consolidated telemetry line. For a live interval it carries that
// interval's tx/rx deltas, rates, the interval Index, and how many of the event's
// flows contributed (Sources of Expected; Complete when all reported). Event and
// From/To name the directional stream the line belongs to, so concurrent flows
// (bidir, N-way) render as distinguishable, labeled lines. For the end-of-run
// Snapshot, Tx/RxBytes are cumulative totals.
type Aggregate struct {
	At           time.Time
	Index        int64
	Event        string
	From         string
	To           string
	TxBitsPerSec float64
	RxBitsPerSec float64
	TxBytes      uint64
	RxBytes      uint64
	TxPackets    uint64
	RxPackets    uint64
	Sources      int
	Expected     int
	Complete     bool
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

// bucketKey identifies one interval of one event.
type bucketKey struct {
	event string
	index int64
}

// bucket accumulates the interval-k deltas reported by an event's flows, so the
// controller can emit a consolidated line once all of them have reported (or a
// lateness bound passes). It is the heart of the watermark aggregation: the
// interval clock lives at the agents; here we only sum what they report.
type bucket struct {
	txBytes, rxBytes uint64
	txPkts, rxPkts   uint64
	nanos            int64                 // interval elapsed ns (rate basis)
	reported         map[string]bool       // flow keys that reported this interval
	flows            map[string]FlowSample // per-flow delta + rate (for --per-flow)
	firstSeen        time.Time             // when this bucket got its first report
	hasSource        bool                  // a source (sender/requester) reported it
}

// Telemetry subscribes to placed flows' telemetry streams and consolidates their
// interval reports into aggregate lines — the realtime path. Each agent owns its
// interval clock (anchored to the scheduled-start gate) and reports per-interval
// deltas; this collector is a pure summer keyed by interval index, emitting a line
// once every contributor has reported the interval or a lateness bound elapses.
//
// Telemetry dials its own gRPC connections to each agent (separate from the
// control-plane connections) so the high-rate stream never contends with control
// RPCs (ADR-0013). Call Close to release them when collection is done.
type Telemetry struct {
	interval time.Duration
	token    string

	mu         sync.Mutex
	latest     map[string]FlowSample // cumulative per flow (for Snapshot/summary)
	ended      map[string]bool       // flows whose telemetry stream finished
	buckets    map[bucketKey]*bucket // pending interval accumulators
	nextIndex  map[string]int64      // next interval index to emit, per event
	events     map[string]bool       // events seen (to iterate for emission)
	incomplete bool                  // a live interval was emitted before all reported
	observers  []Observer
	src        placedSource // set in Collect; used to count expected flows per event

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

// NewTelemetry returns a collector that consolidates interval reports. interval is
// the reporting interval the agents anchor to; it is used here only as the
// lateness grace bound (the cadence itself comes from the agents).
func NewTelemetry(interval time.Duration, opts ...TelemetryOption) *Telemetry {
	if interval <= 0 {
		interval = time.Second
	}
	t := &Telemetry{
		interval:  interval,
		latest:    make(map[string]FlowSample),
		ended:     make(map[string]bool),
		buckets:   make(map[bucketKey]*bucket),
		nextIndex: make(map[string]int64),
		events:    make(map[string]bool),
		conns:     make(map[string]*grpc.ClientConn),
		dialed:    make(map[string]loomv1.ControlClient),
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// AddObserver registers o to receive aggregate snapshots.
func (t *Telemetry) AddObserver(o Observer) {
	t.mu.Lock()
	t.observers = append(t.observers, o)
	t.mu.Unlock()
}

// Close releases all telemetry connections. Safe once Collect has returned.
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

// Collect subscribes to every flow src has placed (eagerly, so a stream opens at
// flow start) and emits consolidated interval lines as they complete, until ctx is
// cancelled. A slow flush ticker applies the lateness bound for stragglers; there
// is no display clock — emission is driven by the agents' interval reports.
func (t *Telemetry) Collect(ctx context.Context, src placedSource) {
	t.mu.Lock()
	t.src = src
	t.mu.Unlock()

	subscribed := make(map[string]bool)
	sub := time.NewTicker(25 * time.Millisecond) // eager subscription poll
	defer sub.Stop()
	flush := time.NewTicker(t.interval / 4) // lateness flush for stragglers
	defer flush.Stop()
	for {
		for _, p := range src.Placed() {
			if !subscribed[p.Key()] {
				subscribed[p.Key()] = true
				t.wg.Add(1)
				go t.subscribe(ctx, p)
			}
		}
		select {
		case <-ctx.Done():
			t.wg.Wait() // join subscribers before returning
			return
		case <-sub.C:
			// loop to pick up newly placed flows
		case <-flush.C:
			t.tryEmit(time.Now())
		}
	}
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
	key := p.Key()
	for {
		s, err := stream.Recv()
		if err != nil {
			t.mu.Lock()
			t.ended[key] = true
			t.mu.Unlock()
			return
		}
		t.mu.Lock()
		// Cumulative, for the end-of-run summary.
		t.latest[key] = FlowSample{
			Event: p.Event, FlowID: p.FlowID, Role: p.Role, From: p.From, To: p.To, Datapath: p.Datapath,
			Bytes: s.GetBytes(), Packets: s.GetPackets(), TCP: tcpStatsOf(s.GetTcp()),
		}
		// Fold a full interval's delta into its bucket. The final (trailing partial)
		// sample carries index -1 and is accounted only in the cumulative totals.
		if !s.GetFinal() && s.GetIntervalIndex() >= 0 {
			t.foldLocked(p, s)
		}
		t.mu.Unlock()
		t.tryEmit(time.Now())
	}
}

// tcpStatsOf converts a proto TcpInfo (nil for non-TCP samples) to a TCPStats.
func tcpStatsOf(ti *loomv1.TcpInfo) *TCPStats {
	if ti == nil {
		return nil
	}
	return &TCPStats{
		Retrans: ti.GetTotalRetrans(), Lost: ti.GetLost(),
		RttUs: ti.GetRttUs(), RttvarUs: ti.GetRttvarUs(),
		Cwnd: ti.GetSndCwnd(), Ssthresh: ti.GetSndSsthresh(),
	}
}

// foldLocked adds one flow's interval-k delta to its bucket. Caller holds t.mu.
func (t *Telemetry) foldLocked(p Placed, s *loomv1.TelemetrySample) {
	idx := s.GetIntervalIndex()
	if idx < t.nextIndex[p.Event] {
		// This interval was already emitted (a straggler arriving after the line
		// went out). Its bytes are still in the cumulative total via latest; don't
		// resurrect a bucket the emit loop will never revisit (that would leak and,
		// for a receiver-only straggler, be dropped anyway).
		return
	}
	bk := bucketKey{event: p.Event, index: idx}
	b := t.buckets[bk]
	if b == nil {
		b = &bucket{reported: make(map[string]bool), flows: make(map[string]FlowSample), firstSeen: time.Now()}
		t.buckets[bk] = b
		t.events[p.Event] = true
	}
	rx := p.Role == Receiver || p.Role == Requester
	if rx {
		b.rxBytes += s.GetIntervalBytes()
		b.rxPkts += s.GetIntervalPackets()
	} else {
		b.txBytes += s.GetIntervalBytes()
		b.txPkts += s.GetIntervalPackets()
	}
	if p.Role == Sender || p.Role == Requester {
		b.hasSource = true // a driving flow reported this interval
	}
	if s.GetIntervalNanos() > b.nanos {
		b.nanos = s.GetIntervalNanos()
	}
	b.reported[p.Key()] = true
	b.flows[p.Key()] = FlowSample{
		Event: p.Event, FlowID: p.FlowID, Role: p.Role, From: p.From, To: p.To,
		Bytes: s.GetIntervalBytes(), Packets: s.GetIntervalPackets(),
		BitsPerSec: bitsPerNanos(s.GetIntervalBytes(), s.GetIntervalNanos()),
	}
}

// eventMeta describes an event's placed flows: how many to expect, their global
// keys (to test against ended/reported), and the directional endpoints for the
// line label.
type eventMeta struct {
	expected int
	flowKeys []string
	from, to string
}

// tryEmit flushes every event's pending intervals in index order. An interval is
// emitted once every placed flow has *settled* it — reported the interval or ended
// its stream (so it never will) — which is the watermark: we wait for a slow but
// live receiver instead of flushing it out as rx 0. A generous backstop still
// flushes an interval whose contributor went silent without ending, so one stuck
// node can't stall the live view forever; such a line carries Sources<Expected so
// the display flags it, and the summary remains authoritative.
func (t *Telemetry) tryEmit(now time.Time) {
	meta := t.eventMetaByEvent()
	backstop := t.latenessBackstop()

	t.mu.Lock()
	var out []Aggregate
	for event := range t.events {
		em := meta[event]
		for {
			k := t.nextIndex[event]
			b, ok := t.buckets[bucketKey{event, k}]
			if !ok {
				break
			}
			settled := 0
			for _, key := range em.flowKeys {
				if b.reported[key] || t.ended[key] {
					settled++
				}
			}
			complete := em.expected > 0 && len(b.reported) >= em.expected
			allSettled := em.expected > 0 && settled >= em.expected
			stuck := now.Sub(b.firstSeen) >= backstop
			if !complete && !allSettled && !stuck {
				break
			}
			delete(t.buckets, bucketKey{event, k})
			t.nextIndex[event] = k + 1
			if !b.hasSource {
				// A tail interval reported only by a receiver/responder after its
				// source flow ended — trailing drain bytes, counted in the summary,
				// not surfaced as a live line.
				continue
			}
			if !complete {
				t.incomplete = true
			}
			a := aggFromBucket(now, k, b, em.expected, complete)
			a.Event, a.From, a.To = event, em.from, em.to
			out = append(out, a)
		}
	}
	obs := t.observers
	t.mu.Unlock()

	for _, a := range out {
		for _, o := range obs {
			o.Observe(a)
		}
	}
}

// latenessBackstop is how long an interval waits for a contributor that has
// neither reported nor ended before it is flushed incomplete. It is deliberately
// several intervals: a live-but-slow receiver should be waited for (the watermark),
// not flushed to rx 0; the backstop only rescues the live view from a node that
// has gone silent mid-stream without its telemetry stream ending.
func (t *Telemetry) latenessBackstop() time.Duration {
	if d := 3 * t.interval; d > 2*time.Second {
		return d
	}
	return 2 * time.Second
}

// eventMetaByEvent gathers each event's placed flow keys and directional endpoints.
// Computed outside t.mu (Placed takes the controller's lock).
func (t *Telemetry) eventMetaByEvent() map[string]eventMeta {
	t.mu.Lock()
	src := t.src
	t.mu.Unlock()
	out := make(map[string]eventMeta)
	if src == nil {
		return out
	}
	for _, p := range src.Placed() {
		em := out[p.Event]
		em.expected++
		em.flowKeys = append(em.flowKeys, p.Key())
		if em.from == "" {
			em.from, em.to = p.From, p.To
		}
		out[p.Event] = em
	}
	return out
}

func aggFromBucket(now time.Time, index int64, b *bucket, expected int, complete bool) Aggregate {
	a := Aggregate{
		At: now, Index: index,
		TxBytes: b.txBytes, RxBytes: b.rxBytes,
		TxBitsPerSec: bitsPerNanos(b.txBytes, b.nanos),
		RxBitsPerSec: bitsPerNanos(b.rxBytes, b.nanos),
		Sources:      len(b.reported),
		Expected:     expected,
		Complete:     complete,
	}
	for _, fs := range b.flows {
		a.Flows = append(a.Flows, fs)
	}
	return a
}

// bitsPerNanos converts a byte delta over an elapsed-ns window to bits/sec.
func bitsPerNanos(bytes uint64, nanos int64) float64 {
	if nanos <= 0 {
		return 0
	}
	return float64(bytes) * 8 / (float64(nanos) / 1e9)
}

// WaitSources blocks until every source flow (Sender/Requester) currently placed
// by src has finished streaming, or ctx is done. Returns true if all sources
// completed, false on ctx cancellation. A scenario with no bounded source flows
// (end-of-test) never completes on its own, so this waits for ctx.
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

// Snapshot returns the cumulative totals for the end-of-run summary.
func (t *Telemetry) Snapshot() Aggregate {
	t.mu.Lock()
	defer t.mu.Unlock()
	agg := Aggregate{At: time.Now(), Flows: make([]FlowSample, 0, len(t.latest))}
	for _, fs := range t.latest {
		agg.Flows = append(agg.Flows, fs)
		if fs.Role == Receiver || fs.Role == Requester {
			agg.RxBytes += fs.Bytes
			agg.RxPackets += fs.Packets
		} else {
			agg.TxBytes += fs.Bytes
			agg.TxPackets += fs.Packets
		}
	}
	return agg
}

// LiveIncomplete reports whether any live interval was emitted before all of its
// event's flows had reported — i.e. the live view was momentarily missing a node,
// so the final summary is the authoritative account.
func (t *Telemetry) LiveIncomplete() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.incomplete
}
