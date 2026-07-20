// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Application flow roles (FLOW_ROLE_APP_CLIENT / FLOW_ROLE_APP_SERVER): the
// agent side of the real-protocol-engine plane. An app flow is built from the
// Components.AppClients/AppServers registries over a netpath Network resolved
// from FlowSpec.network, then registered with the same flowManager that runs
// every other flow — Configure/Arm/Start/Stop/Destroy, panic containment, and
// telemetry boundaries apply unchanged.

package control

import (
	"context"
	"io"
	"math"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/flow"
	"github.com/bgrewell/loom/core/metrics"
)

// appOptions maps the wire spec onto app.Options: params pass through
// verbatim (codec, ptime, jb_ms, port_min/port_max, … — each app documents
// the keys it honors), seed feeds deterministic randomness, and packet_size
// doubles as the MTU bound for packet-oriented apps. OWD is deliberately left
// nil at this layer: the agent has no clock-offset source for the media path,
// so apps fall back to RTT/2 and label it as such (owd.RTTHalf) — Phase 4
// embedders that run a TimeSync loop supply a real owd.OffsetProvider instead.
func appOptions(p *loomv1.FlowSpec) app.Options {
	return app.Options{
		Params: p.GetParams(),
		Seed:   p.GetSeed(),
		MTU:    int(p.GetPacketSize()),
		Target: p.GetTarget(),
	}
}

// configureAppServer builds the far end of the app named in FlowSpec.app over
// the network named in FlowSpec.network and reports the server's bound port as
// data_port (the Receiver.Port() readback pattern), so the controller can aim
// an app client at it.
//
// Orphan protection: an APP_SERVER spec without a positive duration is
// REFUSED (InvalidArgument), not clamped. The design mandates that far-end
// flows are always duration-bounded so a server whose controller crashed
// after Start cannot hold its advertised port and serve strangers forever; we
// refuse rather than clamp because a silent clamp would desynchronize the two
// call legs (the controller believes the far end outlives the client it aims
// at it), and loom's validation style is an explicit, actionable refusal
// (validatePacketSize, validateTransport). The bound is enforced by the
// appRunner wrapper, so it holds even if an app engine ignores its context
// deadline conventions.
func (s *Server) configureAppServer(p *loomv1.FlowSpec) (*loomv1.ConfigureResponse, error) {
	if err := validateAppSpec(p); err != nil {
		return nil, err
	}
	if s.comps.AppServers == nil {
		return nil, status.Error(codes.Unimplemented, "agent has no app server registry")
	}
	dur := appDuration(p)
	if dur <= 0 {
		return nil, status.Error(codes.InvalidArgument,
			"app server flows require a positive duration (far-end orphan protection: an unbounded server would outlive a crashed controller)")
	}
	n, err := s.network(p.GetNetwork(), p.GetLocal())
	if err != nil {
		return nil, err
	}
	o := appOptions(p)
	o.Network = n
	srv, err := s.comps.AppServers.Build(p.GetApp(), o)
	if err != nil {
		_ = n.Close() // release the network we just built
		return nil, status.Errorf(codes.InvalidArgument, "build app server: %v", err)
	}
	port := uint32(srv.Addr().Port())
	id, err := s.mgr.configure(&appRunner{Runner: srv, duration: dur}, joinClosers{closerOf(srv), n}, port)
	if err != nil {
		_ = closerOf(srv).Close() // release the bound port we just took
		_ = n.Close()
		return nil, status.Errorf(codes.ResourceExhausted, "%v", err)
	}
	return &loomv1.ConfigureResponse{FlowId: id, DataPort: port}, nil
}

// configureAppClient builds the initiating side of the app named in
// FlowSpec.app, aimed at FlowSpec.target (the server agent's host plus the
// data_port its Configure returned). A client duration is optional — the near
// end is bounded by its controller's Stop — but is enforced when given.
func (s *Server) configureAppClient(p *loomv1.FlowSpec) (*loomv1.ConfigureResponse, error) {
	if err := validateAppSpec(p); err != nil {
		return nil, err
	}
	if s.comps.AppClients == nil {
		return nil, status.Error(codes.Unimplemented, "agent has no app client registry")
	}
	if p.GetTarget() == "" {
		return nil, status.Error(codes.InvalidArgument, "app client requires a target (server host:data_port)")
	}
	if err := validateTarget(p.GetTarget()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	n, err := s.network(p.GetNetwork(), p.GetLocal())
	if err != nil {
		return nil, err
	}
	o := appOptions(p)
	o.Network = n
	cli, err := s.comps.AppClients.Build(p.GetApp(), o)
	if err != nil {
		_ = n.Close() // release the network we just built
		return nil, status.Errorf(codes.InvalidArgument, "build app client: %v", err)
	}
	id, err := s.mgr.configure(&appRunner{Runner: cli, duration: appDuration(p)}, joinClosers{closerOf(cli), n}, 0)
	if err != nil {
		_ = closerOf(cli).Close() // release any socket the client bound eagerly
		_ = n.Close()
		return nil, status.Errorf(codes.ResourceExhausted, "%v", err)
	}
	return &loomv1.ConfigureResponse{FlowId: id}, nil
}

// validateAppSpec checks the fields shared by both app roles: an app name is
// required (it selects the registry factory), and packet_size — optional for
// apps, unlike raw flows — is bounds-checked only when set so a hostile spec
// cannot request an absurd MTU.
func validateAppSpec(p *loomv1.FlowSpec) error {
	if p.GetApp() == "" {
		return status.Error(codes.InvalidArgument, "app flows require an app name (FlowSpec.app)")
	}
	if ps := p.GetPacketSize(); ps != 0 {
		if err := validatePacketSize(ps); err != nil {
			return status.Errorf(codes.InvalidArgument, "%v", err)
		}
	}
	return nil
}

// appDuration extracts the flow's duration bound (0 = unbounded).
func appDuration(p *loomv1.FlowSpec) time.Duration {
	if d := p.GetDuration(); d != nil {
		return d.AsDuration()
	}
	return 0
}

// appRunner adapts an app engine (either side) to the flowManager: Run is
// bounded by the flow's duration — enforced here rather than trusted to the
// engine, so orphan protection holds for every app — and the engine's
// optional metrics.Source capability is forwarded (metricsAt) so the
// telemetry streamer can discover it on the managed runner (mf.run) by
// assertion, in the spirit of the flowTCPInfo pattern. Unlike TCP_INFO,
// whose reads are idempotent kernel snapshots, an engine's Metrics() call
// CLOSES an observation interval (metrics.VoIP's per-interval loss/discard
// semantics), so the read must happen exactly once per boundary no matter
// how many StreamTelemetry subscribers a flow has — metricsAt serializes it
// under mu and fans the boundary's snapshot out to every stream.
type appRunner struct {
	flow.Runner
	duration time.Duration

	mu       sync.Mutex
	haveSnap bool
	lastIdx  int64
	snap     metrics.Snapshot
}

// Run implements flow.Runner, cancelling the engine when the flow's duration
// elapses. The deadline starts when the flow actually runs (after any
// scheduled-start gate), so duration means media time, not queue time.
func (r *appRunner) Run(ctx context.Context) error {
	if r.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.duration)
		defer cancel()
	}
	return r.Runner.Run(ctx)
}

// finalBoundary is the metricsAt index of the final (trailing partial
// interval) sample. It orders after every real boundary index, so a stream
// that reaches the final sample first still closes the last interval exactly
// once and later finishers reuse its snapshot.
const finalBoundary = int64(math.MaxInt64)

// metricsAt returns the engine's quality snapshot for boundary index k (or
// finalBoundary for the completion sample), reading the engine at most once
// per boundary: the first stream to reach boundary k takes the fresh —
// interval-closing — read, and every other stream (including one catching up
// through past boundaries, k < lastIdx) gets the cached snapshot, so
// concurrent StreamTelemetry subscribers cannot split the observation
// intervals and corrupt each other's LossPct/DiscardPct/MOS. The final
// boundary prefers the engine's whole-call CumulativeMetrics capability when
// present, so the end-of-run summary reflects the entire call rather than
// the trailing fragment. Returns nil when the engine exposes no snapshot.
func (r *appRunner) metricsAt(k int64) metrics.Snapshot {
	src, ok := r.Runner.(metrics.Source)
	if !ok {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.haveSnap && k <= r.lastIdx {
		return r.snap
	}
	if k == finalBoundary {
		if cs, ok := r.Runner.(interface{ CumulativeMetrics() metrics.Snapshot }); ok {
			r.snap = cs.CumulativeMetrics()
			r.haveSnap, r.lastIdx = true, k
			return r.snap
		}
	}
	r.snap = src.Metrics()
	r.haveSnap, r.lastIdx = true, k
	return r.snap
}

// CumulativeMetrics forwards the engine's whole-call snapshot capability (nil
// when the engine has none). Unlike Metrics/metricsAt it closes no observation
// interval, so out-of-band consumers (diagnostics, tests) can read it without
// corrupting the telemetry boundaries' interval accounting.
func (r *appRunner) CumulativeMetrics() metrics.Snapshot {
	if cs, ok := r.Runner.(interface{ CumulativeMetrics() metrics.Snapshot }); ok {
		return cs.CumulativeMetrics()
	}
	return nil
}

// nopCloser is the io.Closer of a runner that owns nothing beyond what its
// Network releases.
type nopCloser struct{}

// Close implements io.Closer.
func (nopCloser) Close() error { return nil }

// closerOf returns v's io.Closer capability, or a no-op. App engines that
// bind eagerly (the voip server binds in Build so Addr is valid at configure
// time) expose Close so a flow torn down between Configure and Start does not
// leak its advertised port; engines without one have nothing to release.
func closerOf(v any) io.Closer {
	if c, ok := v.(io.Closer); ok {
		return c
	}
	return nopCloser{}
}

// flowAppMetrics returns the flow's app-quality snapshot for boundary index k
// (finalBoundary for the completion sample) as the TelemetrySample.app oneof,
// or nil for a flow whose runner exposes none — capability discovery by type
// assertion at the telemetry boundary, the same pattern as flowTCPInfo. The
// engine read is delegated to appRunner.metricsAt, which closes the
// observation interval exactly once per boundary and hands every telemetry
// stream the same snapshot (see appRunner).
func flowAppMetrics(mf *managedFlow, k int64) *loomv1.AppMetrics {
	src, ok := mf.run.(interface{ metricsAt(int64) metrics.Snapshot })
	if !ok {
		return nil
	}
	switch v := src.metricsAt(k).(type) {
	case metrics.VoIP:
		return &loomv1.AppMetrics{Kind: &loomv1.AppMetrics_Voip{Voip: voipMetricsProto(v)}}
	case metrics.HTTP:
		return &loomv1.AppMetrics{Kind: &loomv1.AppMetrics_Http{Http: httpMetricsProto(v)}}
	case metrics.Video:
		return &loomv1.AppMetrics{Kind: &loomv1.AppMetrics_Video{Video: videoMetricsProto(v)}}
	default:
		return nil // no snapshot, or a kind this wire version doesn't carry
	}
}

// voipMetricsProto maps a metrics.VoIP snapshot onto the wire message. The
// OWD method label and error bar travel with the value so an RTT/2 guess is
// never presented as a measured number.
func voipMetricsProto(v metrics.VoIP) *loomv1.VoipMetrics {
	m := &loomv1.VoipMetrics{
		MosCq:       v.MOSCQ,
		RFactor:     v.RFactor,
		JitterMs:    v.JitterMs,
		LossPct:     v.LossPct,
		DiscardPct:  v.DiscardPct,
		BurstR:      v.BurstR,
		RttMs:       v.RTTMs,
		OwdMs:       v.OWDMs,
		OwdErrMs:    v.OWDErrMs,
		OwdMethod:   v.OWDMethod,
		RxPackets:   v.RxPackets,
		Lost:        v.Lost,
		RemoteMosCq: v.RemoteMOSCQ,
		Emodel: &loomv1.EModelBreakdown{
			Ro:    v.EModel.Ro,
			Is:    v.EModel.Is,
			Idte:  v.EModel.Idte,
			Idle:  v.EModel.Idle,
			Idd:   v.EModel.Idd,
			IeEff: v.EModel.IeEff,
		},
	}
	for _, g := range v.MediaGaps {
		m.Gaps = append(m.Gaps, &loomv1.MediaGap{
			StartUnixNanos: g.Start.UnixNano(),
			EndUnixNanos:   g.End.UnixNano(),
			PacketsLost:    g.PacketsLost,
		})
	}
	return m
}

// httpMetricsProto maps a metrics.HTTP snapshot onto the wire message.
func httpMetricsProto(v metrics.HTTP) *loomv1.HttpMetrics {
	return &loomv1.HttpMetrics{
		Requests:       v.Requests,
		Errors:         v.Errors,
		TtfbMsP95:      v.TTFBMsP95,
		GoodputMbps:    v.GoodputMbps,
		TlsHandshakeMs: v.TLSHandshakeMs,
		ConnectMs:      v.ConnectMs,
	}
}

// videoMetricsProto maps a metrics.Video snapshot onto the wire message.
func videoMetricsProto(v metrics.Video) *loomv1.VideoMetrics {
	m := &loomv1.VideoMetrics{
		Stalls:         v.Stalls,
		StallTimeMs:    v.StallTimeMs,
		RebufferRatio:  v.RebufferRatio,
		BufferMs:       v.BufferMs,
		AvgBitrateKbps: v.AvgBitrateKbps,
		StartupMs:      v.StartupMs,
	}
	for _, g := range v.StallEvents {
		m.StallEvents = append(m.StallEvents, &loomv1.MediaGap{
			StartUnixNanos: g.Start.UnixNano(),
			EndUnixNanos:   g.End.UnixNano(),
			PacketsLost:    g.PacketsLost,
		})
	}
	return m
}
