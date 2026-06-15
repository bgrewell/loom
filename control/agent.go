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
	"github.com/bgrewell/loom/core/emul"
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

// start begins a configured flow. If startAt is non-zero and in the future, the
// run goroutine waits until then (on the agent's own clock) before generating —
// the controller schedules a shared start time across agents so flows begin in
// lockstep. A zero or past startAt runs immediately. The wait is interruptible by
// Stop (ctx cancellation).
func (m *flowManager) start(id string, startAt time.Time) error {
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
		if !startAt.IsZero() {
			if d := time.Until(startAt); d > 0 {
				timer := time.NewTimer(d)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return // stopped before the gate opened
				case <-timer.C:
				}
			}
		}
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

	// Responder: the server side of a request/response emulation. Binds an
	// ephemeral TCP/UDP port (returned as data_port) and serves the bytes a
	// requester asks for.
	if p.GetRole() == loomv1.FlowRole_FLOW_ROLE_RESPONDER {
		return s.configureResponder(p)
	}

	// Requester: the client side of a request/response emulation. Compiles the
	// named emulation to a behavior script and drives it against the responder.
	if p.GetRole() == loomv1.FlowRole_FLOW_ROLE_REQUESTER {
		return s.configureRequester(p)
	}

	// Emulation sender: a behavior-script runner over the chosen datapath.
	if p.GetEmulation() != "" {
		return s.configureEmulation(p)
	}

	// Raw sender.
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

// configureEmulation builds an application-behavior emulation sender: it resolves
// the datapath and compiles the named emulation to a behavior script, then wraps
// them in an emulation runner.
func (s *Server) configureEmulation(p *loomv1.FlowSpec) (*loomv1.ConfigureResponse, error) {
	if err := validatePacketSize(p.GetPacketSize()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := validateTarget(p.GetTarget()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	dname := p.GetDatapath()
	if dname == "" {
		dname = "discard"
	}
	dp, err := s.comps.TxDatapaths.Build(dname, datapath.Options{
		Addr: p.GetTarget(), FrameSize: int(p.GetPacketSize()), Iface: p.GetIface(), Queue: int(p.GetQueue()),
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build datapath: %v", err)
	}
	script, err := emul.Build(p.GetEmulation(), emul.Params(p.GetParams()))
	if err != nil {
		_ = dp.Close()
		return nil, status.Errorf(codes.InvalidArgument, "emulation: %v", err)
	}
	var dur time.Duration
	if d := p.GetDuration(); d != nil {
		dur = d.AsDuration()
	}
	runner := emul.NewRunner(script, dp, int(p.GetPacketSize()), dur, p.GetCount(), p.GetVolume(), p.GetSeed())
	id, err := s.mgr.configure(runner, dp, 0)
	if err != nil {
		_ = dp.Close()
		return nil, status.Errorf(codes.ResourceExhausted, "%v", err)
	}
	return &loomv1.ConfigureResponse{FlowId: id}, nil
}

// configureResponder binds a request/response responder on an ephemeral port and
// reports it as data_port so the controller can target it from the requester.
func (s *Server) configureResponder(p *loomv1.FlowSpec) (*loomv1.ConfigureResponse, error) {
	if err := validatePacketSize(p.GetPacketSize()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := validateTransport(p.GetTransport()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	resp, err := emul.ListenResponder(p.GetTransport(), int(p.GetPacketSize()))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build responder: %v", err)
	}
	port := uint32(resp.Port())
	id, err := s.mgr.configure(resp, resp, port)
	if err != nil {
		_ = resp.Close() // release the bound port we just took
		return nil, status.Errorf(codes.ResourceExhausted, "%v", err)
	}
	return &loomv1.ConfigureResponse{FlowId: id, DataPort: port}, nil
}

// configureRequester compiles the named emulation to a behavior script and dials
// the responder at target, preparing a request/response runner. The connection
// is opened at configure time, so the responder must already be listening.
func (s *Server) configureRequester(p *loomv1.FlowSpec) (*loomv1.ConfigureResponse, error) {
	if err := validatePacketSize(p.GetPacketSize()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := validateTransport(p.GetTransport()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := validateTarget(p.GetTarget()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if p.GetEmulation() == "" {
		return nil, status.Error(codes.InvalidArgument, "requester requires an emulation")
	}
	script, err := emul.Build(p.GetEmulation(), emul.Params(p.GetParams()))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "emulation: %v", err)
	}
	var dur time.Duration
	if d := p.GetDuration(); d != nil {
		dur = d.AsDuration()
	}
	req, err := emul.DialRequester(p.GetTransport(), p.GetTarget(), script, int(p.GetPacketSize()), dur, p.GetCount(), p.GetVolume(), p.GetSeed())
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "dial responder: %v", err)
	}
	id, err := s.mgr.configure(req, req, 0)
	if err != nil {
		_ = req.Close() // release the connection we just dialed
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

// Start runs a configured flow, optionally at a scheduled time (start_at) on this
// agent's clock so flows across agents begin in lockstep.
func (s *Server) Start(_ context.Context, req *loomv1.StartRequest) (*loomv1.StartResponse, error) {
	var startAt time.Time
	if ns := req.GetStartAtUnixNanos(); ns > 0 {
		startAt = time.Unix(0, ns)
	}
	if err := s.mgr.start(req.GetFlowId(), startAt); err != nil {
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

// rateTracker computes per-interval throughput (bits/sec) from a monotonically
// increasing byte counter. Seed it with the counter's value at the moment
// measurement begins so the first interval measures only bytes seen after that
// instant — not everything that accumulated before the stream was opened.
type rateTracker struct {
	lastBytes uint64
	lastTime  time.Time
}

func newRateTracker(bytes uint64, now time.Time) *rateTracker {
	return &rateTracker{lastBytes: bytes, lastTime: now}
}

// bitsPerSec returns the throughput since the previous call (or since seeding),
// then advances the baseline. A counter that went backwards (shouldn't happen)
// or a non-positive interval yields 0 rather than a bogus/negative rate.
func (r *rateTracker) bitsPerSec(bytes uint64, now time.Time) float64 {
	var bps float64
	if d := now.Sub(r.lastTime).Seconds(); d > 0 && bytes >= r.lastBytes {
		bps = float64(bytes-r.lastBytes) * 8 / d
	}
	r.lastBytes, r.lastTime = bytes, now
	return bps
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

	// Seed the meter with the counter's current value, not 0: a flow is usually
	// already running by the time a telemetry stream is opened, so a 0 baseline
	// would charge every byte sent before this stream began to the first interval,
	// inflating the first sample's rate (~2x). Seeding here aligns the byte and
	// time baselines so the first rate measures only this interval.
	meter := newRateTracker(c.Bytes(), time.Now())
	send := func(now time.Time) error {
		b, p := c.Bytes(), c.Packets()
		return stream.Send(&loomv1.TelemetrySample{
			FlowId: req.GetFlowId(), Nanos: now.UnixNano(),
			Bytes: b, Packets: p, BitsPerSec: meter.bitsPerSec(b, now),
		})
	}

	// Send an immediate sample so the collector gets a cumulative baseline at
	// subscribe time (≈ flow start) rather than waiting a full interval — this is
	// what lets an N-second run report ~N/interval lines instead of losing the
	// first one or two.
	if err := send(time.Now()); err != nil {
		return err
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

// defaultTelemetryInterval is how often an agent samples a flow's counters. It is
// deliberately faster than a typical controller display interval (e.g. 1s) so the
// collector always has a fresh cumulative reading to compute each display
// interval's rate from. Override with LOOMD_TELEMETRY.
const defaultTelemetryInterval = 250 * time.Millisecond

func (s *Server) telemetryInterval() time.Duration {
	if s.telemetry > 0 {
		return s.telemetry
	}
	return defaultTelemetryInterval
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
