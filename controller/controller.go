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
	"github.com/bgrewell/loom/core/units"
)

// Role distinguishes the two flows a fire creates.
type Role int

// Flow roles.
const (
	Sender Role = iota
	Receiver
)

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
	s      *scenario.Scenario
	addrs  map[string]string // endpoint name -> agent control address
	token  string            // shared control-plane token (ADR-0014)
	dialer Dialer
	rng    *rand.Rand
	agents map[string]loomv1.ControlClient
	closes []func()

	mu     sync.Mutex
	placed []Placed
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

// New returns a Controller for s, with addrs mapping each endpoint name to its
// agent's control address.
func New(s *scenario.Scenario, addrs map[string]string, opts ...Option) *Controller {
	c := &Controller{
		s:      s,
		addrs:  addrs,
		rng:    rand.New(rand.NewSource(s.Seed)),
		agents: make(map[string]loomv1.ControlClient),
	}
	for _, o := range opts {
		o(c)
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

	dp := ev.Datapath
	if dp == "" {
		dp = "udp"
	}

	// Receiver on the destination agent. A UDP listener returns an ephemeral
	// port; a NIC-bound datapath (afxdp) uses the endpoint's iface/queue.
	rxCfg, err := toAgent.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RECEIVER, Datapath: dp,
		PacketSize: int32ToU32(packetSize(ev)), Iface: to.Iface, Queue: int32ToU32(to.Queue),
	}})
	if err != nil {
		return fmt.Errorf("event %q: configure receiver: %w", ev.Name, err)
	}
	if _, err := toAgent.Start(ctx, &loomv1.StartRequest{FlowId: rxCfg.GetFlowId()}); err != nil {
		return fmt.Errorf("event %q: start receiver: %w", ev.Name, err)
	}
	c.track(toAgent, toAddr, rxCfg.GetFlowId(), Receiver, ev.Name)

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
	if _, err := fromAgent.Start(ctx, &loomv1.StartRequest{FlowId: txCfg.GetFlowId()}); err != nil {
		return fmt.Errorf("event %q: start sender: %w", ev.Name, err)
	}
	c.track(fromAgent, fromAddr, txCfg.GetFlowId(), Sender, ev.Name)
	return nil
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
