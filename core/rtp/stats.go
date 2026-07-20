// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import "time"

// RFC 3550 Appendix A.1 constants, by their RFC names.
const (
	minSequential = 2          // MIN_SEQUENTIAL: in-order packets before a source is valid
	maxDropout    = 3000       // MAX_DROPOUT: largest believable forward jump
	maxMisorder   = 100        // MAX_MISORDER: largest believable backward straggle
	seqMod        = 1 << 16    // RTP_SEQ_MOD
	badSeqNone    = seqMod + 1 // sentinel: no pending re-init candidate
)

// windowSize is the dedupe bitmap span in packets. It exceeds MAX_DROPOUT so
// an accepted in-order advance clears at most one window of slots, and it
// dwarfs MAX_MISORDER so every packet the misorder branch can accept has a
// live slot.
const windowSize = 4096

// pktClass is the A.1 disposition of one observed packet.
type pktClass int

const (
	pktDropped   pktClass = iota // probation or unconfirmed jump: not counted
	pktInOrder                   // advanced the extended max (possibly past losses)
	pktReordered                 // arrived within MAX_MISORDER behind the max
	pktDuplicate                 // already seen
	pktResync                    // counted, but after a base re-init (sender restart)
)

// Gap is a hole in media arrival: more than 3·ptime of silence between
// consecutive counted packets while packets were expected. PacketsLost is the
// number of sequence numbers missing inside the gap (0 when the gap was pure
// delay, when the stream re-initialized across it, or when the packet closing
// it was a straggler rather than an in-order advance).
type Gap struct {
	Start, End  time.Time
	PacketsLost uint32
}

// RxSnapshot is a receiver-statistics summary, either cumulative or for one
// interval (see ReceiverStats.Interval and ReceiverStats.Cumulative).
type RxSnapshot struct {
	// Received counts packets accepted by the A.1 state machine, INCLUDING
	// duplicates — that is the RFC's definition, and it is what makes
	// CumulativeLost go negative under duplication.
	Received uint64
	// Duplicates counts packets whose extended sequence number was already
	// seen; Reordered counts packets that arrived behind the extended max
	// but were new. Both are also counted in Received.
	Duplicates, Reordered uint64
	// PayloadOctets sums the RTP payload bytes (as fed to Observe) of every
	// counted packet, duplicates and reordered included — the receiver-side
	// throughput primitive, and a cross-check against the sender's SR octet
	// count.
	PayloadOctets uint64
	// Expected is A.3's extended_max − base_seq + 1 (the delta for interval
	// snapshots).
	Expected uint64
	// CumulativeLost is the signed A.3 loss, Expected − Received, since the
	// source validated. It is NOT clamped here; the 24-bit wire clamp
	// happens only in Report.
	CumulativeLost int64
	// FractionLost is the loss fraction in [0,1] — per-interval for
	// Interval, lifetime for Cumulative — floored at 0 when duplication
	// makes the loss negative.
	FractionLost float64
	// ExtHighestSeq is the extended highest sequence number received
	// (cycles + max_seq), as the RTCP RR field truncates it.
	ExtHighestSeq uint32
	// JitterTicks is the A.8 interarrival jitter estimate in RTP timestamp
	// units (the raw RTCP RR field: the 16× internal state >> 4).
	JitterTicks uint32
	// JitterMs is the same estimate as milliseconds: J/clockRate·1000,
	// computed from the full-precision 16× state.
	JitterMs float64
	// MaxGap is the longest interarrival gap between counted packets — the
	// media-gap primitive (per-interval for Interval snapshots).
	MaxGap time.Duration
	// MediaGaps lists the arrival gaps longer than 3·ptime (all gaps for
	// Cumulative; gaps since the previous call for Interval).
	MediaGaps []Gap
}

// ReportBlockData carries the receiver-side fields an RTCP receiver-report
// block needs (RFC 3550 §6.4.1); the rtcp package supplies SSRC, LSR, and
// DLSR. Each call closes one report interval: FractionLost covers the span
// since the previous Report call.
type ReportBlockData struct {
	// FractionLost is the per-interval loss in 8-bit fixed point
	// (lost·256/expected), 0 when the interval loss is negative (A.3).
	FractionLost uint8
	// CumulativeLost is the signed A.3 cumulative loss clamped to the
	// 24-bit wire range [−0x800000, 0x7FFFFF] — the clamp exists ONLY here,
	// at wire time.
	CumulativeLost int32
	// ExtHighestSeq is cycles + max_seq.
	ExtHighestSeq uint32
	// Jitter is the A.8 estimate in timestamp units (16× state >> 4).
	Jitter uint32
}

// ReceiverStats tracks one RTP source exactly as RFC 3550 Appendix A
// prescribes: the A.1 sequence state machine (probation, cycle counting,
// re-init on big jumps), A.3 signed loss accounting, and the A.8 fixed-point
// jitter estimator, plus loom's media-gap primitive. Feed every packet of
// the source to Observe; read RTCP report fields from Report and telemetry
// snapshots from Interval/Cumulative. Not safe for concurrent use.
//
// A sender restart (detected via the A.1 two-packet re-init handshake)
// resets the loss baseline — Received, Expected, and the report priors start
// over — while Duplicates, Reordered, the jitter estimate, payload octets,
// and media gaps carry across, since they describe the path rather than the
// sequence numbering. The jitter filter's transit REFERENCE does not carry
// across: a restarted sender re-randomizes its RTP timestamp base (RFC 3550
// §5.1), so the first packet after a re-init only re-anchors the transit and
// no cross-restart difference ever reaches the estimator.
type ReceiverStats struct {
	clockRate uint32

	// A.1 state (RFC field names in comments).
	started    bool
	valid      bool   // probation passed at least once
	probation  int    // packets still required in order
	maxSeq     uint16 // max_seq
	cycles     uint64 // cycles, pre-multiplied by seqMod
	baseExt    uint64 // base_seq (extended; cycles is 0 at init)
	badSeq     uint32 // bad_seq
	received   uint64 // received (includes duplicates, per the RFC)
	duplicates uint64
	reordered  uint64
	octets     uint64

	// window is a bitmap over extended sequence numbers (mod windowSize)
	// distinguishing duplicates from late-but-new packets.
	window [windowSize / 64]uint64

	// A.8 state.
	t0          time.Time // arrival-tick anchor (first Observe)
	haveTransit bool
	transit     uint32 // last transit, RTP timestamp units (mod 2^32)
	lastTs      uint32 // RTP timestamp of the last packet fed to the filter
	jitter      uint32 // estimator state, 16× fixed point

	// Media-gap tracking.
	lastArrival time.Time
	lastExt     uint64 // extended max after the previous counted packet
	ptime       time.Duration
	maxGap      time.Duration
	ivMaxGap    time.Duration
	gaps        []Gap
	ivGapIdx    int

	// Report-interval priors (A.3 expected_prior/received_prior) and the
	// independent Interval-snapshot priors.
	repExpPrior, repRecvPrior                           uint64
	ivExpPrior, ivRecvPrior, ivDupPrior, ivReorderPrior uint64
	ivOctetsPrior                                       uint64
}

// NewReceiverStats returns statistics for one source whose RTP clock runs at
// clockRate Hz (the codec table's ClockRate — 48000 for Opus regardless of
// audio bandwidth, RFC 7587 §4.1). It panics on a zero clockRate, which
// would make every jitter conversion divide by zero.
func NewReceiverStats(clockRate uint32) *ReceiverStats {
	if clockRate == 0 {
		panic("rtp: NewReceiverStats with zero clock rate")
	}
	return &ReceiverStats{clockRate: clockRate, badSeq: badSeqNone}
}

// initSeq is RFC 3550 A.1 init_seq: reset the loss baseline to seq. The
// dedupe window resets with it; duplicate/reorder totals and the jitter
// estimate survive (they describe the path, not the numbering). Observe
// re-anchors the jitter transit reference when the re-init came from a
// resync, since the sender's timestamp base changed with the restart.
func (s *ReceiverStats) initSeq(seq uint16) {
	s.baseExt = uint64(seq)
	s.maxSeq = seq
	s.cycles = 0
	s.badSeq = badSeqNone
	s.received = 0
	s.repExpPrior, s.repRecvPrior = 0, 0
	s.ivExpPrior, s.ivRecvPrior = 0, 0
	s.window = [windowSize / 64]uint64{}
	s.setWindow(uint64(seq))
}

// updateSeq is RFC 3550 A.1 update_seq, returning the packet's extended
// sequence number and disposition.
func (s *ReceiverStats) updateSeq(seq uint16) (ext uint64, class pktClass) {
	udelta := seq - s.maxSeq
	if s.probation > 0 {
		// Source is not valid until minSequential packets arrive in order.
		if seq == s.maxSeq+1 {
			s.probation--
			s.maxSeq = seq
			if s.probation == 0 {
				s.initSeq(seq)
				s.received++
				s.valid = true
				return uint64(seq), pktInOrder
			}
		} else {
			s.probation = minSequential - 1
			s.maxSeq = seq
		}
		return 0, pktDropped
	}
	switch {
	case udelta == 0:
		// Duplicate of the current max.
		s.received++
		return s.cycles + uint64(seq), pktDuplicate
	case udelta < maxDropout:
		// In order, with a permissible gap.
		old := s.cycles + uint64(s.maxSeq)
		if seq < s.maxSeq {
			s.cycles += seqMod // sequence wrapped
		}
		s.maxSeq = seq
		ext = s.cycles + uint64(seq)
		s.advanceWindow(old, ext)
		s.received++
		return ext, pktInOrder
	case udelta <= seqMod-maxMisorder:
		// A very large jump, forward or ancient: believe it only when two
		// sequential packets confirm a restart.
		if uint32(seq) == s.badSeq {
			s.initSeq(seq)
			s.received++
			return uint64(seq), pktResync
		}
		s.badSeq = uint32(seq + 1)
		return 0, pktDropped
	default:
		// Within maxMisorder behind the max: reordered or duplicate.
		if seq > s.maxSeq {
			// Numerically above the max means the straggler predates the
			// last wrap; undo one cycle (best effort when none happened).
			if s.cycles >= seqMod {
				ext = s.cycles - seqMod + uint64(seq)
			} else {
				ext = uint64(seq)
			}
		} else {
			ext = s.cycles + uint64(seq)
		}
		s.received++
		if s.testWindow(ext) {
			return ext, pktDuplicate
		}
		s.setWindow(ext)
		return ext, pktReordered
	}
}

func (s *ReceiverStats) setWindow(ext uint64) {
	i := ext % windowSize
	s.window[i/64] |= 1 << (i % 64)
}

func (s *ReceiverStats) clearWindow(ext uint64) {
	i := ext % windowSize
	s.window[i/64] &^= 1 << (i % 64)
}

func (s *ReceiverStats) testWindow(ext uint64) bool {
	i := ext % windowSize
	return s.window[i/64]&(1<<(i%64)) != 0
}

// advanceWindow moves the max from oldExt to newExt: slots for the skipped
// (lost) sequence numbers are cleared so a late arrival reads as reordered,
// not duplicate, and newExt's slot is set.
func (s *ReceiverStats) advanceWindow(oldExt, newExt uint64) {
	if newExt-oldExt >= windowSize {
		s.window = [windowSize / 64]uint64{}
	} else {
		for e := oldExt + 1; e < newExt; e++ {
			s.clearWindow(e)
		}
	}
	s.setWindow(newExt)
}

// arrivalTicks converts an arrival time to RTP timestamp units modulo 2^32,
// anchored at the first observed arrival (A.8 needs only differences, so the
// anchor is arbitrary; anchoring avoids overflowing absolute wall time ×
// clock rate).
func (s *ReceiverStats) arrivalTicks(t time.Time) uint32 {
	d := t.Sub(s.t0)
	sec := int64(d / time.Second)
	rem := int64(d % time.Second)
	return uint32(sec*int64(s.clockRate) + rem*int64(s.clockRate)/int64(time.Second))
}

// Observe feeds one packet of this source: its parsed header, payload length
// in bytes, and arrival time (as close to the wire as the caller can stamp).
// Packets rejected by the A.1 state machine (probation, unconfirmed jumps)
// update only the state machine.
func (s *ReceiverStats) Observe(h Header, payloadLen int, arrival time.Time) {
	if s.t0.IsZero() {
		s.t0 = arrival
	}
	if !s.started {
		// First packet from the source: A.1's "probably valid" entry state.
		s.started = true
		s.initSeq(h.SequenceNumber)
		s.maxSeq = h.SequenceNumber - 1
		s.probation = minSequential
	}
	ext, class := s.updateSeq(h.SequenceNumber)
	if class == pktDropped {
		return
	}
	switch class {
	case pktDuplicate:
		s.duplicates++
	case pktReordered:
		s.reordered++
	}
	if payloadLen > 0 {
		s.octets += uint64(payloadLen)
	}

	// Infer ptime (for the media-gap threshold) from the first adjacent
	// in-order pair: one sequence step whose timestamp advance is sane.
	if s.ptime == 0 && s.haveTransit && class == pktInOrder && ext == s.lastExt+1 {
		if tsd := h.Timestamp - s.lastTs; tsd > 0 && tsd <= s.clockRate {
			s.ptime = time.Duration(uint64(tsd) * uint64(time.Second) / uint64(s.clockRate))
		}
	}

	// Media-gap primitive: interarrival gaps between counted packets.
	if !s.lastArrival.IsZero() {
		gap := arrival.Sub(s.lastArrival)
		if gap > s.maxGap {
			s.maxGap = gap
		}
		if gap > s.ivMaxGap {
			s.ivMaxGap = gap
		}
		if s.ptime > 0 && gap > 3*s.ptime {
			// Only an in-order advance defines a trustworthy hole: after a
			// resync the numbering is not comparable across the gap, and a
			// straggler predating a wrap that never happened (the misorder
			// branch's no-cycle fallback) can carry ext far above the max
			// without any packet having been lost.
			var lost uint32
			if class == pktInOrder && ext > s.lastExt+1 {
				lost = uint32(ext - s.lastExt - 1)
			}
			s.gaps = append(s.gaps, Gap{Start: s.lastArrival, End: arrival, PacketsLost: lost})
		}
	}

	// A.8 jitter, in RTP timestamp units on 16× fixed-point state.
	// Equal-timestamp packets are excluded: their spacing is transport
	// artifact (fragments of one frame), not interarrival jitter.
	if class == pktResync {
		// The re-init handshake means the sender restarted and re-randomized
		// its RTP timestamp base (RFC 3550 §5.1). The stored transit was
		// measured against the dead base, so differencing across the restart
		// would inject an arbitrary-magnitude |D| into the filter (which
		// then decays only by 15/16 per packet). Re-anchor the transit; the
		// jitter ESTIMATE itself carries across — it describes the path.
		s.haveTransit = false
	}
	if !s.haveTransit || h.Timestamp != s.lastTs {
		transit := s.arrivalTicks(arrival) - h.Timestamp
		if s.haveTransit {
			d := int64(int32(transit - s.transit))
			if d < 0 {
				d = -d
			}
			s.jitter += uint32(d) - (s.jitter+8)>>4
		}
		s.transit = transit
		s.lastTs = h.Timestamp
		s.haveTransit = true
	}

	s.lastArrival = arrival
	s.lastExt = s.cycles + uint64(s.maxSeq)
}

// expected is A.3: extended_max − base_seq + 1 (0 until the source
// validates).
func (s *ReceiverStats) expected() uint64 {
	if !s.valid {
		return 0
	}
	return s.cycles + uint64(s.maxSeq) - s.baseExt + 1
}

// extHighest is the RR-truncated extended highest sequence number.
func (s *ReceiverStats) extHighest() uint32 {
	if !s.valid {
		return 0
	}
	return uint32(s.cycles + uint64(s.maxSeq))
}

// jitterMs converts the 16× estimator state to milliseconds at full
// precision.
func (s *ReceiverStats) jitterMs() float64 {
	return float64(s.jitter) / 16 / float64(s.clockRate) * 1000
}

// Cumulative returns lifetime statistics. It never modifies interval state,
// so it may be called freely between Interval calls.
func (s *ReceiverStats) Cumulative() RxSnapshot {
	exp := s.expected()
	snap := RxSnapshot{
		Received:       s.received,
		Duplicates:     s.duplicates,
		Reordered:      s.reordered,
		PayloadOctets:  s.octets,
		Expected:       exp,
		CumulativeLost: int64(exp) - int64(s.received),
		ExtHighestSeq:  s.extHighest(),
		JitterTicks:    s.jitter >> 4,
		JitterMs:       s.jitterMs(),
		MaxGap:         s.maxGap,
		MediaGaps:      append([]Gap(nil), s.gaps...),
	}
	if exp > 0 && snap.CumulativeLost > 0 {
		snap.FractionLost = float64(snap.CumulativeLost) / float64(exp)
	}
	return snap
}

// Interval returns the delta since the previous Interval call (or since
// creation): received/duplicate/reorder counts, payload octets, expected
// packets, the per-interval loss fraction (0 when negative), the interval's
// longest arrival gap, and the media gaps that closed during the interval.
// CumulativeLost, ExtHighestSeq, and the jitter fields are current values —
// jitter is a filter, not a counter.
func (s *ReceiverStats) Interval() RxSnapshot {
	exp := s.expected()
	expInt := int64(exp) - int64(s.ivExpPrior)
	recvInt := int64(s.received) - int64(s.ivRecvPrior)
	if expInt < 0 {
		expInt = 0
	}
	if recvInt < 0 {
		recvInt = 0
	}
	snap := RxSnapshot{
		Received:       uint64(recvInt),
		Duplicates:     s.duplicates - s.ivDupPrior,
		Reordered:      s.reordered - s.ivReorderPrior,
		PayloadOctets:  s.octets - s.ivOctetsPrior,
		Expected:       uint64(expInt),
		CumulativeLost: int64(exp) - int64(s.received),
		ExtHighestSeq:  s.extHighest(),
		JitterTicks:    s.jitter >> 4,
		JitterMs:       s.jitterMs(),
		MaxGap:         s.ivMaxGap,
		MediaGaps:      append([]Gap(nil), s.gaps[s.ivGapIdx:]...),
	}
	if lost := expInt - recvInt; expInt > 0 && lost > 0 {
		snap.FractionLost = float64(lost) / float64(expInt)
	}
	s.ivExpPrior, s.ivRecvPrior = exp, s.received
	s.ivDupPrior, s.ivReorderPrior = s.duplicates, s.reordered
	s.ivOctetsPrior = s.octets
	s.ivGapIdx = len(s.gaps)
	s.ivMaxGap = 0
	return snap
}

// Report closes one RTCP report interval and returns the receiver-report
// fields per RFC 3550 §6.4.1/A.3: per-interval 8-bit fraction lost (0 when
// the interval loss is negative), cumulative lost clamped to the 24-bit wire
// range, extended highest sequence number, and jitter in timestamp units.
// Before the source passes probation it returns the zero value.
func (s *ReceiverStats) Report() ReportBlockData {
	if !s.valid {
		return ReportBlockData{}
	}
	exp := s.expected()
	expInt := int64(exp) - int64(s.repExpPrior)
	recvInt := int64(s.received) - int64(s.repRecvPrior)
	s.repExpPrior, s.repRecvPrior = exp, s.received
	var frac uint8
	if lost := expInt - recvInt; expInt > 0 && lost > 0 {
		f := (lost << 8) / expInt
		if f > 255 {
			f = 255
		}
		frac = uint8(f)
	}
	return ReportBlockData{
		FractionLost:   frac,
		CumulativeLost: clamp24(int64(exp) - int64(s.received)),
		ExtHighestSeq:  s.extHighest(),
		Jitter:         s.jitter >> 4,
	}
}

// clamp24 applies the A.3 wire clamp: cumulative lost is carried in a 24-bit
// signed field, [−0x800000, 0x7FFFFF]. The clamp exists only at report time;
// internal accounting stays fully signed.
func clamp24(lost int64) int32 {
	switch {
	case lost > 0x7FFFFF:
		return 0x7FFFFF
	case lost < -0x800000:
		return -0x800000
	default:
		return int32(lost)
	}
}
