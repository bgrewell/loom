// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package controller orchestrates a scenario across agents: it resolves the
// endpoints each event runs between, plans the timeline, and at each fire wires
// a receiver and a sender on the right agents and starts them. Telemetry
// aggregation is a follow-on (#35). See DESIGN.md §9/§11.
package controller

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/control"
	"github.com/bgrewell/loom/core/emul"
	"github.com/bgrewell/loom/core/scenario"
	"github.com/bgrewell/loom/core/selection"
	"github.com/bgrewell/loom/core/timeline"
	"github.com/bgrewell/loom/core/timesync"
	"github.com/bgrewell/loom/core/units"
)

// Role distinguishes the two flows a fire creates.
type Role int

// Flow roles. Sender/Receiver are the push model (one-directional); the
// Requester/Responder pair carries request/response emulations. For telemetry,
// the side that *receives* the download (Receiver, Requester) counts as Rx and
// the side that *sends* it (Sender, Responder) as Tx.
const (
	Sender Role = iota
	Receiver
	Responder
	Requester
)

// String renders a Role for display.
func (r Role) String() string {
	switch r {
	case Sender:
		return "sender"
	case Receiver:
		return "receiver"
	case Responder:
		return "responder"
	case Requester:
		return "requester"
	default:
		return "unknown"
	}
}

// Placed is one configured flow on an agent. FlowIDs are only unique per agent,
// so AgentAddr+FlowID is the global key.
type Placed struct {
	Agent     loomv1.ControlClient
	AgentAddr string
	FlowID    string
	Role      Role
	Event     string
}

// Key uniquely identifies a placed flow across agents.
func (p Placed) Key() string { return p.AgentAddr + "/" + p.FlowID }

// Dialer opens a control connection to an agent address, returning a client and
// a closer. Injectable (ADR-0022) so the controller is testable without real
// gRPC; the default dials over the network with the controller's token.
type Dialer func(addr string) (loomv1.ControlClient, io.Closer, error)

// Controller drives a scenario across agents addressed by endpoint name.
type Controller struct {
	s        *scenario.Scenario
	addrs    map[string]string // endpoint name -> agent control address
	token    string            // shared control-plane token (ADR-0014)
	dialer   Dialer
	rng      *rand.Rand
	interval time.Duration // reporting interval the agents anchor their samples to
	agents   map[string]loomv1.ControlClient
	closes   []func()

	mu     sync.Mutex
	placed []Placed
	sync   map[string]timesync.Sample // endpoint -> measured clock offset/delay
}

// Option configures a Controller.
type Option func(*Controller)

// WithToken sets the shared control-plane token presented to every agent
// (ADR-0014). An empty token is a no-op.
func WithToken(token string) Option {
	return func(c *Controller) { c.token = token }
}

// WithDialer overrides how the controller connects to agents (e.g. an in-process
// client in tests).
func WithDialer(d Dialer) Option {
	return func(c *Controller) { c.dialer = d }
}

// WithInterval sets the reporting interval the agents anchor their boundary
// samples to (matches loomctl's --interval). Zero leaves agents on their legacy
// free-running cadence.
func WithInterval(d time.Duration) Option {
	return func(c *Controller) { c.interval = d }
}

// New returns a Controller for s, with addrs mapping each endpoint name to its
// agent's control address.
func New(s *scenario.Scenario, addrs map[string]string, opts ...Option) *Controller {
	c := &Controller{
		s:      s,
		addrs:  addrs,
		rng:    rand.New(rand.NewSource(s.Seed)),
		agents: make(map[string]loomv1.ControlClient),
		sync:   make(map[string]timesync.Sample),
	}
	for _, o := range opts {
		o(c)
	}
	if c.interval <= 0 {
		c.interval = time.Second // agents anchor boundary samples to this
	}
	if c.dialer == nil {
		c.dialer = func(addr string) (loomv1.ControlClient, io.Closer, error) {
			return control.Dial(addr, control.WithToken(c.token))
		}
	}
	return c
}

// Token returns the controller's control-plane token, so the telemetry collector
// can authenticate with the same credential.
func (c *Controller) Token() string { return c.token }

// Run plans the timeline and drives every fire until ctx is cancelled or the
// timeline completes within horizon.
func (c *Controller) Run(ctx context.Context, horizon time.Duration) error {
	fires, err := timeline.Plan(c.s, horizon)
	if err != nil {
		return err
	}
	events := make(map[string]scenario.Event, len(c.s.Timeline))
	for _, e := range c.s.Timeline {
		events[e.Name] = e
	}

	// timeline.Run calls onFire sequentially, so firstErr needs no lock. Each
	// fire is wrapped so a panic in one placement is captured as an error rather
	// than tearing down the whole run.
	var firstErr error
	timeline.Run(ctx, fires, time.Now(), func(f timeline.Fire) {
		err := c.fireSafe(ctx, events[f.Event])
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	return firstErr
}

// Placed returns the flows configured so far.
func (c *Controller) Placed() []Placed {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Placed(nil), c.placed...)
}

// Teardown destroys every placed flow.
func (c *Controller) Teardown(ctx context.Context) {
	for _, p := range c.Placed() {
		_, _ = p.Agent.Destroy(ctx, &loomv1.DestroyRequest{FlowId: p.FlowID})
	}
}

// Close closes all dialed agent connections.
func (c *Controller) Close() {
	for _, f := range c.closes {
		f()
	}
}

// fireSafe runs fire, converting a panic into an error so one bad event
// placement cannot crash the controller.
func (c *Controller) fireSafe(ctx context.Context, ev scenario.Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("event %q: panic: %v", ev.Name, r)
		}
	}()
	return c.fire(ctx, ev)
}

func (c *Controller) fire(ctx context.Context, ev scenario.Event) error {
	fromPool, err := selection.Resolve(ev.From, c.s.Endpoints)
	if err != nil {
		return fmt.Errorf("event %q from: %w", ev.Name, err)
	}
	toPool, err := selection.Resolve(ev.To, c.s.Endpoints)
	if err != nil {
		return fmt.Errorf("event %q to: %w", ev.Name, err)
	}
	from, ok := selection.Pick(fromPool, "", c.rng)
	if !ok {
		return fmt.Errorf("event %q: no source endpoint", ev.Name)
	}
	to, ok := selection.Pick(toPool, from.Name, c.rng)
	if !ok {
		return fmt.Errorf("event %q: no destination endpoint (after excluding %q)", ev.Name, from.Name)
	}

	fromAgent, fromAddr, err := c.agentFor(from.Name)
	if err != nil {
		return err
	}
	toAgent, toAddr, err := c.agentFor(to.Name)
	if err != nil {
		return err
	}

	// Request/response emulations (e.g. https-browse) need a responder/requester
	// pair over a real connection, not the one-directional sender/receiver pair.
	if emul.Has(ev.Flow.Kind) && emul.ModeOf(ev.Flow.Kind) == emul.ModeRequestResponse {
		return c.fireRequestResponse(ctx, ev, from, to, fromAgent, fromAddr, toAgent, toAddr)
	}

	dp := ev.Datapath
	if dp == "" {
		dp = "udp"
	}

	// Configure the receiver first (its ephemeral port targets the sender). A UDP
	// listener returns a data port; a NIC-bound datapath (afxdp) uses iface/queue.
	rxCfg, err := toAgent.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RECEIVER, Datapath: dp,
		PacketSize: int32ToU32(packetSize(ev)), Iface: to.Iface, Queue: int32ToU32(to.Queue),
	}})
	if err != nil {
		return fmt.Errorf("event %q: configure receiver: %w", ev.Name, err)
	}

	// Sender on the source agent. Socket datapaths target the receiver's data
	// address; NIC-bound datapaths (afxdp, raw L2) ignore it and use the iface.
	var target string
	if dp != "afxdp" {
		dataHost := to.Address
		if dataHost == "" {
			dataHost = hostOf(toAddr)
		}
		target = net.JoinHostPort(dataHost, strconv.Itoa(int(rxCfg.GetDataPort())))
	}
	txCfg, err := fromAgent.Configure(ctx, &loomv1.ConfigureRequest{Flow: senderSpec(ev, dp, target, from, c.s.Seed)})
	if err != nil {
		return fmt.Errorf("event %q: configure sender: %w", ev.Name, err)
	}

	// Both ends open at the same gate, so their interval boundaries (and thus the
	// controller's per-interval consolidation) line up. The receiver's socket is
	// bound at Configure; gating its drain to T is safe because no traffic flows
	// before the sender's gate anyway.
	gate := c.startGate()
	if _, err := toAgent.Start(ctx, c.startReq(rxCfg.GetFlowId(), to.Name, gate)); err != nil {
		return fmt.Errorf("event %q: start receiver: %w", ev.Name, err)
	}
	c.track(toAgent, toAddr, rxCfg.GetFlowId(), Receiver, ev.Name)
	if _, err := fromAgent.Start(ctx, c.startReq(txCfg.GetFlowId(), from.Name, gate)); err != nil {
		return fmt.Errorf("event %q: start sender: %w", ev.Name, err)
	}
	c.track(fromAgent, fromAddr, txCfg.GetFlowId(), Sender, ev.Name)
	return nil
}

// fireRequestResponse places a request/response emulation: a responder on the
// destination agent (binding an ephemeral port) and a requester on the source
// agent that dials it and drives the behavior script. The download bytes flow
// responder→requester over the chosen transport.
func (c *Controller) fireRequestResponse(ctx context.Context, ev scenario.Event, from, to scenario.Endpoint, fromAgent loomv1.ControlClient, fromAddr string, toAgent loomv1.ControlClient, toAddr string) error {
	transport := emul.DefaultTransport(ev.Flow.Kind)
	if v, ok := ev.Flow.Params["transport"]; ok {
		if s := fmt.Sprint(v); s != "" {
			transport = s
		}
	}

	// Responder on the destination agent; it returns its ephemeral data port. It
	// must be accepting before the requester dials (at Configure), so it starts
	// immediately (no gate) — its telemetry still anchors to first traffic, which
	// only arrives once the requester begins at the gate.
	respCfg, err := toAgent.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RESPONDER, Transport: transport,
		PacketSize: int32ToU32(packetSize(ev)),
	}})
	if err != nil {
		return fmt.Errorf("event %q: configure responder: %w", ev.Name, err)
	}
	if _, err := toAgent.Start(ctx, c.startReq(respCfg.GetFlowId(), to.Name, time.Time{})); err != nil {
		return fmt.Errorf("event %q: start responder: %w", ev.Name, err)
	}
	c.track(toAgent, toAddr, respCfg.GetFlowId(), Responder, ev.Name)

	// Requester on the source agent, dialing the responder's data address.
	dataHost := to.Address
	if dataHost == "" {
		dataHost = hostOf(toAddr)
	}
	target := net.JoinHostPort(dataHost, strconv.Itoa(int(respCfg.GetDataPort())))
	gate := c.startGate()
	reqCfg, err := fromAgent.Configure(ctx, &loomv1.ConfigureRequest{Flow: requesterSpec(ev, transport, target, c.s.Seed)})
	if err != nil {
		return fmt.Errorf("event %q: configure requester: %w", ev.Name, err)
	}
	if _, err := fromAgent.Start(ctx, c.startReq(reqCfg.GetFlowId(), from.Name, gate)); err != nil {
		return fmt.Errorf("event %q: start requester: %w", ev.Name, err)
	}
	c.track(fromAgent, fromAddr, reqCfg.GetFlowId(), Requester, ev.Name)
	return nil
}

// startReq builds a Start request that opens flowID at the shared gate (translated
// into endpoint's clock) and tells the agent the reporting interval to anchor its
// boundary samples to. A zero gate means "start immediately" (responders).
func (c *Controller) startReq(flowID, endpoint string, gate time.Time) *loomv1.StartRequest {
	return &loomv1.StartRequest{
		FlowId:              flowID,
		StartAtUnixNanos:    c.startAtFor(endpoint, gate),
		ReportIntervalNanos: c.interval.Nanoseconds(),
	}
}

func (c *Controller) agentFor(endpoint string) (loomv1.ControlClient, string, error) {
	addr, ok := c.addrs[endpoint]
	if !ok {
		return nil, "", fmt.Errorf("no agent address for endpoint %q", endpoint)
	}
	if cl, ok := c.agents[addr]; ok {
		return cl, addr, nil
	}
	cl, closer, err := c.dialer(addr)
	if err != nil {
		return nil, "", err
	}
	c.agents[addr] = cl
	c.closes = append(c.closes, func() { _ = closer.Close() })
	return cl, addr, nil
}

// startGate returns a shared start time (on the controller's clock) far enough in
// the future that a Start RPC reaches every agent before the gate opens. The
// slack scales with the slowest measured round-trip delay so even high-latency
// links stay in lockstep; a floor covers RPC/processing on fast links.
func (c *Controller) startGate() time.Time {
	var maxDelay time.Duration
	c.mu.Lock()
	for _, s := range c.sync {
		if s.Delay > maxDelay {
			maxDelay = s.Delay
		}
	}
	c.mu.Unlock()
	return time.Now().Add(100*time.Millisecond + maxDelay)
}

// startAtFor translates a controller-clock gate time into endpoint's agent clock
// using the measured TimeSync offset, returning unix nanoseconds (0 if the gate
// is zero, meaning "start immediately").
func (c *Controller) startAtFor(endpoint string, gate time.Time) int64 {
	if gate.IsZero() {
		return 0
	}
	c.mu.Lock()
	off := c.sync[endpoint].Offset
	c.mu.Unlock()
	return gate.Add(off).UnixNano()
}

func (c *Controller) track(agent loomv1.ControlClient, addr, id string, role Role, event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.placed = append(c.placed, Placed{Agent: agent, AgentAddr: addr, FlowID: id, Role: role, Event: event})
}

// senderSpec builds the sender's FlowSpec from an event, the chosen datapath,
// the receiver target (socket datapaths), and the source endpoint (iface/queue
// for NIC-bound datapaths). If the event's flow kind names an emulation, the spec
// carries it (and its params) so the agent runs the behavior engine.
func senderSpec(ev scenario.Event, dp, target string, from scenario.Endpoint, seed int64) *loomv1.FlowSpec {
	spec := &loomv1.FlowSpec{
		Role:       loomv1.FlowRole_FLOW_ROLE_SENDER,
		Datapath:   dp,
		Target:     target,
		Generator:  "stream",
		PacketSize: int32ToU32(packetSize(ev)),
		Iface:      from.Iface,
		Queue:      int32ToU32(from.Queue),
	}
	if emul.Has(ev.Flow.Kind) {
		spec.Emulation = ev.Flow.Kind
		spec.Params = stringParams(ev.Flow.Params)
		spec.Seed = seed
	}
	if v, ok := ev.Flow.Params["rate"]; ok {
		spec.Rate = fmt.Sprint(v)
	}
	switch {
	case ev.Stop.After > 0:
		spec.Duration = durationpb.New(ev.Stop.After)
	case spec.Emulation != "":
		// Convenience: emulations may set a `duration` knob in the flow block
		// instead of a stop.after — e.g. a voip-call's call length.
		if v, ok := ev.Flow.Params["duration"]; ok {
			if d, err := units.ParseDuration(fmt.Sprint(v)); err == nil {
				spec.Duration = durationpb.New(d)
			}
		}
	}
	spec.Count = ev.Stop.Count
	spec.Volume = ev.Stop.Volume
	return spec
}

// requesterSpec builds the requester's FlowSpec for a request/response emulation:
// the emulation name + params (compiled to a script on the agent), the transport,
// and the responder target. Stop conditions mirror senderSpec.
func requesterSpec(ev scenario.Event, transport, target string, seed int64) *loomv1.FlowSpec {
	spec := &loomv1.FlowSpec{
		Role:       loomv1.FlowRole_FLOW_ROLE_REQUESTER,
		Transport:  transport,
		Target:     target,
		Emulation:  ev.Flow.Kind,
		Params:     stringParams(ev.Flow.Params),
		Seed:       seed,
		PacketSize: int32ToU32(packetSize(ev)),
	}
	switch {
	case ev.Stop.After > 0:
		spec.Duration = durationpb.New(ev.Stop.After)
	default:
		// Convenience: an emulation may carry its own `duration` knob instead of a
		// stop.after (mirrors senderSpec).
		if v, ok := ev.Flow.Params["duration"]; ok {
			if d, err := units.ParseDuration(fmt.Sprint(v)); err == nil {
				spec.Duration = durationpb.New(d)
			}
		}
	}
	spec.Count = ev.Stop.Count
	spec.Volume = ev.Stop.Volume
	return spec
}

// stringParams stringifies a scenario flow's params for the emulation engine.
func stringParams(p map[string]any) map[string]string {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func packetSize(ev scenario.Event) int {
	if v, ok := ev.Flow.Params["packet_size"]; ok {
		if n, err := strconv.Atoi(fmt.Sprint(v)); err == nil {
			return n
		}
	}
	return 1400
}

func int32ToU32(n int) uint32 {
	if n < 0 {
		return 0
	}
	return uint32(n)
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
