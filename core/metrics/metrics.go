// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package metrics is loom's application results plane: typed quality
// snapshots (VoIP MOS, HTTP timings, video QoE) that application engines
// expose through one tiny seam.
//
// An app engine that has quality numbers to report implements [Source]; the
// consumer (the agent's telemetry streamer in Phase 3, an embedder's own
// loop today) type-asserts for it at telemetry boundaries — exactly the
// flowTCPInfo pattern, where a capability is discovered by assertion instead
// of widening the flow.Runner interface. [Snapshot.Kind] discriminates the
// concrete type ([KindVoIP], [KindHTTP], [KindVideo]) so a snapshot can
// travel through interface-typed plumbing and still be dispatched without
// reflection.
//
// Snapshots are plain data: no methods beyond Kind, no synchronization, no
// live references. A Source must return a self-contained copy — the caller
// may hold it, marshal it, or diff it against a later one freely. JSON tags
// are snake_case, matching loom's serialization style (controller JSON
// observers, proto wire names); the golden tests in this package pin every
// field name so wire evolution is deliberate, not accidental.
//
// The package is dependency-light by design: it imports only core/rtp (for
// [rtp.Gap]) and core/quality/emodel (for [emodel.Components]). It must
// never import core/app — app engines import metrics to build snapshots,
// not the other way around.
package metrics

import (
	"github.com/bgrewell/loom/core/quality/emodel"
	"github.com/bgrewell/loom/core/rtp"
)

// Snapshot kinds, as returned by [Snapshot.Kind]. These are wire-visible
// identifiers (they become FlowSpec.app / AppMetrics discriminators in
// Phase 3) — never rename one.
const (
	// KindVoIP identifies a [VoIP] snapshot.
	KindVoIP = "voip"
	// KindHTTP identifies an [HTTP] snapshot.
	KindHTTP = "http"
	// KindVideo identifies a [Video] snapshot.
	KindVideo = "video"
)

// Source is implemented by application engines that expose a quality
// snapshot. Consumers discover it by type assertion at telemetry
// boundaries; Metrics must return a self-contained copy that is safe to
// retain while the engine keeps running.
type Source interface {
	Metrics() Snapshot
}

// Snapshot is one point-in-time quality reading. Kind returns the snapshot's
// wire identifier ([KindVoIP], [KindHTTP] or [KindVideo]) so interface-typed
// consumers can dispatch on the concrete type.
type Snapshot interface {
	Kind() string
}

// Compile-time checks: every snapshot satisfies Snapshot by value (and
// therefore also by pointer).
var (
	_ Snapshot = VoIP{}
	_ Snapshot = HTTP{}
	_ Snapshot = Video{}
)

// VoIP is the media-quality snapshot of one RTP session direction pair, as
// produced by core/app/voip: RFC 3550 receiver statistics, RFC 3611-style
// discard accounting, one-way delay with its provenance and error bar, and
// the G.107 E-model rating with its full audit breakdown.
type VoIP struct {
	// Codec is the negotiated codec name ("pcmu", "opus", ...).
	Codec string `json:"codec"`
	// TxPackets counts RTP packets sent; RxPackets counts packets the
	// A.1 state machine accepted (duplicates included, per RFC 3550).
	TxPackets uint64 `json:"tx_packets"`
	RxPackets uint64 `json:"rx_packets"`
	// Lost is the signed A.3 cumulative loss clamped at 0 for reporting;
	// Duplicates and Reordered are the corresponding RxSnapshot counters.
	Lost       uint64 `json:"lost"`
	Duplicates uint64 `json:"duplicates"`
	Reordered  uint64 `json:"reordered"`
	// LossPct is network packet loss and DiscardPct jitter-buffer discards,
	// both in percent (0..100). Their sum is the E-model's Ppl input —
	// RFC 3611 discard semantics, so delay spikes hurt MOS.
	LossPct    float64 `json:"loss_pct"`
	DiscardPct float64 `json:"discard_pct"`
	// JitterMs is the A.8 interarrival jitter in milliseconds; RTTMs is the
	// RFC 3550 §6.4.1 LSR/DLSR round trip in milliseconds.
	JitterMs float64 `json:"jitter_ms"`
	RTTMs    float64 `json:"rtt_ms"`
	// OWDMs is the one-way delay estimate and OWDErrMs its half-width error
	// bound, both in milliseconds; OWDMethod labels the provenance —
	// "timesync", "rtt/2", "assume-synced" (owd.Method strings) or "none"
	// when no estimate exists. The error bar travels with the value so an
	// RTT/2 guess is never presented as a measured number.
	OWDMs     float64 `json:"owd_ms"`
	OWDErrMs  float64 `json:"owd_err_ms"`
	OWDMethod string  `json:"owd_method"`
	// BurstR is the Gilbert burst ratio (1 = random loss, >1 = bursty);
	// RFactor and MOSCQ are the local G.107/G.107.1 rating and MOS-CQE.
	BurstR  float64 `json:"burst_r"`
	RFactor float64 `json:"r_factor"`
	MOSCQ   float64 `json:"mos_cq"`
	// EModel is the per-term audit breakdown behind RFactor, so a rating
	// can be attributed to delay, loss or codec rather than trusted blindly.
	EModel emodel.Components `json:"emodel"`
	// RemoteRFactor and RemoteMOSCQ are the peer's view of the opposite
	// direction, carried back in RTCP XR VoIP-metrics blocks (0 when the
	// peer has not reported).
	RemoteRFactor float64 `json:"remote_r_factor"`
	RemoteMOSCQ   float64 `json:"remote_mos_cq"`
	// RemoteBye reports that the peer sent an RTCP BYE: the session is
	// still pacing (flows are duration-bounded), but the remote view above
	// is final and the peer has stopped listening.
	RemoteBye bool `json:"remote_bye"`
	// MediaGaps lists holes in media arrival (>3·ptime of silence), the raw
	// material for handover/outage correlation.
	MediaGaps []rtp.Gap `json:"media_gaps,omitempty"`
}

// Kind implements [Snapshot].
func (VoIP) Kind() string { return KindVoIP }

// HTTP is the web-traffic snapshot produced by core/app/httpx (Phase 6):
// request counts and the latency/goodput aggregates of the completed
// requests in the observation window.
type HTTP struct {
	// Requests counts completed requests; Errors counts transport or
	// non-2xx failures (also included in Requests).
	Requests uint64 `json:"requests"`
	Errors   uint64 `json:"errors"`
	// ConnectMs and TLSHandshakeMs are mean TCP-connect and TLS-handshake
	// times of the window's newly established connections (reused keep-alive
	// connections contribute no samples); TTFBMsP50/P95/P99 are
	// time-to-first-byte percentiles and ObjectMsP50/P95/P99 the full-transfer
	// (request start to last body byte) percentiles, all in milliseconds.
	ConnectMs      float64 `json:"connect_ms"`
	TLSHandshakeMs float64 `json:"tls_handshake_ms"`
	TTFBMsP50      float64 `json:"ttfb_ms_p50"`
	TTFBMsP95      float64 `json:"ttfb_ms_p95"`
	TTFBMsP99      float64 `json:"ttfb_ms_p99"`
	ObjectMsP50    float64 `json:"object_ms_p50"`
	ObjectMsP95    float64 `json:"object_ms_p95"`
	ObjectMsP99    float64 `json:"object_ms_p99"`
	// GoodputMbps is application-payload throughput in megabits per second.
	GoodputMbps float64 `json:"goodput_mbps"`
}

// Kind implements [Snapshot].
func (HTTP) Kind() string { return KindHTTP }

// Video is the ABR-player QoE snapshot produced by core/app/vidstream
// (Phase 7): startup, stalls, buffer level and ladder behavior of the
// virtual player.
type Video struct {
	// SegmentsFetched counts media segments downloaded; Stalls counts
	// buffer-underrun events while playing.
	SegmentsFetched uint64 `json:"segments_fetched"`
	Stalls          uint64 `json:"stalls"`
	// StartupMs is time from session start to first play; StallTimeMs the
	// total time spent stalled; RebufferRatio stall time over stall+play
	// time (bounded [0,1]: an all-stall interval reads 1, not 0); BufferMs
	// the current buffer level; AvgBitrateKbps the mean bitrate of fetched
	// segments.
	StartupMs      float64 `json:"startup_ms"`
	StallTimeMs    float64 `json:"stall_time_ms"`
	RebufferRatio  float64 `json:"rebuffer_ratio"`
	BufferMs       float64 `json:"buffer_ms"`
	AvgBitrateKbps float64 `json:"avg_bitrate_kbps"`
	// RepSwitchesUp and RepSwitchesDown count ABR ladder switches.
	RepSwitchesUp   uint64 `json:"rep_switches_up"`
	RepSwitchesDown uint64 `json:"rep_switches_down"`
	// StallEvents lists each stall as a timed gap (rtp.Gap reused as the
	// generic timed-outage record; PacketsLost is 0 for video), aligning
	// stalls with media gaps and handover events on one timeline.
	StallEvents []rtp.Gap `json:"stall_events,omitempty"`
}

// Kind implements [Snapshot].
func (Video) Kind() string { return KindVideo }
