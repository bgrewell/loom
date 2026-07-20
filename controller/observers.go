// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// TextObserver prints aggregate telemetry as human-readable lines.
type TextObserver struct {
	w       io.Writer
	perFlow bool
}

// NewTextObserver returns a TextObserver writing to w.
func NewTextObserver(w io.Writer) *TextObserver { return &TextObserver{w: w} }

// WithPerFlow toggles a per-flow breakdown under each aggregate line — useful to
// spot a single flow lagging (e.g. when flows take different network paths).
func (o *TextObserver) WithPerFlow(on bool) *TextObserver { o.perFlow = on; return o }

// Observe implements Observer. Each line is one consolidated interval. When the
// interval was flushed before every flow reported (a node we're not hearing from
// in near-real-time), it's marked — the final summary remains authoritative.
func (o *TextObserver) Observe(a Aggregate) {
	if len(a.Flows) == 0 {
		return
	}
	// tx is sender-measured, rx receiver-measured (so loss shows as tx > rx). An
	// interval flushed before every endpoint reported is flagged; the summary is
	// authoritative.
	marker := ""
	if !a.Complete && a.Expected > 0 {
		marker = fmt.Sprintf("  (!) %d/%d endpoints reporting", a.Sources, a.Expected)
	}
	fmt.Fprintf(o.w, "[%s] %stx %-11s rx %-11s%s%s\n",
		a.At.Format("15:04:05"), label(a), humanBits(a.TxBitsPerSec), humanBits(a.RxBitsPerSec), liveTCP(a.TCP), marker)
	if line := voipLive(a.App); line != "" {
		fmt.Fprintf(o.w, "           %s\n", line)
	}
	if o.perFlow {
		for _, f := range sortedFlows(a.Flows) {
			fmt.Fprintf(o.w, "           %-10s %-15s %-9s %-11s %s\n",
				f.Event, flowDir(f), f.Role, humanBits(f.BitsPerSec), humanBytes(f.Bytes))
		}
	}
}

// liveTCP renders a compact per-interval TCP-health suffix for the live line:
// retransmits *this interval* (the live trouble signal), current congestion window,
// and smoothed RTT. Empty for non-TCP intervals.
func liveTCP(t *TCPStats) string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf("  tcp retrans +%d cwnd %d rtt %.2fms", t.Retrans, t.Cwnd, float64(t.RttUs)/1000)
}

// voipLive renders the compact per-interval MOS/QoE line for a voip flow:
// MOS-CQ with its R factor, jitter, network loss, jitter-buffer discards, and
// the one-way delay with its error bar and provenance (an rtt/2 guess is never
// presented as a measured number). Empty for non-voip intervals.
func voipLive(am *loomv1.AppMetrics) string {
	v := am.GetVoip()
	if v == nil {
		return ""
	}
	return fmt.Sprintf("mos %.2f (R %.1f)  jitter %.2fms  loss %.2f%%  discard %.2f%%  owd %s",
		v.GetMosCq(), v.GetRFactor(), v.GetJitterMs(), v.GetLossPct(), v.GetDiscardPct(), owdField(v))
}

// owdField renders "12.3±0.5ms (timesync)", or "n/a" when the agent reported
// no one-way-delay estimate.
func owdField(v *loomv1.VoipMetrics) string {
	if v.GetOwdMethod() == "" || v.GetOwdMethod() == "none" {
		return "n/a"
	}
	return fmt.Sprintf("%.1f±%.1fms (%s)", v.GetOwdMs(), v.GetOwdErrMs(), v.GetOwdMethod())
}

// flowDir renders a flow's from→to direction, or "" when unknown.
func flowDir(f FlowSample) string {
	if f.From == "" || f.To == "" {
		return ""
	}
	return f.From + "→" + f.To
}

// JSONObserver prints aggregate telemetry as one JSON object per snapshot.
type JSONObserver struct{ enc *json.Encoder }

// NewJSONObserver returns a JSONObserver writing to w.
func NewJSONObserver(w io.Writer) *JSONObserver { return &JSONObserver{enc: json.NewEncoder(w)} }

// Observe implements Observer.
func (o *JSONObserver) Observe(a Aggregate) {
	m := map[string]any{
		"at":              a.At.Format(time.RFC3339Nano),
		"index":           a.Index,
		"event":           a.Event,
		"from":            a.From,
		"to":              a.To,
		"tx_bits_per_sec": a.TxBitsPerSec,
		"rx_bits_per_sec": a.RxBitsPerSec,
		"tx_bytes":        a.TxBytes,
		"rx_bytes":        a.RxBytes,
		"flows":           len(a.Flows),
		"sources":         a.Sources,
		"expected":        a.Expected,
		"complete":        a.Complete,
	}
	if t := a.TCP; t != nil {
		m["tcp_retrans"] = t.Retrans // delta this interval
		m["tcp_lost"] = t.Lost
		m["tcp_rtt_us"] = t.RttUs
		m["tcp_cwnd"] = t.Cwnd
	}
	if j := appJSON(a.App); j != nil {
		m["app"] = j
	}
	_ = o.enc.Encode(m)
}

// appJSON renders an AppMetrics message verbatim as JSON (proto field names,
// matching loom's snake_case serialization style), or nil when absent.
func appJSON(am *loomv1.AppMetrics) json.RawMessage {
	if am == nil {
		return nil
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(am)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

func humanBits(bps float64) string {
	switch {
	case bps >= 1e9:
		return fmt.Sprintf("%.2f Gbps", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%.2f Mbps", bps/1e6)
	case bps >= 1e3:
		return fmt.Sprintf("%.2f Kbps", bps/1e3)
	default:
		return fmt.Sprintf("%.0f bps", bps)
	}
}

// humanBytes renders a byte count with SI decimal units (consistent with the
// rest of loom's unit handling).
func humanBytes(b uint64) string {
	v := float64(b)
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2f GB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2f MB", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.2f KB", v/1e3)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// label renders an event/direction prefix for a consolidated line, padded so
// lines align in a multi-flow run. It is empty for an unlabeled aggregate (e.g. a
// single-event scenario predating the field, or the end-of-run snapshot), keeping
// the simple one-flow output unchanged.
func label(a Aggregate) string {
	if a.Event == "" {
		return ""
	}
	dir := ""
	if a.From != "" && a.To != "" {
		dir = a.From + "→" + a.To
	}
	return fmt.Sprintf("%-10s %-17s ", a.Event, dir)
}

// sortedFlows returns the flows ordered by event then role, so per-flow output is
// stable across ticks (the aggregate's slice comes from a map).
func sortedFlows(flows []FlowSample) []FlowSample {
	out := append([]FlowSample(nil), flows...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Event != out[j].Event {
			return out[i].Event < out[j].Event
		}
		return out[i].Role < out[j].Role
	})
	return out
}

// packetOriented reports whether every flow uses a packet datapath (udp/afxdp),
// so packet counts on the two ends correspond 1:1 and packet loss is meaningful.
// A stream transport (tcp) chunks differently on each end, so only byte loss is
// comparable there.
func packetOriented(flows []FlowSample) bool {
	any := false
	for _, f := range flows {
		switch f.Datapath {
		case "udp", "afxdp":
			any = true
		case "":
			// unknown datapath — don't claim packet semantics
		default:
			return false // a stream transport (tcp) is present
		}
	}
	return any
}

// hasAppFlows reports whether any flow is an app end (AppClient/AppServer).
// App engines account both directions of their media plane in one counter,
// so tx-vs-rx subtraction across app ends is not a loss measure.
func hasAppFlows(flows []FlowSample) bool {
	for _, f := range flows {
		if f.Role == AppClient || f.Role == AppServer {
			return true
		}
	}
	return false
}

// allAppFlows reports whether every flow is an app end (and there is at least
// one), i.e. the aggregate's tx/rx buckets hold only per-end app totals.
func allAppFlows(flows []FlowSample) bool {
	if len(flows) == 0 {
		return false
	}
	for _, f := range flows {
		if f.Role != AppClient && f.Role != AppServer {
			return false
		}
	}
	return true
}

// lossLine renders end-to-end loss (sender-measured minus receiver-measured) as a
// percentage with a count. For packet datapaths it reports packet loss; for stream
// transports it reports byte loss (≈0 — TCP recovers loss via retransmits, which
// app-level byte accounting can't see). Empty when nothing was sent, and for app
// flows: each app end counts both directions, so byte subtraction would cancel
// symmetric loss to ~0 — media loss is reported by the voip quality lines
// (each end scores the direction it receives), not by byte accounting.
func lossLine(a Aggregate) string {
	if a.TxBytes == 0 || hasAppFlows(a.Flows) {
		return ""
	}
	lostBytes := uint64(0)
	if a.TxBytes > a.RxBytes {
		lostBytes = a.TxBytes - a.RxBytes
	}
	if packetOriented(a.Flows) && a.TxPackets > 0 {
		lostPkts := uint64(0)
		if a.TxPackets > a.RxPackets {
			lostPkts = a.TxPackets - a.RxPackets
		}
		pct := float64(lostPkts) / float64(a.TxPackets) * 100
		return fmt.Sprintf("  loss       %.2f%%   (%d of %d packets, %s)\n",
			pct, lostPkts, a.TxPackets, humanBytes(lostBytes))
	}
	pct := float64(lostBytes) / float64(a.TxBytes) * 100
	return fmt.Sprintf("  loss       %.2f%%   (%s)\n", pct, humanBytes(lostBytes))
}

// tcpLine renders a sender flow's TCP_INFO for the summary: retransmits and lost
// segments (climbing = trouble), smoothed RTT ± variance, and the congestion
// window / slow-start threshold (a collapsed cwnd signals congestion).
func tcpLine(f FlowSample) string {
	t := f.TCP
	ssthresh := fmt.Sprintf("%d", t.Ssthresh)
	if t.Ssthresh >= 1<<30 { // kernel reports ~INT_MAX while still in slow start
		ssthresh = "∞"
	}
	return fmt.Sprintf("  tcp  %-10s %-15s retrans %d  lost %d  rtt %.2fms ±%.2f  cwnd %d seg  ssthresh %s\n",
		f.Event, flowDir(f), t.Retrans, t.Lost, float64(t.RttUs)/1000, float64(t.RttvarUs)/1000, t.Cwnd, ssthresh)
}

// voipLine renders an app flow's final voip quality for the summary, one line
// per end (each end scores the direction it receives): MOS/R, jitter, loss,
// discards, and OWD with its error bar and method — the same fields as the
// live line, labeled with the flow's stream and role. The snapshot is the
// flow's FINAL telemetry sample, which the agent scores over the whole call
// (voip CumulativeMetrics), not over the trailing partial interval.
func voipLine(f FlowSample) string {
	v := f.App.GetVoip()
	return fmt.Sprintf("  voip %-10s %-15s %-10s mos %.2f (R %.1f)  jitter %.2fms  loss %.2f%%  discard %.2f%%  owd %s\n",
		f.Event, flowDir(f), f.Role, v.GetMosCq(), v.GetRFactor(), v.GetJitterMs(), v.GetLossPct(), v.GetDiscardPct(), owdField(v))
}

// StreamSummary counts the distinct streams (events) in the flows and lists their
// unique directions, for the summary header. One event = one logical stream
// (carried by a sender + receiver pair). Exported for the JSON summary in loomctl.
func StreamSummary(flows []FlowSample) (int, string) {
	events := make(map[string]bool)
	var dirs []string
	seenDir := make(map[string]bool)
	for _, f := range flows {
		events[f.Event] = true
		if d := flowDir(f); d != "" && !seenDir[d] {
			seenDir[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	return len(events), strings.Join(dirs, ", ")
}

// Summary renders the authoritative end-of-run report: a header (scenario,
// duration, stream count + directions) followed by tx/rx cumulative totals and the
// average over the test duration, and (when perFlow) a line per flow. tx is
// sender-measured and rx receiver-measured, so on a lossless transport they match
// and on a lossy one the gap is loss. Averaging over the test duration (not
// wall-clock including startup) keeps it consistent with the per-interval lines.
// liveIncomplete notes when the live view was momentarily missing an endpoint, so
// the reader knows this report — not the live stream — is the source of truth.
func (a Aggregate) Summary(scenario string, duration time.Duration, perFlow, liveIncomplete bool) string {
	secs := duration.Seconds()
	avg := func(bytes uint64) string {
		if secs <= 0 {
			return "n/a"
		}
		return humanBits(float64(bytes) * 8 / secs)
	}
	streams, dirs := StreamSummary(a.Flows)
	var b strings.Builder
	fmt.Fprintf(&b, "--- summary (authoritative) ---\n")
	if scenario != "" {
		fmt.Fprintf(&b, "  scenario   %s\n", scenario)
	}
	fmt.Fprintf(&b, "  duration   %s\n", duration.Round(time.Millisecond))
	if dirs != "" {
		fmt.Fprintf(&b, "  streams    %d  (%s)\n", streams, dirs)
	} else {
		fmt.Fprintf(&b, "  streams    %d\n", streams)
	}
	// tx/rx labels: for push/reqresp flows tx is sender-measured and rx
	// receiver-measured (tx−rx = loss). App ends account both directions of
	// their media plane, so their buckets are per-end totals — labeled as
	// such when the run is all app flows, and never fed to the loss line
	// either way (a mixed run keeps the generic labels but still suppresses
	// the loss inference, which app bytes would corrupt).
	txLabel, rxLabel := "(sender-measured)", "(receiver-measured)"
	if allAppFlows(a.Flows) {
		txLabel, rxLabel = "(server end, both directions)", "(client end, both directions)"
	}
	fmt.Fprintf(&b, "  tx %-10s avg %s   %s\n", humanBytes(a.TxBytes), avg(a.TxBytes), txLabel)
	fmt.Fprintf(&b, "  rx %-10s avg %s   %s\n", humanBytes(a.RxBytes), avg(a.RxBytes), rxLabel)
	b.WriteString(lossLine(a))
	// TCP health (retransmits/RTT/cwnd) per sending TCP flow — the signal byte
	// accounting can't show, since TCP recovers loss below the app layer.
	for _, f := range sortedFlows(a.Flows) {
		if f.TCP != nil && (f.Role == Sender || f.Role == Responder || f.Role == Requester) {
			fmt.Fprintf(&b, "%s", tcpLine(f))
		}
	}
	// App quality (MOS/QoE) per app-flow end — each end of a voip call scores
	// the direction it receives, so both lines matter.
	for _, f := range sortedFlows(a.Flows) {
		if f.App.GetVoip() != nil {
			fmt.Fprintf(&b, "%s", voipLine(f))
		}
	}
	if perFlow {
		for _, f := range sortedFlows(a.Flows) {
			fmt.Fprintf(&b, "  %-10s %-15s %-9s %-10s avg %s\n",
				f.Event, flowDir(f), f.Role, humanBytes(f.Bytes), avg(f.Bytes))
		}
	}
	if liveIncomplete {
		fmt.Fprintf(&b, "  note: some live intervals were missing an endpoint; totals above are reconciled.\n")
	}
	return b.String()
}
