// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package app is the application-traffic framework: real protocol engines
// (VoIP, HTTP, video) packaged as clients and servers the agent can drive with
// its existing flow lifecycle. An app dials and listens only through an
// injected netpath.Network — never net.Dial/net.Listen directly — so the same
// engine runs over the kernel stack, a datapath-backed network, or the
// in-memory test fabric unchanged (DESIGN.md §5, the netpath seam).
//
// Client and Server are flow.Runner-compatible (Run(ctx) error +
// Counters() *accounting.Counters), so the agent's flowManager lifecycle —
// Configure/Start/Stop, panic containment, telemetry boundaries — applies to
// apps exactly as it does to sending Flows and Receivers. (The assertion lives
// in this package's tests: app cannot import core/flow without a cycle through
// core/components.)
package app

import (
	"context"
	"net/netip"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/owd"
	"github.com/bgrewell/loom/core/registry"
)

// Options configures one app instance at Build time.
//
// Unlike netpath.Options — pure data per the ADR-0006 registry pattern —
// Options deliberately carries live values: Network and OWD are components,
// not names. The registries below stay generic over Options (factories keyed
// by app name, Options passed through Build at runtime), but an Options value
// is not serializable scenario data. The pure-data description of an app
// belongs to the control plane (a FlowSpec's app/network/params strings); the
// agent or embedder resolves those into live components first — Network from
// Components.Networks, OWD from its time-sync loop — and then calls Build,
// mirroring how flow.Build resolves a flow.Spec through Components before the
// Flow runs. Splitting Options into a pure half plus a side channel for the
// live half would just re-derive this struct in two pieces.
type Options struct {
	// Params are the app's tuning knobs (codec, ptime, jb_ms, objects,
	// port_min/port_max, …), taken verbatim from the flow's params. Each app
	// documents the keys it honors; NewParams provides typed access.
	Params map[string]string
	// Seed feeds the app's deterministic randomness (payload synthesis,
	// think-time jitter), so runs are reproducible.
	Seed int64
	// MTU bounds datagram payload sizes for packet-oriented apps.
	MTU int
	// Network is the connection factory the app dials/listens through,
	// resolved by the agent (registry) or embedder (direct constructors).
	Network netpath.Network
	// Target is the client side's server address ("host:port"). Servers
	// ignore it.
	Target string
	// OWD supplies the clock offset for one-way-delay measurement. Nil means
	// no synchronization: apps fall back to RTT/2 and label it as such
	// (owd.RTTHalf) — never silently presented as measured.
	OWD owd.OffsetProvider
}

// Client is the initiating side of an app (caller, fetcher, player). It is
// flow.Runner-compatible: Run blocks until the app completes or ctx is
// cancelled, and Counters exposes live byte/packet totals for sampling.
type Client interface {
	// Name returns the app's registry name ("voip", "http", "video").
	Name() string
	// Run executes the app until completion or ctx cancellation.
	Run(ctx context.Context) error
	// Counters exposes the app's live byte/packet totals.
	Counters() *accounting.Counters
}

// Server is the answering side of an app. Beyond the Client contract it
// reports its bound address: the agent returns Addr's port to the controller
// as the flow's data_port, the same bound-port readback pattern as the UDP
// receiver datapath's Port().
type Server interface {
	// Name returns the app's registry name.
	Name() string
	// Run serves until ctx cancellation.
	Run(ctx context.Context) error
	// Counters exposes the app's live byte/packet totals.
	Counters() *accounting.Counters
	// Addr returns the server's bound address, valid once the factory has
	// bound (before Run), so the agent can advertise the port at configure
	// time.
	Addr() netip.AddrPort
}

// ClientRegistry holds the app client factories by name. Apps self-register in
// init (ADR-0006); the agent builds from Components.AppClients, which
// components.Default() backs with this registry.
var ClientRegistry = registry.New[Client, Options]()

// ServerRegistry holds the app server factories by name.
var ServerRegistry = registry.New[Server, Options]()
