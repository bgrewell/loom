// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package gilbert estimates packet-loss burstiness online. It fits a two-state
// Markov (Gilbert) loss model over the per-packet loss/receive sequence and
// derives the RFC 3611 §4.7.2 burst/gap metrics, so one estimator feeds both
// RTCP XR VoIP-metrics emission (RFC 3611 §4.7) and the E-model's Ie,eff
// burst adjustment (ITU-T G.113/G.107).
//
// Gilbert model: p = P(loss | previous packet received) and q = P(received |
// previous packet lost), estimated from transition counts. BurstR = 1/(p+q)
// is 1 for random (Bernoulli) loss and grows with burstiness; it is clamped
// to ≥ 1, and degenerate streams whose transitions identify neither rate
// (no observations, all received, or all lost) report BurstR = 1.
//
// RFC 3611 §4.7.2 semantics, implemented exactly: a burst is the longest
// sequence that (a) starts with a lost or discarded packet, (b) does not
// contain any run of Gmin or more consecutively received (and not discarded)
// packets, and (c) ends with a lost or discarded packet. Gaps are the
// complement: session start to the first burst, the periods between bursts,
// and the last burst to report time. For classifying losses near the edges,
// the session is assumed to be preceded and followed by at least Gmin
// received packets — so an isolated loss with Gmin or more received packets
// on both sides falls within a gap, giving the RFC's maximum gap loss rate
// of 1/(Gmin+1), and every burst contains at least two losses. Burst/gap
// density is the fraction of packets expected within the respective periods
// that were lost; burst/gap duration is the mean duration of the respective
// periods. "Lost" here means lost or discarded in the RFC 3611 sense — the
// caller reports jitter-buffer discards as losses too.
//
// Period durations follow the RFC's per-packet accounting: every observation
// occupies one packet slot, so a period's duration is its slot count times
// the packet time, estimated as the mean inter-observation spacing
// (lastAt − firstAt)/(n − 1). Observe is called once per playout slot at the
// media cadence, so the estimate equals ptime for RTP traffic without the
// estimator having to be told it. (Measuring first-loss-to-last-loss
// timestamps instead would drop the final packet's own slot from every
// period — a minimal two-loss burst would report one packet time, not two.)
// Durations are zero while the spacing is unknown (fewer than two
// observations).
//
// The estimator is pure computation — no I/O, no clock — and, like the other
// measurement types in core, is not safe for concurrent use.
package gilbert

import "time"

// DefaultGmin is the RECOMMENDED gap threshold from RFC 3611 §4.7.2: a run of
// at least 16 received packets separates losses into distinct bursts (or
// leaves them isolated within gaps).
const DefaultGmin = 16

// phase tracks where the burst/gap state machine is between observations.
type phase int

const (
	// phaseGap: inside a gap with no unresolved loss. Any loss arriving now
	// is at least Gmin received packets after the previous one (or after the
	// assumed pre-session padding).
	phaseGap phase = iota
	// phasePending: one loss seen; whether it is an isolated gap loss or the
	// start of a burst depends on whether Gmin received packets or another
	// loss arrives first.
	phasePending
	// phaseBurst: inside a burst (two or more losses, each separated by
	// fewer than Gmin received packets). The burst ends at its last loss
	// once Gmin received packets follow it.
	phaseBurst
)

// Metrics is a snapshot of the estimator's model fit and RFC 3611 burst/gap
// measures. Densities are fractions in [0, 1] (the XR emitter scales by 256);
// durations are means over the completed periods, zero when none occurred.
type Metrics struct {
	// P is the estimated P(loss | previous packet received).
	P float64
	// Q is the estimated P(received | previous packet lost).
	Q float64
	// BurstR is 1/(P+Q) clamped to ≥ 1: 1 = random loss, > 1 = bursty.
	// Degenerate streams (no observations, all received, all lost) report 1.
	BurstR float64
	// BurstDensity is the fraction of packets within burst periods that were
	// lost (RFC 3611 §4.7.2), 0 when no burst has occurred.
	BurstDensity float64
	// GapDensity is the fraction of packets within gap periods that were
	// lost, 0 when no packets fell within gaps.
	GapDensity float64
	// BurstDuration is the mean duration of the burst periods observed:
	// mean packets-per-burst × the mean inter-observation spacing.
	BurstDuration time.Duration
	// GapDuration is the mean duration of the gap periods observed, on the
	// same per-packet-slot accounting as BurstDuration.
	GapDuration time.Duration
}

// Estimator fits the Gilbert model and RFC 3611 burst/gap metrics from a
// stream of per-packet loss observations in playout order. Feed it with
// Observe and read it with Metrics at telemetry boundaries; observing may
// continue afterwards.
type Estimator struct {
	gmin int

	// Observation extent; (lastAt − firstAt)/(n − 1) is the mean packet-slot
	// time that converts period slot counts into durations.
	n       uint64
	firstAt time.Time
	lastAt  time.Time

	// Two-state Markov transition counts over consecutive observations.
	prevValid          bool
	prevLost           bool
	nRR, nRL, nLR, nLL uint64

	// Burst/gap state machine.
	ph  phase
	run int // consecutively received packets since the last loss

	burstLost uint64 // phaseBurst: losses in the open burst
	burstPkts uint64 // phaseBurst: packets from first through last loss

	// Committed totals.
	gapLost, gapTotal    uint64
	burstLostTot         uint64
	burstTotalTot        uint64
	curGapPkts           uint64 // packets committed to the current gap
	gapCount, burstCount uint64
}

// New returns an Estimator using the given Gmin threshold; gmin values less
// than 1 select DefaultGmin (16, the RFC 3611 recommendation).
func New(gmin int) *Estimator {
	if gmin < 1 {
		gmin = DefaultGmin
	}
	return &Estimator{gmin: gmin}
}

// Observe records one packet slot in playout order: lost is true when the
// packet was lost or discarded (RFC 3611 counts jitter-buffer discards with
// losses), and at is the observation time used for period durations. Times
// must be non-decreasing across calls.
func (e *Estimator) Observe(lost bool, at time.Time) {
	if e.n == 0 {
		e.firstAt = at
	}
	e.n++
	e.lastAt = at

	if e.prevValid {
		switch {
		case !e.prevLost && !lost:
			e.nRR++
		case !e.prevLost && lost:
			e.nRL++
		case e.prevLost && !lost:
			e.nLR++
		default:
			e.nLL++
		}
	}
	e.prevValid, e.prevLost = true, lost

	if lost {
		e.observeLost()
	} else {
		e.observeReceived()
	}
}

// observeLost advances the burst/gap machine for a lost/discarded packet.
func (e *Estimator) observeLost() {
	switch e.ph {
	case phaseGap:
		// First loss after ≥ Gmin received (or session start): burst vs
		// isolated gap loss is not yet decidable.
		e.ph = phasePending
	case phasePending:
		// A second loss within Gmin of the pending one: a burst begins at
		// the pending loss, and the received packets between the two losses
		// are inside it. The current gap ends where the burst starts.
		e.closeGap()
		e.ph = phaseBurst
		e.burstLost = 2
		e.burstPkts = uint64(e.run) + 2
	case phaseBurst:
		e.burstLost++
		e.burstPkts += uint64(e.run) + 1
	}
	e.run = 0
}

// observeReceived advances the burst/gap machine for a received packet.
// Received packets are committed to the gap immediately when no loss is
// unresolved; otherwise they stay deferred in the run counter until the run
// either reaches Gmin (gap) or a loss claims them for a burst.
func (e *Estimator) observeReceived() {
	e.run++
	switch e.ph {
	case phaseGap:
		e.gapTotal++
		e.curGapPkts++
	case phasePending:
		if e.run >= e.gmin {
			// Gmin received after the pending loss: it was an isolated loss
			// inside the continuing gap.
			e.gapLost++
			e.gapTotal += uint64(e.run) + 1
			e.curGapPkts += uint64(e.run) + 1
			e.ph = phaseGap
		}
	case phaseBurst:
		if e.run >= e.gmin {
			// The burst ended at its last loss; this run opens the next gap.
			e.commitBurst()
			e.curGapPkts = uint64(e.run)
			e.gapTotal += uint64(e.run)
			e.ph = phaseGap
		}
	}
}

// closeGap commits the current gap period. Gaps that contain no packets (a
// burst starting at the first observation) are not counted.
func (e *Estimator) closeGap() {
	if e.curGapPkts > 0 {
		e.gapCount++
	}
	e.curGapPkts = 0
}

// commitBurst commits the open burst period.
func (e *Estimator) commitBurst() {
	e.burstLostTot += e.burstLost
	e.burstTotalTot += e.burstPkts
	e.burstCount++
	e.burstLost, e.burstPkts = 0, 0
}

// Metrics returns the current model fit and burst/gap measures. Unresolved
// state is settled per the RFC 3611 §4.7.2 edge rule — the report time is
// assumed to be followed by at least Gmin received packets, so a pending loss
// counts as an isolated gap loss and an open burst ends at its last loss —
// without mutating the estimator; observation may continue afterwards.
// Before any observation it returns the zero value with BurstR = 1.
func (e *Estimator) Metrics() Metrics {
	if e.n == 0 {
		return Metrics{BurstR: 1}
	}

	gapLost, gapTotal := e.gapLost, e.gapTotal
	burstLost, burstTotal := e.burstLostTot, e.burstTotalTot
	gapCount, burstCount := e.gapCount, e.burstCount
	curGapPkts := e.curGapPkts

	switch e.ph {
	case phasePending:
		// Assumed followed by ≥ Gmin received: isolated loss within the gap.
		gapLost++
		gapTotal += uint64(e.run) + 1
		curGapPkts += uint64(e.run) + 1
	case phaseBurst:
		// Assumed followed by ≥ Gmin received: the burst ends at its last
		// loss and the trailing run belongs to a final gap.
		burstLost += e.burstLost
		burstTotal += e.burstPkts
		burstCount++
		gapTotal += uint64(e.run)
		curGapPkts = uint64(e.run)
	}
	if curGapPkts > 0 {
		gapCount++
	}

	var m Metrics
	if d := e.nRR + e.nRL; d > 0 {
		m.P = float64(e.nRL) / float64(d)
	}
	if d := e.nLR + e.nLL; d > 0 {
		m.Q = float64(e.nLR) / float64(d)
	}
	m.BurstR = 1
	if s := m.P + m.Q; s > 0 {
		if r := 1 / s; r > 1 {
			m.BurstR = r
		}
	}
	if burstTotal > 0 {
		m.BurstDensity = float64(burstLost) / float64(burstTotal)
	}
	if gapTotal > 0 {
		m.GapDensity = float64(gapLost) / float64(gapTotal)
	}
	// Durations are slot counts × the mean inter-observation spacing (the
	// RFC's per-packet accounting; see the package comment). With fewer than
	// two observations the spacing — and thus any duration — is unknown.
	var spacing float64
	if e.n > 1 {
		spacing = float64(e.lastAt.Sub(e.firstAt)) / float64(e.n-1)
	}
	if burstCount > 0 {
		m.BurstDuration = time.Duration(float64(burstTotal) / float64(burstCount) * spacing)
	}
	if gapCount > 0 {
		m.GapDuration = time.Duration(float64(gapTotal) / float64(gapCount) * spacing)
	}
	return m
}
