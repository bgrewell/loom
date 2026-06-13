// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package stats

// SeqTracker detects loss, duplicates, and reordering over a stream of sequence
// numbers (e.g. from the patterned payloader). Loss is inferred from the highest
// sequence seen versus the count of distinct sequences received.
//
// It retains the set of seen sequences, so memory grows with distinct sequences;
// a windowed variant is a later optimization.
type SeqTracker struct {
	seen       map[uint64]struct{}
	maxSeq     uint64
	hasMax     bool
	total      uint64
	duplicates uint64
	reordered  uint64
}

// NewSeqTracker returns an empty tracker.
func NewSeqTracker() *SeqTracker {
	return &SeqTracker{seen: make(map[uint64]struct{})}
}

// Observe records one received sequence number.
func (t *SeqTracker) Observe(seq uint64) {
	t.total++
	if _, dup := t.seen[seq]; dup {
		t.duplicates++
		return
	}
	t.seen[seq] = struct{}{}
	if t.hasMax && seq < t.maxSeq {
		t.reordered++
	}
	if !t.hasMax || seq > t.maxSeq {
		t.maxSeq = seq
		t.hasMax = true
	}
}

// Total returns the count of observations including duplicates.
func (t *SeqTracker) Total() uint64 { return t.total }

// Received returns the number of distinct sequences received.
func (t *SeqTracker) Received() uint64 { return uint64(len(t.seen)) }

// Duplicates returns the count of repeated sequences.
func (t *SeqTracker) Duplicates() uint64 { return t.duplicates }

// Reordered returns the count of sequences that arrived below the running max.
func (t *SeqTracker) Reordered() uint64 { return t.reordered }

// Expected returns maxSeq+1 — the number of packets that should have arrived if
// sequences start at 0 and are contiguous.
func (t *SeqTracker) Expected() uint64 {
	if !t.hasMax {
		return 0
	}
	return t.maxSeq + 1
}

// Lost returns Expected minus distinct Received (never negative).
func (t *SeqTracker) Lost() uint64 {
	e, r := t.Expected(), t.Received()
	if e > r {
		return e - r
	}
	return 0
}

// LossPercent returns loss as a percentage of Expected.
func (t *SeqTracker) LossPercent() float64 {
	e := t.Expected()
	if e == 0 {
		return 0
	}
	return float64(t.Lost()) / float64(e) * 100
}
