// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// TextObserver prints aggregate telemetry as human-readable lines.
type TextObserver struct{ w io.Writer }

// NewTextObserver returns a TextObserver writing to w.
func NewTextObserver(w io.Writer) *TextObserver { return &TextObserver{w: w} }

// Observe implements Observer.
func (o *TextObserver) Observe(a Aggregate) {
	fmt.Fprintf(o.w, "[%s] tx %-11s rx %-11s (%d flows)\n",
		a.At.Format("15:04:05"), humanBits(a.TxBitsPerSec), humanBits(a.RxBitsPerSec), len(a.Flows))
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
