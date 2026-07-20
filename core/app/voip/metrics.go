// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package voip

import (
	"time"

	"github.com/bgrewell/loom/core/metrics"
)

// Metrics assembles the session's metrics.VoIP snapshot under the session
// lock, safe to call concurrently with Run. Each call closes one observation
// interval: LossPct and DiscardPct cover the span since the previous Metrics
// call (telemetry boundaries, per the flow sampler pattern) and feed the
// E-model's Ppl for this interval's R/MOS-CQ, while the packet counters,
// jitter, media gaps, and the remote XR view are cumulative/current values.
// Sessions that are not scoring reception (SendOnly, or an answerer that has
// not latched) return transport counters with zero quality fields.
func (m *MediaSession) Metrics() metrics.VoIP {
	m.mu.Lock()
	defer m.mu.Unlock()

	v := metrics.VoIP{
		Codec:         m.cfg.Codec.Name,
		TxPackets:     m.txPkts,
		OWDMethod:     "none",
		RemoteRFactor: m.remoteR,
		RemoteMOSCQ:   m.remoteMOS,
		RemoteBye:     m.remoteBye,
	}
	if est := m.owdLocked(); est.Valid {
		v.OWDMs = float64(est.Value) / float64(time.Millisecond)
		v.OWDErrMs = float64(est.ErrBound) / float64(time.Millisecond)
		v.OWDMethod = est.Method.String()
	}
	if m.haveRTT {
		v.RTTMs = float64(m.rtt) / float64(time.Millisecond)
	}
	if m.stats == nil {
		m.discPrior = m.discards
		return v
	}

	cum := m.stats.Cumulative()
	iv := m.stats.Interval()
	v.RxPackets = cum.Received
	v.Duplicates = cum.Duplicates
	v.Reordered = cum.Reordered
	if cum.CumulativeLost > 0 {
		v.Lost = uint64(cum.CumulativeLost)
	}
	v.JitterMs = cum.JitterMs
	v.MediaGaps = cum.MediaGaps
	v.LossPct = iv.FractionLost * 100
	ivDisc := m.discards - m.discPrior
	m.discPrior = m.discards
	if iv.Expected > 0 {
		v.DiscardPct = float64(ivDisc) / float64(iv.Expected) * 100
	}
	if !m.scoring {
		return v
	}

	gm := m.gil.Metrics()
	v.BurstR = gm.BurstR
	if res, ok := m.scoreLocked(v.LossPct+v.DiscardPct, gm.BurstR); ok {
		v.RFactor = res.R
		v.MOSCQ = res.MOSCQ
		v.EModel = res.C
	}
	return v
}

// CumulativeMetrics assembles a whole-call metrics.VoIP snapshot: LossPct,
// DiscardPct, and the R/MOS-CQ scored from them cover the entire call rather
// than one observation interval. Unlike Metrics it does NOT close an
// observation interval (no interval state is mutated), so it is safe to call
// at any time — the agent uses it for the final telemetry sample and `loom
// rtp` for its summary, where a trailing-fragment score would misrepresent
// the run (a 60s call with mid-call loss must not summarize as its last clean
// 200ms).
func (m *MediaSession) CumulativeMetrics() metrics.VoIP {
	m.mu.Lock()
	defer m.mu.Unlock()

	v := metrics.VoIP{
		Codec:         m.cfg.Codec.Name,
		TxPackets:     m.txPkts,
		OWDMethod:     "none",
		RemoteRFactor: m.remoteR,
		RemoteMOSCQ:   m.remoteMOS,
		RemoteBye:     m.remoteBye,
	}
	if est := m.owdLocked(); est.Valid {
		v.OWDMs = float64(est.Value) / float64(time.Millisecond)
		v.OWDErrMs = float64(est.ErrBound) / float64(time.Millisecond)
		v.OWDMethod = est.Method.String()
	}
	if m.haveRTT {
		v.RTTMs = float64(m.rtt) / float64(time.Millisecond)
	}
	if m.stats == nil {
		return v
	}

	cum := m.stats.Cumulative()
	v.RxPackets = cum.Received
	v.Duplicates = cum.Duplicates
	v.Reordered = cum.Reordered
	if cum.CumulativeLost > 0 {
		v.Lost = uint64(cum.CumulativeLost)
	}
	v.JitterMs = cum.JitterMs
	v.MediaGaps = cum.MediaGaps
	v.LossPct = cum.FractionLost * 100
	if cum.Expected > 0 {
		v.DiscardPct = float64(m.discards) / float64(cum.Expected) * 100
	}
	if !m.scoring {
		return v
	}

	gm := m.gil.Metrics()
	v.BurstR = gm.BurstR
	if res, ok := m.scoreLocked(v.LossPct+v.DiscardPct, gm.BurstR); ok {
		v.RFactor = res.R
		v.MOSCQ = res.MOSCQ
		v.EModel = res.C
	}
	return v
}
