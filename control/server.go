// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package control implements the loom.v1.Control gRPC service — the
// controller<->agent control plane (DESIGN.md §8). This is the skeleton: the
// coordination RPCs (Health/Register/Capabilities/TimeSync) work; the flow
// lifecycle RPCs are inherited as Unimplemented until the agent fills them in.
package control

import (
	"context"
	"sort"
	"time"

	"google.golang.org/grpc"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/components"
)

// Server implements loomv1.ControlServer. Flow-lifecycle RPCs
// (Configure/Arm/Start/Stop/Destroy/StreamTelemetry) are inherited from
// UnimplementedControlServer and return codes.Unimplemented for now.
type Server struct {
	loomv1.UnimplementedControlServer
	version   string
	mgr       *flowManager
	telemetry time.Duration          // telemetry sample interval (0 = 1s)
	authToken string                 // shared control-plane token (empty = auth disabled)
	comps     *components.Components // pluggable parts this agent offers
}

// Option configures a Server (ADR-0022). Apply at construction via NewServer.
type Option func(*Server)

// WithTelemetryInterval sets how often StreamTelemetry emits samples (0 = 1s).
func WithTelemetryInterval(d time.Duration) Option { return func(s *Server) { s.telemetry = d } }

// WithMaxFlows caps concurrently configured flows; Configure returns
// ResourceExhausted past the cap. n <= 0 removes the limit (not recommended on a
// reachable agent).
func WithMaxFlows(n int) Option { return func(s *Server) { s.mgr.max = n } }

// WithAuthToken sets the shared control-plane token (ADR-0014). When non-empty,
// NewGRPCServer installs interceptors that reject any RPC lacking a matching
// bearer token; empty leaves the plane open.
func WithAuthToken(token string) Option { return func(s *Server) { s.authToken = token } }

// WithComponents sets the datapath/generator/scheduler/payload registries this
// agent builds flows from and advertises via Capabilities. Defaults to
// components.Default().
func WithComponents(c *components.Components) Option { return func(s *Server) { s.comps = c } }

// NewServer returns a control Server reporting the given version, configured by
// opts.
func NewServer(version string, opts ...Option) *Server {
	s := &Server{version: version, mgr: newFlowManager(), comps: components.Default()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// AuthEnabled reports whether a control-plane token is configured.
func (s *Server) AuthEnabled() bool { return s.authToken != "" }

// NewGRPCServer builds a *grpc.Server with the control service registered. When
// the Server has an auth token set, token-checking unary/stream interceptors are
// installed.
func NewGRPCServer(s *Server) *grpc.Server {
	var opts []grpc.ServerOption
	if s.authToken != "" {
		opts = append(opts,
			grpc.UnaryInterceptor(tokenUnaryInterceptor(s.authToken)),
			grpc.StreamInterceptor(tokenStreamInterceptor(s.authToken)),
		)
	}
	gs := grpc.NewServer(opts...)
	loomv1.RegisterControlServer(gs, s)
	return gs
}

// APIVersion is the control-plane wire version this build speaks (ADR-0021). Bump
// it on a breaking proto change so peers can detect a mismatch.
const APIVersion = 1

// Health reports liveness, build version, and the wire API version.
func (s *Server) Health(context.Context, *loomv1.HealthRequest) (*loomv1.HealthResponse, error) {
	return &loomv1.HealthResponse{Version: s.version, Ready: true, ApiVersion: APIVersion}, nil
}

// Register enrolls an agent. Minimal for now: the agent id is echoed as the
// session. Token/mTLS enrollment (ADR-0014) arrives with the controller/agent.
func (s *Server) Register(_ context.Context, req *loomv1.RegisterRequest) (*loomv1.RegisterResponse, error) {
	return &loomv1.RegisterResponse{Session: req.GetAgentId()}, nil
}

// Capabilities reports what this agent can do, from its configured components.
// networks and apps are the version-skew gate for the app plane: consumers
// check them at provision time and fail fast with an actionable error instead
// of configuring a role this agent cannot run (ADR-0021: additive evolution,
// so mixed versions degrade to clean refusals). The registries are guarded for
// nil because embedder component sets predating them leave the fields unset.
func (s *Server) Capabilities(context.Context, *loomv1.CapabilitiesRequest) (*loomv1.CapabilitiesResponse, error) {
	resp := &loomv1.CapabilitiesResponse{
		Datapaths:  s.comps.TxDatapaths.Names(),
		Generators: s.comps.Generators.Names(),
		Schedulers: s.comps.Schedulers.Names(),
		Payloads:   s.comps.Payloads.Names(),
		Apps:       s.appNames(),
	}
	if s.comps.AppClients != nil {
		resp.AppsClient = s.comps.AppClients.Names()
	}
	if s.comps.AppServers != nil {
		resp.AppsServer = s.comps.AppServers.Names()
	}
	if s.comps.Networks != nil {
		resp.Networks = s.comps.Networks.Names()
	}
	return resp, nil
}

// appNames returns the union of registered app client and server names,
// sorted — the legacy single-list `apps` field. A consumer that can gate
// per side uses apps_client/apps_server instead (a slimmed build may carry
// only one side of an app; the union alone can't say which).
func (s *Server) appNames() []string {
	seen := make(map[string]struct{})
	if s.comps.AppClients != nil {
		for _, n := range s.comps.AppClients.Names() {
			seen[n] = struct{}{}
		}
	}
	if s.comps.AppServers != nil {
		for _, n := range s.comps.AppServers.Names() {
			seen[n] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TimeSync stamps t2 on receipt and t3 on send for the four-timestamp exchange.
func (s *Server) TimeSync(_ context.Context, req *loomv1.TimeSyncRequest) (*loomv1.TimeSyncResponse, error) {
	t2 := time.Now().UnixNano()
	return &loomv1.TimeSyncResponse{T1: req.GetT1(), T2: t2, T3: time.Now().UnixNano()}, nil
}
