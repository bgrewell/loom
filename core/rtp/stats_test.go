// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import (
	"math"
	"testing"
	"time"
)

var statsBase = time.Unix(1700000000, 0)

// at returns statsBase + m milliseconds.
func at(m int) time.Time { return statsBase.Add(time.Duration(m) * time.Millisecond) }

// obs feeds one 160-byte pcmu-shaped packet.
func obs(s *ReceiverStats, seq uint16, ts uint32, arrival time.Time) {
	s.Observe(Header{SequenceNumber: seq, Timestamp: ts, SSRC: 1}, 160, arrival)
}

// obsRun feeds count in-order packets starting at seq, 20 ms / 160 ticks
// apart, starting at the given millisecond offset.
func obsRun(s *ReceiverStats, seq uint16, ts uint32, startMs, count int) {
	for i := 0; i < count; i++ {
		obs(s, seq+uint16(i), ts+uint32(160*i), at(startMs+20*i))
	}
}

// TestProbation pins A.1's MIN_SEQUENTIAL=2 probation: nothing is counted
// until two in-order packets arrive, the first packet is never counted, and
// a non-consecutive packet restarts probation.
func TestProbation(t *testing.T) {
	t.Run("single packet not counted", func(t *testing.T) {
		s := NewReceiverStats(8000)
		obs(s, 10, 0, at(0))
		c := s.Cumulative()
		if c.Received != 0 || c.Expected != 0 || c.CumulativeLost != 0 {
			t.Errorf("after one packet: Received=%d Expected=%d Lost=%d, want all 0",
				c.Received, c.Expected, c.CumulativeLost)
		}
		if r := s.Report(); r != (ReportBlockData{}) {
			t.Errorf("Report during probation = %+v, want zero value", r)
		}
	})

	t.Run("two in-order validates; first stays uncounted", func(t *testing.T) {
		s := NewReceiverStats(8000)
		obs(s, 10, 0, at(0))
		obs(s, 11, 160, at(20))
		c := s.Cumulative()
		if c.Received != 1 || c.Expected != 1 {
			t.Errorf("Received=%d Expected=%d, want 1/1 (base is the second packet)", c.Received, c.Expected)
		}
		if c.ExtHighestSeq != 11 {
			t.Errorf("ExtHighestSeq = %d, want 11", c.ExtHighestSeq)
		}
	})

	t.Run("non-consecutive packet restarts probation", func(t *testing.T) {
		s := NewReceiverStats(8000)
		obs(s, 10, 0, at(0))
		obs(s, 20, 160, at(20)) // not 11: probation restarts at 20
		if c := s.Cumulative(); c.Received != 0 {
			t.Fatalf("Received = %d after broken probation, want 0", c.Received)
		}
		obs(s, 21, 320, at(40)) // in order with 20: validates
		c := s.Cumulative()
		if c.Received != 1 || c.ExtHighestSeq != 21 {
			t.Errorf("Received=%d ExtHighestSeq=%d, want 1/21", c.Received, c.ExtHighestSeq)
		}
	})
}

// TestSeqWrapCycleCounting pins 16→32-bit extension across the 65535→0 wrap,
// with and without loss at the wrap.
func TestSeqWrapCycleCounting(t *testing.T) {
	t.Run("clean wrap", func(t *testing.T) {
		s := NewReceiverStats(8000)
		for i, seq := range []uint16{65533, 65534, 65535, 0, 1, 2} {
			obs(s, seq, uint32(160*i), at(20*i))
		}
		c := s.Cumulative()
		if want := uint32(65536 + 2); c.ExtHighestSeq != want {
			t.Errorf("ExtHighestSeq = %d, want %d (one cycle counted)", c.ExtHighestSeq, want)
		}
		if c.Received != 5 || c.Expected != 5 || c.CumulativeLost != 0 {
			t.Errorf("Received=%d Expected=%d Lost=%d, want 5/5/0", c.Received, c.Expected, c.CumulativeLost)
		}
	})

	t.Run("loss across the wrap", func(t *testing.T) {
		s := NewReceiverStats(8000)
		for i, seq := range []uint16{65533, 65534, 65535, 1, 2} { // 0 lost
			obs(s, seq, uint32(160*i), at(20*i))
		}
		c := s.Cumulative()
		if want := uint32(65536 + 2); c.ExtHighestSeq != want {
			t.Errorf("ExtHighestSeq = %d, want %d", c.ExtHighestSeq, want)
		}
		if c.Expected != 5 || c.Received != 4 || c.CumulativeLost != 1 {
			t.Errorf("Expected=%d Received=%d Lost=%d, want 5/4/1", c.Expected, c.Received, c.CumulativeLost)
		}
	})
}

// TestJitterVector pins the A.8 fixed-point estimator against a hand-computed
// trace at an 8 kHz clock (ptime 20 ms → 160 ticks/packet):
//
//	pkt  ts    arrival  transit  |D|  J16 update            J16
//	100  1000    0 ms   (probation — not counted)
//	101  1160   20 ms   −1000     —   first sample            0
//	102  1320   45 ms    −960    40   +40 − (0+8)>>4 = +40   40
//	103  1480   60 ms   −1000    40   +40 − (48)>>4  = +37   77
//	104  1640   80 ms   −1000     0    +0 − (85)>>4  = −5    72
//
// Reported jitter is J16>>4 = 4 ticks; JitterMs is the full-precision
// 72/16/8000·1000 = 0.5625 ms.
func TestJitterVector(t *testing.T) {
	s := NewReceiverStats(8000)
	arrivalsMs := []int{0, 20, 45, 60, 80}
	for i, ms := range arrivalsMs {
		obs(s, uint16(100+i), uint32(1000+160*i), at(ms))
	}
	c := s.Cumulative()
	if c.JitterTicks != 4 {
		t.Errorf("JitterTicks = %d, want 4", c.JitterTicks)
	}
	if math.Abs(c.JitterMs-0.5625) > 1e-9 {
		t.Errorf("JitterMs = %v, want 0.5625", c.JitterMs)
	}
	if r := s.Report(); r.Jitter != 4 {
		t.Errorf("Report().Jitter = %d, want 4", r.Jitter)
	}
}

// TestJitterEqualTimestampExcluded pins the A.8 exclusion: a packet whose
// RTP timestamp equals the previous packet's (a fragment of the same frame)
// must not move the estimator, even when its arrival spacing would.
func TestJitterEqualTimestampExcluded(t *testing.T) {
	s := NewReceiverStats(8000)
	obs(s, 1, 0, at(0))
	obs(s, 2, 160, at(20))
	obs(s, 3, 320, at(40))
	obs(s, 4, 320, at(48)) // same timestamp, 8 ms late: excluded
	obs(s, 5, 480, at(60))
	c := s.Cumulative()
	if c.JitterTicks != 0 || c.JitterMs != 0 {
		t.Errorf("JitterTicks=%d JitterMs=%v after equal-ts packet, want 0/0", c.JitterTicks, c.JitterMs)
	}
	if c.Received != 4 {
		t.Errorf("Received = %d, want 4 (the equal-ts packet still counts)", c.Received)
	}
}

// TestReorderedCounted pins that a packet within MAX_MISORDER behind the max
// is accepted, counted in Received, and tallied as reordered — no loss.
func TestReorderedCounted(t *testing.T) {
	s := NewReceiverStats(8000)
	for i, seq := range []uint16{1, 2, 3, 5, 6} {
		obs(s, seq, uint32(160*i), at(20*i))
	}
	obs(s, 4, 480, at(100)) // late straggler
	c := s.Cumulative()
	if c.Reordered != 1 || c.Duplicates != 0 {
		t.Errorf("Reordered=%d Duplicates=%d, want 1/0", c.Reordered, c.Duplicates)
	}
	if c.Expected != 5 || c.Received != 5 || c.CumulativeLost != 0 {
		t.Errorf("Expected=%d Received=%d Lost=%d, want 5/5/0", c.Expected, c.Received, c.CumulativeLost)
	}
}

// TestMaxMisorderBoundary pins the A.1 boundary: 99 behind the max is a
// countable straggler; exactly MAX_MISORDER=100 behind is treated as a big
// jump and dropped pending the two-packet re-init handshake.
func TestMaxMisorderBoundary(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 1, 0, 0, 2) // base 2
	obs(s, 1000, 320, at(40))
	obs(s, 901, 480, at(60)) // 99 behind: reordered, counted
	obs(s, 900, 640, at(80)) // 100 behind: dropped
	obs(s, 1001, 800, at(100))
	c := s.Cumulative()
	if c.Reordered != 1 {
		t.Errorf("Reordered = %d, want 1 (only the 99-behind packet)", c.Reordered)
	}
	if c.Received != 4 {
		t.Errorf("Received = %d, want 4 (the 100-behind packet is dropped)", c.Received)
	}
	if want := uint64(1000); c.Expected != want {
		t.Errorf("Expected = %d, want %d", c.Expected, want)
	}
}

// TestDuplicatesNegativeLost pins A.3's signed loss: duplicates count as
// received, driving cumulative lost negative; the snapshot keeps the
// negative value, fraction lost floors at 0, and only Report clamps for the
// wire (where −2 still fits).
func TestDuplicatesNegativeLost(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 1, 0, 0, 3) // base 2: expected 2 (seqs 2,3)
	obs(s, 3, 320, at(60))
	obs(s, 3, 320, at(80))
	c := s.Cumulative()
	if c.Duplicates != 2 {
		t.Errorf("Duplicates = %d, want 2", c.Duplicates)
	}
	if c.Received != 4 || c.Expected != 2 {
		t.Errorf("Received=%d Expected=%d, want 4/2", c.Received, c.Expected)
	}
	if c.CumulativeLost != -2 {
		t.Errorf("CumulativeLost = %d, want -2 (signed, unclamped)", c.CumulativeLost)
	}
	if c.FractionLost != 0 {
		t.Errorf("FractionLost = %v, want 0 for negative loss", c.FractionLost)
	}
	r := s.Report()
	if r.CumulativeLost != -2 {
		t.Errorf("Report().CumulativeLost = %d, want -2", r.CumulativeLost)
	}
	if r.FractionLost != 0 {
		t.Errorf("Report().FractionLost = %d, want 0", r.FractionLost)
	}
}

// TestDuplicateOfStraggler pins duplicate detection away from the max: a
// second copy of an already-received non-max packet is a duplicate, not a
// reorder.
func TestDuplicateOfStraggler(t *testing.T) {
	s := NewReceiverStats(8000)
	for i, seq := range []uint16{1, 2, 3, 4} {
		obs(s, seq, uint32(160*i), at(20*i))
	}
	obs(s, 3, 320, at(80)) // copy of 3, behind the max
	c := s.Cumulative()
	if c.Duplicates != 1 || c.Reordered != 0 {
		t.Errorf("Duplicates=%d Reordered=%d, want 1/0", c.Duplicates, c.Reordered)
	}
}

// TestBigJumpReinit pins MAX_DROPOUT handling: a jump ≥3000 is dropped, two
// sequential jumped packets re-initialize the loss baseline (sender
// restart), and a second non-sequential jump keeps the source parked.
func TestBigJumpReinit(t *testing.T) {
	t.Run("consecutive confirmation re-inits", func(t *testing.T) {
		s := NewReceiverStats(8000)
		obsRun(s, 1, 0, 0, 3) // base 2, received 2
		obs(s, 50000, 480, at(60))
		if c := s.Cumulative(); c.Received != 2 {
			t.Fatalf("Received = %d after unconfirmed jump, want 2", c.Received)
		}
		obs(s, 50001, 640, at(80)) // confirms: re-init at 50001
		obs(s, 50002, 800, at(100))
		c := s.Cumulative()
		if c.Received != 2 || c.Expected != 2 || c.CumulativeLost != 0 {
			t.Errorf("after re-init: Received=%d Expected=%d Lost=%d, want 2/2/0 (baseline reset)",
				c.Received, c.Expected, c.CumulativeLost)
		}
		if c.ExtHighestSeq != 50002 {
			t.Errorf("ExtHighestSeq = %d, want 50002", c.ExtHighestSeq)
		}
	})

	t.Run("non-consecutive jumps stay dropped", func(t *testing.T) {
		s := NewReceiverStats(8000)
		obsRun(s, 1, 0, 0, 3)
		obs(s, 50000, 480, at(60))
		obs(s, 60000, 640, at(80)) // not 50001: still parked
		c := s.Cumulative()
		if c.Received != 2 || c.ExtHighestSeq != 3 {
			t.Errorf("Received=%d ExtHighestSeq=%d, want 2/3 (jumps never accepted)", c.Received, c.ExtHighestSeq)
		}
	})
}

// TestReportFractionLostPerInterval pins the A.3 report math: fraction lost
// covers only the span since the previous Report, in 8-bit fixed point, and
// an interval with net duplication reports 0.
func TestReportFractionLostPerInterval(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 1, 0, 0, 4) // base 2: seqs 2,3,4 received
	r := s.Report()
	if r.FractionLost != 0 || r.CumulativeLost != 0 {
		t.Fatalf("report 1 = %+v, want no loss", r)
	}

	obs(s, 5, 640, at(80))
	obs(s, 8, 1120, at(100)) // 6,7 lost
	r = s.Report()
	if r.FractionLost != 128 { // 2 lost of 4 expected → 2·256/4
		t.Errorf("report 2 FractionLost = %d, want 128", r.FractionLost)
	}
	if r.CumulativeLost != 2 {
		t.Errorf("report 2 CumulativeLost = %d, want 2", r.CumulativeLost)
	}

	// Empty interval: nothing expected, nothing lost.
	r = s.Report()
	if r.FractionLost != 0 || r.CumulativeLost != 2 {
		t.Errorf("report 3 = %+v, want FractionLost 0, CumulativeLost 2", r)
	}

	// Interval that is pure duplication: negative interval loss → 0.
	obs(s, 8, 1120, at(120))
	obs(s, 8, 1120, at(140))
	r = s.Report()
	if r.FractionLost != 0 {
		t.Errorf("report 4 FractionLost = %d, want 0 for negative interval loss", r.FractionLost)
	}
	if r.CumulativeLost != 0 {
		t.Errorf("report 4 CumulativeLost = %d, want 0 (2 lost − 2 duplicates)", r.CumulativeLost)
	}
}

// Test24BitWireClamp pins that the A.3 clamp exists only at report time: the
// snapshot's loss keeps growing past 0x7FFFFF while the wire value pins.
// (Loss is driven up by repeated max-permissible forward jumps, which also
// exercises cycle counting across ~130 wraps.)
func Test24BitWireClamp(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 0, 0, 0, 2) // base 1
	seq := uint16(1)
	for i := 0; i < 2900; i++ {
		seq += 2999 // < MAX_DROPOUT, always believed
		obs(s, seq, uint32(160*(i+2)), at(20*(i+2)))
	}
	c := s.Cumulative()
	if c.CumulativeLost <= 0x7FFFFF {
		t.Fatalf("CumulativeLost = %d, want > 0x7FFFFF unclamped in the snapshot", c.CumulativeLost)
	}
	if r := s.Report(); r.CumulativeLost != 0x7FFFFF {
		t.Errorf("Report().CumulativeLost = %d, want clamped 0x7FFFFF", r.CumulativeLost)
	}
}

// TestClamp24 pins the wire-clamp bounds themselves, including the negative
// side that duplication cannot cheaply reach in an integration test.
func TestClamp24(t *testing.T) {
	tests := []struct {
		in   int64
		want int32
	}{
		{0, 0},
		{-1, -1},
		{0x7FFFFF, 0x7FFFFF},
		{0x800000, 0x7FFFFF},
		{1 << 40, 0x7FFFFF},
		{-0x800000, -0x800000},
		{-0x800001, -0x800000},
		{-(1 << 40), -0x800000},
	}
	for _, tt := range tests {
		if got := clamp24(tt.in); got != tt.want {
			t.Errorf("clamp24(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestMediaGaps pins the media-gap primitive: a gap opens after more than
// 3·ptime of arrival silence (ptime inferred from the stream), carries the
// missing-packet count, and a pure-delay gap reports zero lost. MaxGap
// tracks the longest interarrival gap.
func TestMediaGaps(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 1, 0, 0, 3)       // arrivals 0,20,40 ms; ptime inferred 20 ms
	obs(s, 14, 160*13, at(240)) // 200 ms silence, seqs 4..13 lost
	obs(s, 15, 160*14, at(340)) // 100 ms silence, nothing lost
	obs(s, 16, 160*15, at(360)) // normal spacing
	c := s.Cumulative()
	if len(c.MediaGaps) != 2 {
		t.Fatalf("MediaGaps = %d, want 2", len(c.MediaGaps))
	}
	g := c.MediaGaps[0]
	if !g.Start.Equal(at(40)) || !g.End.Equal(at(240)) {
		t.Errorf("gap 0 = %v..%v, want %v..%v", g.Start, g.End, at(40), at(240))
	}
	if g.PacketsLost != 10 {
		t.Errorf("gap 0 PacketsLost = %d, want 10", g.PacketsLost)
	}
	if c.MediaGaps[1].PacketsLost != 0 {
		t.Errorf("gap 1 PacketsLost = %d, want 0 (pure delay)", c.MediaGaps[1].PacketsLost)
	}
	if c.MaxGap != 200*time.Millisecond {
		t.Errorf("MaxGap = %v, want 200ms", c.MaxGap)
	}
	if c.CumulativeLost != 10 {
		t.Errorf("CumulativeLost = %d, want 10", c.CumulativeLost)
	}
}

// TestIntervalSnapshots pins Interval's delta semantics against Cumulative:
// counts, per-interval fraction lost, interval MaxGap reset, and media-gap
// slicing.
func TestIntervalSnapshots(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 1, 0, 0, 4) // base 2: 2,3,4 counted
	iv := s.Interval()
	if iv.Received != 3 || iv.Expected != 3 || iv.FractionLost != 0 {
		t.Errorf("interval 1: Received=%d Expected=%d Fraction=%v, want 3/3/0",
			iv.Received, iv.Expected, iv.FractionLost)
	}
	if iv.MaxGap != 20*time.Millisecond {
		t.Errorf("interval 1 MaxGap = %v, want 20ms", iv.MaxGap)
	}

	obs(s, 7, 160*6, at(80)) // 5,6 lost, no arrival gap
	obs(s, 8, 160*7, at(100))
	iv = s.Interval()
	if iv.Received != 2 || iv.Expected != 4 {
		t.Errorf("interval 2: Received=%d Expected=%d, want 2/4", iv.Received, iv.Expected)
	}
	if iv.FractionLost != 0.5 {
		t.Errorf("interval 2 FractionLost = %v, want 0.5 (per-interval, not cumulative)", iv.FractionLost)
	}
	if iv.CumulativeLost != 2 {
		t.Errorf("interval 2 CumulativeLost = %d, want 2", iv.CumulativeLost)
	}
	if iv.MaxGap != 20*time.Millisecond {
		t.Errorf("interval 2 MaxGap = %v, want 20ms (reset between intervals)", iv.MaxGap)
	}
	if len(iv.MediaGaps) != 0 {
		t.Errorf("interval 2 MediaGaps = %d, want 0 (loss without arrival silence)", len(iv.MediaGaps))
	}

	// A media gap lands in exactly one interval.
	obs(s, 9, 160*8, at(220)) // 120 ms silence
	iv = s.Interval()
	if len(iv.MediaGaps) != 1 {
		t.Fatalf("interval 3 MediaGaps = %d, want 1", len(iv.MediaGaps))
	}
	if iv = s.Interval(); len(iv.MediaGaps) != 0 {
		t.Errorf("interval 4 MediaGaps = %d, want 0 (already consumed)", len(iv.MediaGaps))
	}

	c := s.Cumulative()
	if c.Received != 6 || c.Expected != 8 || c.CumulativeLost != 2 {
		t.Errorf("cumulative: Received=%d Expected=%d Lost=%d, want 6/8/2", c.Received, c.Expected, c.CumulativeLost)
	}
	if want := 2.0 / 8.0; c.FractionLost != want {
		t.Errorf("cumulative FractionLost = %v, want %v", c.FractionLost, want)
	}
	if len(c.MediaGaps) != 1 || c.MaxGap != 120*time.Millisecond {
		t.Errorf("cumulative MediaGaps=%d MaxGap=%v, want 1/120ms", len(c.MediaGaps), c.MaxGap)
	}
}

// TestJitterAcrossResync pins that the A.8 transit reference is re-anchored
// when the A.1 re-init handshake accepts a sender restart: the restarted
// sender re-randomizes its RTP timestamp base (RFC 3550 §5.1), so a perfectly
// paced zero-jitter stream must stay at zero jitter across the resync instead
// of absorbing an arbitrary-magnitude |D| against the dead base.
func TestJitterAcrossResync(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 1, 0, 0, 10) // perfectly paced: 20 ms / 160 ticks
	if c := s.Cumulative(); c.JitterTicks != 0 {
		t.Fatalf("pre-resync JitterTicks = %d, want 0 for perfect pacing", c.JitterTicks)
	}

	// Sender restart: far-away sequence number AND a new random timestamp
	// base, still perfectly paced on arrival.
	const newTs = 0x9ABCDEF0
	obs(s, 50000, newTs, at(200))     // unconfirmed jump: parked, not counted
	obs(s, 50001, newTs+160, at(220)) // confirms: re-init + resync
	obsRun(s, 50002, newTs+320, 240, 10)

	c := s.Cumulative()
	if c.JitterTicks != 0 {
		t.Errorf("post-resync JitterTicks = %d, want 0 (transit must re-anchor on the new timestamp base)",
			c.JitterTicks)
	}
	if c.JitterMs != 0 {
		t.Errorf("post-resync JitterMs = %v, want 0", c.JitterMs)
	}
}

// TestResyncMediaGapNoLoss pins the documented resync media-gap rule: when
// the arrival silence exceeds 3·ptime AND the stream re-initialized across
// it, the recorded Gap carries PacketsLost = 0 — the numbering is not
// comparable across a restart, so no loss count can be inferred from it.
func TestResyncMediaGapNoLoss(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 1, 0, 0, 3) // ptime inferred 20 ms; last counted arrival at 40 ms
	// >3·ptime of silence, then the restart handshake across the gap.
	obs(s, 40000, 800000, at(200)) // parked (unconfirmed), not counted
	obs(s, 40001, 800160, at(220)) // resync: closes the media gap
	c := s.Cumulative()
	if len(c.MediaGaps) != 1 {
		t.Fatalf("MediaGaps = %d, want 1", len(c.MediaGaps))
	}
	g := c.MediaGaps[0]
	if !g.Start.Equal(at(40)) || !g.End.Equal(at(220)) {
		t.Errorf("gap = %v..%v, want %v..%v", g.Start, g.End, at(40), at(220))
	}
	if g.PacketsLost != 0 {
		t.Errorf("gap PacketsLost = %d, want 0 (re-init across the gap must not fabricate loss)", g.PacketsLost)
	}
}

// TestStragglerMediaGapNoLoss pins the misorder no-cycle fallback against the
// media-gap accounting: a straggler numerically above the max when no wrap
// has happened yet gets a best-effort ext far beyond lastExt, and a media gap
// closed by such a packet must not report ext − lastExt − 1 phantom losses.
func TestStragglerMediaGapNoLoss(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 5, 0, 0, 3) // seqs 5,6,7 — no cycles yet; ptime inferred 20 ms
	// Within MAX_MISORDER behind the max mod 2^16, numerically above it,
	// arriving after >3·ptime of silence.
	obs(s, 65500, 0, at(200))
	c := s.Cumulative()
	if c.Reordered != 1 {
		t.Fatalf("Reordered = %d, want 1 (straggler counted)", c.Reordered)
	}
	if len(c.MediaGaps) != 1 {
		t.Fatalf("MediaGaps = %d, want 1", len(c.MediaGaps))
	}
	if got := c.MediaGaps[0].PacketsLost; got != 0 {
		t.Errorf("gap PacketsLost = %d, want 0 (no-cycle straggler must not fabricate loss)", got)
	}
	if c.CumulativeLost != -1 {
		t.Errorf("CumulativeLost = %d, want -1 (straggler counts as received beyond expected)", c.CumulativeLost)
	}
}

// TestPayloadOctets pins the payload-octet accounting: every counted packet
// contributes (duplicates included), probation drops do not, Cumulative is
// lifetime and Interval is the delta.
func TestPayloadOctets(t *testing.T) {
	s := NewReceiverStats(8000)
	obsRun(s, 1, 0, 0, 3) // first packet dropped by probation: 2 × 160 counted
	if c := s.Cumulative(); c.PayloadOctets != 320 {
		t.Fatalf("cumulative PayloadOctets = %d, want 320", c.PayloadOctets)
	}
	if iv := s.Interval(); iv.PayloadOctets != 320 {
		t.Fatalf("interval 1 PayloadOctets = %d, want 320", iv.PayloadOctets)
	}
	obs(s, 3, 320, at(60)) // duplicate of the max: still counted
	if iv := s.Interval(); iv.PayloadOctets != 160 {
		t.Errorf("interval 2 PayloadOctets = %d, want 160 (delta only)", iv.PayloadOctets)
	}
	if c := s.Cumulative(); c.PayloadOctets != 480 {
		t.Errorf("cumulative PayloadOctets = %d, want 480", c.PayloadOctets)
	}
}

// TestNewReceiverStatsZeroClock pins the constructor's zero-clock panic.
func TestNewReceiverStatsZeroClock(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewReceiverStats(0) did not panic")
		}
	}()
	NewReceiverStats(0)
}
