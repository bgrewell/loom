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

// Observe implements Observer.
func (o *TextObserver) Observe(a Aggregate) {
	// Suppress empty ticks (before any flow is placed/streaming) so the first
	// printed line is the first real traffic, the way iperf stays quiet until the
	// test connects.
	if len(a.Flows) == 0 {
		return
	}
	fmt.Fprintf(o.w, "[%s] tx %-11s rx %-11s (%d flows)\n",
		a.At.Format("15:04:05"), humanBits(a.TxBitsPerSec), humanBits(a.RxBitsPerSec), len(a.Flows))
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
		"tx_bits_per_sec": a.TxBitsPerSec,
		"rx_bits_per_sec": a.RxBitsPerSec,
		"tx_bytes":        a.TxBytes,
		"rx_bytes":        a.RxBytes,
		"flows":           len(a.Flows),
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

// Summary renders an end-of-run report: tx/rx totals with the average throughput
// over elapsed, and (when perFlow) a line per flow. avg is bytes×8/elapsed.
func (a Aggregate) Summary(elapsed time.Duration, perFlow bool) string {
	secs := elapsed.Seconds()
	avg := func(bytes uint64) string {
		if secs <= 0 {
			return "n/a"
		}
		return humanBits(float64(bytes) * 8 / secs)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- summary --- elapsed %s\n", elapsed.Round(time.Millisecond))
	fmt.Fprintf(&b, "  tx %-10s avg %s\n", humanBytes(a.TxBytes), avg(a.TxBytes))
	fmt.Fprintf(&b, "  rx %-10s avg %s\n", humanBytes(a.RxBytes), avg(a.RxBytes))
	if perFlow {
		for _, f := range sortedFlows(a.Flows) {
			fmt.Fprintf(&b, "  %-12s %-9s %-10s avg %s\n",
				f.Event, f.Role, humanBytes(f.Bytes), avg(f.Bytes))
		}
	}
	return b.String()
}
