// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"encoding/json"
	"fmt"
	"io"
)

// Human writes human-readable lines to w.
type Human struct{ w io.Writer }

// NewHuman returns a Human reporter writing to w.
func NewHuman(w io.Writer) *Human { return &Human{w: w} }

// Sample implements Reporter.
func (h *Human) Sample(s Sample) {
	fmt.Fprintf(h.w, "[%6.1fs] %12s  %8d pkts  %10s\n",
		s.Elapsed.Seconds(), humanBits(s.BitsPerSec), s.Packets, humanBytes(s.Bytes))
}

// Summary implements Reporter.
func (h *Human) Summary(s Summary) {
	fmt.Fprintf(h.w, "--- summary ---\n  duration : %s\n  sent     : %s in %d packets\n  avg rate : %s\n",
		s.Duration.Round(1e6), humanBytes(s.Bytes), s.Packets, humanBits(s.AvgBitsPerSec))
}

// JSON writes one JSON object per line (json-lines).
type JSON struct{ enc *json.Encoder }

// NewJSON returns a JSON reporter writing to w.
func NewJSON(w io.Writer) *JSON { return &JSON{enc: json.NewEncoder(w)} }

// Sample implements Reporter.
func (j *JSON) Sample(s Sample) {
	_ = j.enc.Encode(map[string]any{
		"type":         "sample",
		"elapsed_s":    s.Elapsed.Seconds(),
		"bytes":        s.Bytes,
		"packets":      s.Packets,
		"bits_per_sec": s.BitsPerSec,
	})
}

// Summary implements Reporter.
func (j *JSON) Summary(s Summary) {
	_ = j.enc.Encode(map[string]any{
		"type":             "summary",
		"duration_s":       s.Duration.Seconds(),
		"bytes":            s.Bytes,
		"packets":          s.Packets,
		"avg_bits_per_sec": s.AvgBitsPerSec,
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

func humanBytes(b uint64) string {
	const k = 1024.0
	f := float64(b)
	switch {
	case f >= k*k*k:
		return fmt.Sprintf("%.2f GB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.2f MB", f/(k*k))
	case f >= k:
		return fmt.Sprintf("%.2f KB", f/k)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
