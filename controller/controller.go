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
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/scenario"
	"github.com/bgrewell/loom/core/selection"
	"github.com/bgrewell/loom/core/timeline"
	"github.com/bgrewell/loom/core/timesync"
	"github.com/bgrewell/loom/core/units"
)

// afxdpDataPort is the nominal UDP destination port stamped into AF_XDP frames.
// The receiver's AF_XDP socket captures every frame on its queue regardless of
// port, so this only needs to be a valid, consistent value.
const afxdpDataPort = 9999

// Role distinguishes the two flows a fire creates.
type Role int

// Flow roles. Sender/Receiver are the push model (one-directional); the
// Requester/Responder pair carries request/response emulations; the
// AppServer/AppClient pair carries real application protocol engines
// (core/app: voip, http, video). For telemetry, the side that *receives* the
// download (Receiver, Requester — the driving end) counts as Rx and the side
// that *sends* it (Sender, Responder) as Tx, so tx−rx is loss. App ends are
// different: an app engine's Counters cover BOTH directions of its media
// plane, so AppClient/AppServer bytes are per-end totals — folded under the
// rx/tx buckets for display (client end = rx bucket, server end = tx bucket)
// but never comparable as sender-vs-receiver loss; app-layer loss comes from
// the engines' own AppMetrics.
const (
	Sender Role = iota
	Receiver
	Responder
	Requester
	AppServer
	AppClient
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
	case AppServer:
		return "app-server"
	case AppClient:
		return "app-client"
	default:
		return "unknown"
	}
}

// Placed is one configured flow on an agent. FlowIDs are only unique per agent,
// so AgentAddr+FlowID is the global key. From/To are the event's source and
// destination endpoint names, so telemetry can label a consolidated line with its
// direction (e.g. "client→server") — essential once a scenario runs several
// concurrent flows (bidir, N-way) whose lines would otherwise be indistinguishable.
type Placed struct {
	Agent     loomv1.ControlClient
	AgentAddr string
	FlowID    string
	Role      Role
	Event     string
	From      string
	To        string
	Datapath  string // udp|tcp|afxdp (or the reqresp transport, or the app name) — for loss accounting
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
	caps     map[string]agentCaps // agent addr -> cached Capabilities + version (skew gate)
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
		caps:   make(map[string]agentCaps),
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

	// Application kinds (voip, and http/video once those engines land) place a
	// real protocol engine pair: an app server on the destination agent and an
	// app client on the source agent (core/app, FLOW_ROLE_APP_*).
	if isAppKind(ev.Flow.Kind) {
		return c.fireApp(ctx, ev, from, to, fromAgent, fromAddr, toAgent, toAddr)
	}

	// Request/response emulations (e.g. https-browse) need a responder/requester
	// pair over a real connection, not the one-directional sender/receiver pair.
	if emul.Has(ev.Flow.Kind) && emul.ModeOf(ev.Flow.Kind) == emul.ModeRequestResponse {
		return c.fireRequestResponse(ctx, ev, from, to, fromAgent, fromAddr, toAgent, toAddr)
	}

	dp := eventDatapath(ev)

	// Configure the receiver first (its ephemeral port targets the sender). A UDP
	// listener returns a data port; a NIC-bound datapath (afxdp) uses iface/queue.
	rxCfg, err := toAgent.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RECEIVER, Datapath: dp,
		PacketSize: int32ToU32(packetSize(ev)), Iface: to.Iface, Queue: int32ToU32(to.Queue),
	}})
	if err != nil {
		return fmt.Errorf("event %q: configure receiver: %w", ev.Name, err)
	}

	// Sender on the source agent, targeting the receiver's data address. Socket
	// datapaths send to that host:port; the AF_XDP datapath ignores it for delivery
	// (it sends raw frames over the NIC) but its ethernet generator uses it as the
	// dst IP/MAC when crafting frames — the receiver captures every frame on its
	// queue, so the port is nominal.
	dataHost := to.Address
	if dataHost == "" {
		dataHost = hostOf(toAddr)
	}
	port := int(rxCfg.GetDataPort())
	if dp == "afxdp" {
		port = afxdpDataPort
	}
	target := net.JoinHostPort(dataHost, strconv.Itoa(port))
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
	c.track(toAgent, toAddr, rxCfg.GetFlowId(), Receiver, ev.Name, from.Name, to.Name, dp)
	if _, err := fromAgent.Start(ctx, c.startReq(txCfg.GetFlowId(), from.Name, gate)); err != nil {
		return fmt.Errorf("event %q: start sender: %w", ev.Name, err)
	}
	c.track(fromAgent, fromAddr, txCfg.GetFlowId(), Sender, ev.Name, from.Name, to.Name, dp)
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
	c.track(toAgent, toAddr, respCfg.GetFlowId(), Responder, ev.Name, from.Name, to.Name, transport)

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
	c.track(fromAgent, fromAddr, reqCfg.GetFlowId(), Requester, ev.Name, from.Name, to.Name, transport)
	return nil
}

// appMinVersion is the first loom release whose agents carry the app engines
// (FLOW_ROLE_APP_CLIENT/APP_SERVER, CapabilitiesResponse.apps/networks). Used
// only to word the version-skew error actionably.
const appMinVersion = "v0.10"

// appServerGrace pads the app server's duration bound past the client's: the
// server must outlive the call so trailing media/RTCP (and the client's BYE)
// land before it winds down. Both ends run at the same start gate, so this
// constant only needs to cover the trailing exchange — the residual risk of
// the two duration clocks drifting apart (a blown gate) is covered by the
// measured-delay term appServerBound adds on top. The duration is orphan
// protection (the far end self-terminates even if the controller dies before
// Teardown), not the session clock — the client ends the session.
const appServerGrace = 2 * time.Second

// isAppKind reports whether a scenario flow kind names an application protocol
// engine (core/app). The kind strings are the metrics snapshot kinds — the
// same identifiers that travel as FlowSpec.app; http/video are accepted now
// and light up as those engines land on the agents (until then the skew gate
// refuses them cleanly).
func isAppKind(kind string) bool {
	switch kind {
	case metrics.KindVoIP, metrics.KindHTTP, metrics.KindVideo:
		return true
	}
	return false
}

// fireApp places an application flow: the app server on the destination agent
// (binding a data port inside port_min/port_max when given) and the app client
// on the source agent targeting it — the responder→requester ordering, because
// the server's bound port feeds the client's Target. Both agents pass the
// version-skew gate first, so an old loomd yields an actionable refusal
// instead of a confusing downstream Configure failure.
//
// Both ends are started at ONE shared gate (fire()'s lockstep invariant): the
// server binds its port at Configure, so it can be listening-ready while its
// Run — which anchors both its telemetry interval clock and its duration
// bound — waits for the same gate the client's media starts at. That keeps
// the two ends' interval boundaries aligned for the controller's per-interval
// consolidation, and pins the server's duration deadline to gate+d+grace so
// it always outlives the client's gate+d call regardless of link latency
// (a fixed pre-gate grace would be eaten by the gate slack on slow links).
func (c *Controller) fireApp(ctx context.Context, ev scenario.Event, from, to scenario.Endpoint, fromAgent loomv1.ControlClient, fromAddr string, toAgent loomv1.ControlClient, toAddr string) error {
	appName := ev.Flow.Kind
	network := appNetwork(ev)
	// The far end must be duration-bounded (orphan protection: a server whose
	// controller crashed after Start must not hold its port forever), and the
	// agent refuses an unbounded APP_SERVER spec — catch it here with the
	// scenario-level fix instead of surfacing that refusal mid-placement.
	if appDuration(ev) <= 0 {
		return fmt.Errorf("event %q: app flow %q requires a duration bound (stop.after or a flow `duration` param)", ev.Name, appName)
	}
	// The agents' app path bounds a run by duration only; refuse the stop
	// knobs it would silently drop (loom's validation style: an actionable
	// refusal, never a silently ignored condition).
	if ev.Stop.Count > 0 || ev.Stop.Volume > 0 {
		return fmt.Errorf("event %q: app flow %q supports only a duration bound (stop.after); stop.count/stop.volume are not supported for app kinds", ev.Name, appName)
	}
	if err := c.gateApp(ctx, toAgent, toAddr, appName, network, AppServer); err != nil {
		return fmt.Errorf("event %q: %w", ev.Name, err)
	}
	if err := c.gateApp(ctx, fromAgent, fromAddr, appName, network, AppClient); err != nil {
		return fmt.Errorf("event %q: %w", ev.Name, err)
	}

	// One gate for both ends, computed before the four placement RPCs below
	// with slack for each of them, so the gate normally opens after the last
	// Start has landed. If a slow link blows the gate anyway, both agents
	// degrade the same way (start on Start arrival, server first), so the
	// server still leads the client and the grace bound still holds.
	gate := c.appStartGate()

	// App server on the destination agent; it binds at Configure (so it is
	// reachable the moment the client's media starts) and reports the bound
	// media/data port. Tracked immediately after Configure: a Start that fails
	// mid-placement must still leave the flow visible to Teardown, or the
	// configured-but-never-started server — whose duration bound only engages
	// at Run — would hold its advertised port until the agent dies (the exact
	// orphan the duration mandate exists to prevent).
	srvCfg, err := toAgent.Configure(ctx, &loomv1.ConfigureRequest{Flow: appServerSpec(ev, network, c.s.Seed, c.appServerBound(ev))})
	if err != nil {
		return fmt.Errorf("event %q: configure app server: %w", ev.Name, err)
	}
	c.track(toAgent, toAddr, srvCfg.GetFlowId(), AppServer, ev.Name, from.Name, to.Name, appName)
	if _, err := toAgent.Start(ctx, c.startReq(srvCfg.GetFlowId(), to.Name, gate)); err != nil {
		return fmt.Errorf("event %q: start app server: %w", ev.Name, err)
	}

	// App client on the source agent, targeting the server's data address.
	// Tracked after Configure for the same reason as the server: the voip
	// client binds its socket eagerly at Build.
	dataHost := to.Address
	if dataHost == "" {
		dataHost = hostOf(toAddr)
	}
	target := net.JoinHostPort(dataHost, strconv.Itoa(int(srvCfg.GetDataPort())))
	cliCfg, err := fromAgent.Configure(ctx, &loomv1.ConfigureRequest{Flow: appClientSpec(ev, network, target, c.s.Seed)})
	if err != nil {
		return fmt.Errorf("event %q: configure app client: %w", ev.Name, err)
	}
	c.track(fromAgent, fromAddr, cliCfg.GetFlowId(), AppClient, ev.Name, from.Name, to.Name, appName)
	if _, err := fromAgent.Start(ctx, c.startReq(cliCfg.GetFlowId(), from.Name, gate)); err != nil {
		return fmt.Errorf("event %q: start app client: %w", ev.Name, err)
	}
	return nil
}

// agentCaps caches one agent's Capabilities plus its Health version, so the
// skew gate asks each agent once per run. apps is the legacy union list;
// appsClient/appsServer are the per-side lists (nil on agents predating
// them, in which case the gate falls back to the union).
type agentCaps struct {
	apps       map[string]bool
	appsClient map[string]bool
	appsServer map[string]bool
	networks   map[string]bool
	version    string
}

// capsFor fetches (and caches) the agent's capabilities and version.
func (c *Controller) capsFor(ctx context.Context, agent loomv1.ControlClient, addr string) (agentCaps, error) {
	if ac, ok := c.caps[addr]; ok {
		return ac, nil
	}
	caps, err := agent.Capabilities(ctx, &loomv1.CapabilitiesRequest{})
	if err != nil {
		return agentCaps{}, fmt.Errorf("capabilities of loomd at %s: %w", addr, err)
	}
	ac := agentCaps{apps: make(map[string]bool), networks: make(map[string]bool), version: "unknown version"}
	for _, a := range caps.GetApps() {
		ac.apps[a] = true
	}
	ac.appsClient = nameSet(caps.GetAppsClient())
	ac.appsServer = nameSet(caps.GetAppsServer())
	for _, n := range caps.GetNetworks() {
		ac.networks[n] = true
	}
	// Version is only for wording the refusal; an unreachable Health keeps the
	// gate decisive rather than failing it.
	if h, herr := agent.Health(ctx, &loomv1.HealthRequest{}); herr == nil && h.GetVersion() != "" {
		ac.version = h.GetVersion()
	}
	c.caps[addr] = ac
	return ac, nil
}

// nameSet builds a membership set from a capability list, keeping nil for an
// absent list so consumers can distinguish "advertises none" from "predates
// the field".
func nameSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// gateApp is the version-skew gate (ADR-0021 additive evolution): before an app
// flow is provisioned, the agent must advertise the app (and, when the flow
// pins one, the netpath network) in its Capabilities. An old loomd predating
// the apps field advertises none and is refused with an actionable error
// instead of a confusing Configure failure downstream. role selects the side
// this agent must run: an agent advertising the per-side lists is gated on
// the matching one (a server-only slimmed build placed as the client agent
// is refused here, not at Configure); agents predating the per-side fields
// are gated on the union list, the best their wire version can say.
func (c *Controller) gateApp(ctx context.Context, agent loomv1.ControlClient, addr, app, network string, role Role) error {
	ac, err := c.capsFor(ctx, agent, addr)
	if err != nil {
		return err
	}
	side, sideName := ac.appsClient, "app client"
	if role == AppServer {
		side, sideName = ac.appsServer, "app server"
	}
	// A non-empty per-side list (either side) marks an agent that speaks the
	// per-side wire; its side lists are then authoritative — an absent side
	// means "cannot run that side", not "old agent". Only when both are absent
	// (a repeated field carries no presence) do we fall back to the union.
	if ac.appsClient != nil || ac.appsServer != nil {
		if !side[app] {
			return fmt.Errorf("loomd at %s (%s) lacks %s %q; run loom >= %s", addr, ac.version, sideName, app, appMinVersion)
		}
	} else if !ac.apps[app] {
		return fmt.Errorf("loomd at %s (%s) lacks app %q; run loom >= %s", addr, ac.version, app, appMinVersion)
	}
	if network != "" && !ac.networks[network] {
		return fmt.Errorf("loomd at %s (%s) lacks network %q; run loom >= %s", addr, ac.version, network, appMinVersion)
	}
	return nil
}

// appNetwork resolves the netpath network an app flow's sockets are opened on
// (flow param `network`); "" lets the agent default to the host stack.
func appNetwork(ev scenario.Event) string {
	if v, ok := ev.Flow.Params["network"]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

// appServerBound computes the app server's duration bound: the event's
// duration plus appServerGrace (trailing media/RTCP/BYE), plus twice the
// slowest measured agent round trip. Both ends run at one shared gate, so
// their duration clocks normally start together and the grace is pure slack;
// the delay term keeps the bound safe even when a slow control link blows the
// gate and the client's Run starts up to two RPC round trips after the
// server's. Fake-delay-free tests (and LANs) see exactly d + appServerGrace.
func (c *Controller) appServerBound(ev scenario.Event) time.Duration {
	return appDuration(ev) + appServerGrace + 2*c.maxSyncDelay()
}

// appServerSpec builds the app server's FlowSpec: the far end of the app named
// by the flow kind. Params travel verbatim (codec, jb_ms, port_min/port_max,
// …). The server is duration-bounded whenever the client is — orphan
// protection per the responder-role design — with bound (appServerBound) as
// the enforced run limit so it outlives the client's call and trailing RTCP.
func appServerSpec(ev scenario.Event, network string, seed int64, bound time.Duration) *loomv1.FlowSpec {
	spec := &loomv1.FlowSpec{
		Role:    loomv1.FlowRole_FLOW_ROLE_APP_SERVER,
		App:     ev.Flow.Kind,
		Network: network,
		Params:  stringParams(ev.Flow.Params),
		Seed:    seed,
	}
	if appDuration(ev) > 0 {
		spec.Duration = durationpb.New(bound)
	}
	return spec
}

// appClientSpec builds the app client's FlowSpec: it drives the app named by
// the flow kind at the server's data address, bounded by the event's duration
// (stop.after wins; a `duration` flow param is the convenience fallback).
// Count/volume stop conditions are not carried: the agents' app path enforces
// only a duration bound, and fireApp refuses them up front rather than let a
// scenario's stop condition silently never fire.
func appClientSpec(ev scenario.Event, network, target string, seed int64) *loomv1.FlowSpec {
	spec := &loomv1.FlowSpec{
		Role:    loomv1.FlowRole_FLOW_ROLE_APP_CLIENT,
		App:     ev.Flow.Kind,
		Network: network,
		Target:  target,
		Params:  stringParams(ev.Flow.Params),
		Seed:    seed,
	}
	if d := appDuration(ev); d > 0 {
		spec.Duration = durationpb.New(d)
	}
	return spec
}

// appDuration resolves an app event's run bound: stop.after wins, else the
// flow block's `duration` knob (e.g. a call length), else 0 (until stopped).
func appDuration(ev scenario.Event) time.Duration {
	if ev.Stop.After > 0 {
		return ev.Stop.After
	}
	if v, ok := ev.Flow.Params["duration"]; ok {
		if d, err := units.ParseDuration(fmt.Sprint(v)); err == nil {
			return d
		}
	}
	return 0
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

// maxSyncDelay returns the slowest measured agent round-trip delay (0 when no
// TimeSync has run) — the scaling term for gate slack and grace bounds.
func (c *Controller) maxSyncDelay() time.Duration {
	var maxDelay time.Duration
	c.mu.Lock()
	for _, s := range c.sync {
		if s.Delay > maxDelay {
			maxDelay = s.Delay
		}
	}
	c.mu.Unlock()
	return maxDelay
}

// startGate returns a shared start time (on the controller's clock) far enough in
// the future that a Start RPC reaches every agent before the gate opens. The
// slack scales with the slowest measured round-trip delay so even high-latency
// links stay in lockstep; a floor covers RPC/processing on fast links.
func (c *Controller) startGate() time.Time {
	return time.Now().Add(100*time.Millisecond + c.maxSyncDelay())
}

// appStartGate returns the shared gate for an app pair. Unlike startGate,
// which is computed after the flows are configured (only the Start RPCs race
// the gate), fireApp computes its gate before any placement RPC — the
// server's data_port must flow into the client's Configure between the two
// Starts — so the slack covers the four placement round trips that follow
// (server Configure/Start, client Configure/Start) with the same
// measured-delay scaling.
func (c *Controller) appStartGate() time.Time {
	return time.Now().Add(100*time.Millisecond + 4*c.maxSyncDelay())
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

func (c *Controller) track(agent loomv1.ControlClient, addr, id string, role Role, event, from, to, datapath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.placed = append(c.placed, Placed{Agent: agent, AgentAddr: addr, FlowID: id, Role: role, Event: event, From: from, To: to, Datapath: datapath})
}

// senderSpec builds the sender's FlowSpec from an event, the chosen datapath,
// the receiver target (socket datapaths), and the source endpoint (iface/queue
// for NIC-bound datapaths). If the event's flow kind names an emulation, the spec
// carries it (and its params) so the agent runs the behavior engine.
func senderSpec(ev scenario.Event, dp, target string, from scenario.Endpoint, seed int64) *loomv1.FlowSpec {
	// AF_XDP bypasses the kernel stack, so the sender must emit complete Ethernet/
	// IPv4/UDP frames — use the ethernet (frame-crafting) generator. Socket
	// datapaths let the kernel build headers, so they use the plain stream.
	gen := "stream"
	if dp == "afxdp" {
		gen = "ethernet"
	}
	spec := &loomv1.FlowSpec{
		Role:       loomv1.FlowRole_FLOW_ROLE_SENDER,
		Datapath:   dp,
		Target:     target,
		Generator:  gen,
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

// eventDatapath resolves an event's datapath. The canonical place is the
// event-level `datapath:` field, but `packet_size` and other knobs live under
// `flow:`, so authors naturally put `datapath:` there too — accept it as a
// fallback rather than silently running UDP (which made `datapath: tcp` under
// `flow:` quietly send UDP datagrams). Event-level wins when both are set.
func eventDatapath(ev scenario.Event) string {
	if ev.Datapath != "" {
		return ev.Datapath
	}
	if v, ok := ev.Flow.Params["datapath"]; ok {
		if s := fmt.Sprint(v); s != "" {
			return s
		}
	}
	return "udp"
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
