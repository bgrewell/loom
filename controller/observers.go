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
	marker := ""
	if !a.Complete && a.Expected > 0 {
		marker = fmt.Sprintf("  (!) %d/%d nodes reporting", a.Sources, a.Expected)
	}
	fmt.Fprintf(o.w, "[%s] %stx %-11s rx %-11s (%d flows)%s\n",
		a.At.Format("15:04:05"), label(a), humanBits(a.TxBitsPerSec), humanBits(a.RxBitsPerSec), len(a.Flows), marker)
	if o.perFlow {
		for _, f := range sortedFlows(a.Flows) {
			fmt.Fprintf(o.w, "           %-12s %-9s %-11s %s\n",
				f.Event, f.Role, humanBits(f.BitsPerSec), humanBytes(f.Bytes))
		}
	}
}

// JSONObserver prints aggregate telemetry as one JSON object per snapshot.
type JSONObserver struct{ enc *json.Encoder }

// NewJSONObserver returns a JSONObserver writing to w.
func NewJSONObserver(w io.Writer) *JSONObserver { return &JSONObserver{enc: json.NewEncoder(w)} }

// Observe implements Observer.
func (o *JSONObserver) Observe(a Aggregate) {
	_ = o.enc.Encode(map[string]any{
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
	})
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

// Summary renders the authoritative end-of-run report: tx/rx cumulative totals and
// the average throughput over the test duration, and (when perFlow) a line per
// flow. Averaging over the test duration (not wall-clock including startup) keeps
// it consistent with the per-interval lines. liveIncomplete notes when the live
// view was momentarily missing a node, so the reader knows this report — not the
// live stream — is the source of truth.
func (a Aggregate) Summary(duration time.Duration, perFlow, liveIncomplete bool) string {
	secs := duration.Seconds()
	avg := func(bytes uint64) string {
		if secs <= 0 {
			return "n/a"
		}
		return humanBits(float64(bytes) * 8 / secs)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- summary (authoritative) --- %s\n", duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "  tx %-10s avg %s\n", humanBytes(a.TxBytes), avg(a.TxBytes))
	fmt.Fprintf(&b, "  rx %-10s avg %s\n", humanBytes(a.RxBytes), avg(a.RxBytes))
	if perFlow {
		for _, f := range sortedFlows(a.Flows) {
			fmt.Fprintf(&b, "  %-12s %-9s %-10s avg %s\n",
				f.Event, f.Role, humanBytes(f.Bytes), avg(f.Bytes))
		}
	}
	if liveIncomplete {
		fmt.Fprintf(&b, "  note: some live intervals were missing a node; totals above are reconciled.\n")
	}
	return b.String()
}
