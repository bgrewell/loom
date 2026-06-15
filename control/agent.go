// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/flow"
)

// managedFlow is one flow the agent holds across its lifecycle. run is the
// sending Flow or receiving Receiver; dp is held for cleanup; port is the bound
// receiver port (0 for senders).
//
// run, dp, port, and done are set at configure time and never reassigned, so
// they are read without a lock. mu guards the mutable lifecycle fields
// (started/cancel/err), which gRPC dispatches concurrently from per-RPC
// goroutines.
type managedFlow struct {
	id   string
	run  flow.Runner
	dp   io.Closer // the flow's datapath, held for cleanup
	port uint32
	done chan struct{} // closed when the run goroutine returns

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	err     error
}

// flowManager tracks configured/running flows on an agent.
type flowManager struct {
	mu    sync.Mutex
	flows map[string]*managedFlow
	next  uint64
	max   int // cap on concurrently configured flows (0 = unlimited)
}

func newFlowManager() *flowManager {
	return &flowManager{flows: make(map[string]*managedFlow), max: defaultMaxFlows}
}

// configure registers a flow and returns its id, or an error if the agent's
// flow limit is reached (so an unbounded Configure loop cannot exhaust memory
// and ports). On error the caller must release run/dp.
func (m *flowManager) configure(run flow.Runner, dp io.Closer, port uint32) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.max > 0 && len(m.flows) >= m.max {
		return "", fmt.Errorf("flow limit reached (%d)", m.max)
	}
	m.next++
	id := fmt.Sprintf("flow-%d", m.next)
	m.flows[id] = &managedFlow{id: id, run: run, dp: dp, port: port, done: make(chan struct{})}
	return id, nil
}

func (m *flowManager) get(id string) (*managedFlow, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mf, ok := m.flows[id]
	return mf, ok
}

func (m *flowManager) start(id string) error {
	mf, ok := m.get(id)
	if !ok {
		return fmt.Errorf("flow %q not found", id)
	}
	mf.mu.Lock()
	defer mf.mu.Unlock()
	if mf.started {
		return fmt.Errorf("flow %q already started", id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	mf.started = true
	mf.cancel = cancel
	go func() {
		// Contain a flow panic to this flow: a generator/datapath panic must not
		// take down the agent and every other flow with it.
		defer close(mf.done)
		defer func() {
			if r := recover(); r != nil {
				mf.setErr(fmt.Errorf("flow %q panicked: %v", id, r))
			}
		}()
		mf.setErr(mf.run.Run(ctx))
	}()
	return nil
}

func (mf *managedFlow) setErr(err error) {
	mf.mu.Lock()
	mf.err = err
	mf.mu.Unlock()
}

func (m *flowManager) stop(id string) error {
	mf, ok := m.get(id)
	if !ok {
		return fmt.Errorf("flow %q not found", id)
	}
	mf.mu.Lock()
	cancel, started := mf.cancel, mf.started
	mf.mu.Unlock()
	if started && cancel != nil {
		cancel()  // idempotent: concurrent Stops cancel once and all observe done
		<-mf.done // done is closed exactly once by the run goroutine
	}
	return nil
}

func (m *flowManager) destroy(id string) error {
	_ = m.stop(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if mf, ok := m.flows[id]; ok {
		_ = mf.dp.Close()
		delete(m.flows, id)
	}
	return nil
}

// --- control-plane flow lifecycle RPCs (server = agent) ---

// Configure builds and stores a flow, returning its id. Ephemeral data-port
// assignment is a later step; data_port is 0 for now.
func (s *Server) Configure(_ context.Context, req *loomv1.ConfigureRequest) (*loomv1.ConfigureResponse, error) {
	p := req.GetFlow()

	if p.GetRole() == loomv1.FlowRole_FLOW_ROLE_REFLECTOR {
		return nil, status.Error(codes.Unimplemented, "reflector role not yet supported")
	}

	// Receiver: build the requested receive datapath, drain + account inbound
	// traffic. A UDP listener reports its ephemeral port; afxdp binds a NIC queue
	// and has none (data_port stays 0).
	if p.GetRole() == loomv1.FlowRole_FLOW_ROLE_RECEIVER {
		if err := validatePacketSize(p.GetPacketSize()); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		dname := p.GetDatapath()
		if dname == "" {
			dname = "udp"
		}
		rx, err := s.comps.RxDatapaths.Build(dname, datapath.Options{
			FrameSize: int(p.GetPacketSize()), Iface: p.GetIface(), Queue: int(p.GetQueue()),
		})
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "build receiver: %v", err)
		}
		var port uint32
		if pr, ok := rx.(interface{ Port() int }); ok {
			port = uint32(pr.Port())
		}
		id, err := s.mgr.configure(flow.NewReceiver(rx), rx, port)
		if err != nil {
			_ = rx.Close() // release the bound port/queue we just took
			return nil, status.Errorf(codes.ResourceExhausted, "%v", err)
		}
		return &loomv1.ConfigureResponse{FlowId: id, DataPort: port}, nil
	}

	// Sender.
	spec, err := toSpec(p)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "flow spec: %v", err)
	}
	fl, err := flow.Build(spec, s.comps)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build flow: %v", err)
	}
	id, err := s.mgr.configure(fl, fl.Datapath, 0)
	if err != nil {
		_ = fl.Datapath.Close() // release the datapath we just built
		return nil, status.Errorf(codes.ResourceExhausted, "%v", err)
	}
	return &loomv1.ConfigureResponse{FlowId: id}, nil
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
	c := mf.run.Counters()
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

// doneCh returns the flow's completion channel, created at configure time. It is
// closed when the run goroutine returns; for a flow that is configured but never
// started it simply never fires.
func (mf *managedFlow) doneCh() <-chan struct{} { return mf.done }

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
	if err := validatePacketSize(p.GetPacketSize()); err != nil {
		return flow.Spec{}, err
	}
	if err := validateTarget(p.GetTarget()); err != nil {
		return flow.Spec{}, err
	}
	var dur time.Duration
	if d := p.GetDuration(); d != nil {
		dur = d.AsDuration()
	}
	return flow.Spec{
		Generator:  p.GetGenerator(),
		Payload:    p.GetPayload(),
		Datapath:   p.GetDatapath(),
		Target:     p.GetTarget(),
		Iface:      p.GetIface(),
		Queue:      int(p.GetQueue()),
		PacketSize: int(p.GetPacketSize()),
		Rate:       p.GetRate(),
		Duration:   dur,
		Count:      p.GetCount(),
		Volume:     p.GetVolume(),
	}, nil
}
