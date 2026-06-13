// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/flow"
	"github.com/bgrewell/loom/core/units"
)

// managedFlow is one flow the agent holds across its lifecycle.
type managedFlow struct {
	id     string
	flow   *flow.Flow
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// flowManager tracks configured/running flows on an agent.
type flowManager struct {
	mu    sync.Mutex
	flows map[string]*managedFlow
	next  uint64
}

func newFlowManager() *flowManager {
	return &flowManager{flows: make(map[string]*managedFlow)}
}

func (m *flowManager) configure(fl *flow.Flow) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	id := fmt.Sprintf("flow-%d", m.next)
	m.flows[id] = &managedFlow{id: id, flow: fl}
	return id
}

func (m *flowManager) get(id string) (*managedFlow, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mf, ok := m.flows[id]
	return mf, ok
}

func (m *flowManager) start(id string) error {
	mf, ok := m.get(id)
	if ok && mf.done != nil {
		return fmt.Errorf("flow %q already started", id)
	}
	if !ok {
		return fmt.Errorf("flow %q not found", id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	mf.cancel = cancel
	mf.done = make(chan struct{})
	go func() {
		mf.err = mf.flow.Run(ctx)
		close(mf.done)
	}()
	return nil
}

func (m *flowManager) stop(id string) error {
	mf, ok := m.get(id)
	if !ok {
		return fmt.Errorf("flow %q not found", id)
	}
	if mf.cancel != nil {
		mf.cancel()
		<-mf.done
	}
	return nil
}

func (m *flowManager) destroy(id string) error {
	_ = m.stop(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if mf, ok := m.flows[id]; ok {
		_ = mf.flow.Datapath.Close()
		delete(m.flows, id)
	}
	return nil
}

// --- control-plane flow lifecycle RPCs (server = agent) ---

// Configure builds and stores a flow, returning its id. Ephemeral data-port
// assignment is a later step; data_port is 0 for now.
func (s *Server) Configure(_ context.Context, req *loomv1.ConfigureRequest) (*loomv1.ConfigureResponse, error) {
	spec, err := toSpec(req.GetFlow())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "flow spec: %v", err)
	}
	fl, err := flow.Build(spec)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build flow: %v", err)
	}
	return &loomv1.ConfigureResponse{FlowId: s.mgr.configure(fl)}, nil
}

// Arm is a no-op for now (receivers/ephemeral ports arrive later).
func (s *Server) Arm(_ context.Context, req *loomv1.ArmRequest) (*loomv1.ArmResponse, error) {
	if _, ok := s.mgr.get(req.GetFlowId()); !ok {
		return nil, status.Errorf(codes.NotFound, "flow %q", req.GetFlowId())
	}
	return &loomv1.ArmResponse{}, nil
}

// Start runs a configured flow.
func (s *Server) Start(_ context.Context, req *loomv1.StartRequest) (*loomv1.StartResponse, error) {
	if err := s.mgr.start(req.GetFlowId()); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	return &loomv1.StartResponse{}, nil
}

// Stop cancels a running flow.
func (s *Server) Stop(_ context.Context, req *loomv1.StopRequest) (*loomv1.StopResponse, error) {
	if err := s.mgr.stop(req.GetFlowId()); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &loomv1.StopResponse{}, nil
}

// Destroy tears a flow down and releases its datapath.
func (s *Server) Destroy(_ context.Context, req *loomv1.DestroyRequest) (*loomv1.DestroyResponse, error) {
	_ = s.mgr.destroy(req.GetFlowId())
	return &loomv1.DestroyResponse{}, nil
}

// StreamTelemetry streams interval samples for a flow until the flow finishes or
// the client disconnects, emitting a final sample on completion.
func (s *Server) StreamTelemetry(req *loomv1.TelemetryRequest, stream loomv1.Control_StreamTelemetryServer) error {
	mf, ok := s.mgr.get(req.GetFlowId())
	if !ok {
		return status.Errorf(codes.NotFound, "flow %q", req.GetFlowId())
	}
	c := mf.flow.Counters()
	ticker := time.NewTicker(s.telemetryInterval())
	defer ticker.Stop()

	last, lastT := uint64(0), time.Now()
	send := func(now time.Time) error {
		b, p := c.Bytes(), c.Packets()
		var bps float64
		if d := now.Sub(lastT).Seconds(); d > 0 {
			bps = float64(b-last) * 8 / d
		}
		last, lastT = b, now
		return stream.Send(&loomv1.TelemetrySample{
			FlowId: req.GetFlowId(), Nanos: now.UnixNano(),
			Bytes: b, Packets: p, BitsPerSec: bps,
		})
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-mf.doneCh():
			return send(time.Now()) // final sample
		case now := <-ticker.C:
			if err := send(now); err != nil {
				return err
			}
		}
	}
}

func (mf *managedFlow) doneCh() <-chan struct{} {
	if mf.done != nil {
		return mf.done
	}
	// Not started yet: never fires.
	return make(chan struct{})
}

func (s *Server) telemetryInterval() time.Duration {
	if s.telemetry > 0 {
		return s.telemetry
	}
	return time.Second
}

func toSpec(p *loomv1.FlowSpec) (flow.Spec, error) {
	if p == nil {
		return flow.Spec{}, fmt.Errorf("nil flow")
	}
	var dur time.Duration
	if p.GetDuration() != "" {
		d, err := units.ParseDuration(p.GetDuration())
		if err != nil {
			return flow.Spec{}, err
		}
		dur = d
	}
	return flow.Spec{
		Generator:  p.GetGenerator(),
		Payload:    p.GetPayload(),
		Datapath:   p.GetDatapath(),
		Target:     p.GetTarget(),
		PacketSize: int(p.GetPacketSize()),
		Rate:       p.GetRate(),
		Duration:   dur,
		Count:      p.GetCount(),
		Volume:     p.GetVolume(),
	}, nil
}
