// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package control implements the loom.v1.Control gRPC service — the
// controller<->agent control plane (DESIGN.md §8). This is the skeleton: the
// coordination RPCs (Health/Register/Capabilities/TimeSync) work; the flow
// lifecycle RPCs are inherited as Unimplemented until the agent fills them in.
package control

import (
	"context"
	"time"

	"google.golang.org/grpc"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/payload"
	"github.com/bgrewell/loom/core/scheduler"
)

// Server implements loomv1.ControlServer. Flow-lifecycle RPCs
// (Configure/Arm/Start/Stop/Destroy/StreamTelemetry) are inherited from
// UnimplementedControlServer and return codes.Unimplemented for now.
type Server struct {
	loomv1.UnimplementedControlServer
	version   string
	mgr       *flowManager
	telemetry time.Duration // telemetry sample interval (0 = 1s)
	authToken string        // shared control-plane token (empty = auth disabled)
}

// NewServer returns a control Server reporting the given version.
func NewServer(version string) *Server {
	return &Server{version: version, mgr: newFlowManager()}
}

// SetTelemetryInterval sets how often StreamTelemetry emits samples. Call before
// serving. 0 restores the 1s default.
func (s *Server) SetTelemetryInterval(d time.Duration) { s.telemetry = d }

// SetMaxFlows caps the number of concurrently configured flows on this agent;
// Configure returns ResourceExhausted past the cap. Call before serving. n <= 0
// removes the limit (not recommended on a reachable agent).
func (s *Server) SetMaxFlows(n int) { s.mgr.max = n }

// SetAuthToken sets the shared control-plane token (ADR-0014). Call before
// serving. When non-empty, NewGRPCServer installs interceptors that reject any
// RPC lacking a matching bearer token; empty leaves the plane open.
func (s *Server) SetAuthToken(token string) { s.authToken = token }

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

// Capabilities reports what this agent can do, from the live registries.
func (s *Server) Capabilities(context.Context, *loomv1.CapabilitiesRequest) (*loomv1.CapabilitiesResponse, error) {
	return &loomv1.CapabilitiesResponse{
		Datapaths:  datapath.Registry.Names(),
		Generators: generator.Registry.Names(),
		Schedulers: scheduler.Registry.Names(),
		Payloads:   payload.Registry.Names(),
	}, nil
}

// TimeSync stamps t2 on receipt and t3 on send for the four-timestamp exchange.
func (s *Server) TimeSync(_ context.Context, req *loomv1.TimeSyncRequest) (*loomv1.TimeSyncResponse, error) {
	t2 := time.Now().UnixNano()
	return &loomv1.TimeSyncResponse{T1: req.GetT1(), T2: t2, T3: time.Now().UnixNano()}, nil
}
