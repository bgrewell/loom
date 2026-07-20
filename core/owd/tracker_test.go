// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package owd

import (
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/timesync"
)

// exchange simulates one four-timestamp exchange started at local time t1
// against a remote clock whose offset from the local clock at local instant x
// is offsetAt(x). fwd/rev are the one-way wire delays, proc the remote
// processing time. It returns the resulting Sample and the local receive time
// t4 to feed as `at`.
func exchange(t1 time.Time, fwd, proc, rev time.Duration, offsetAt func(time.Time) time.Duration) (timesync.Sample, time.Time) {
	t1n := t1.UnixNano()
	t2n := t1n + int64(fwd) + int64(offsetAt(t1.Add(fwd)))
	t3n := t1n + int64(fwd+proc) + int64(offsetAt(t1.Add(fwd+proc)))
	t4n := t1n + int64(fwd+proc+rev)
	return timesync.NewSample(t1n, t2n, t3n, t4n), time.Unix(0, t4n)
}

func constant(d time.Duration) func(time.Time) time.Duration {
	return func(time.Time) time.Duration { return d }
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func TestTrackerNotReadyBeforeFirstWindow(t *testing.T) {
	tr := NewTracker(10*time.Second, 4)
	t0 := time.Unix(1700000000, 0)
	if _, _, ok := tr.Offset(); ok {
		t.Fatal("ok=true on an empty tracker")
	}
	s := timesync.Sample{Offset: 2 * time.Millisecond, Delay: 6 * time.Millisecond}
	for _, dt := range []time.Duration{0, 3 * time.Second, 6 * time.Second, 9 * time.Second} {
		tr.Feed(s, t0.Add(dt))
		if _, _, ok := tr.Offset(); ok {
			t.Fatalf("ok=true at %v into the first window", dt)
		}
	}
	tr.Feed(s, t0.Add(10*time.Second))
	if _, _, ok := tr.Offset(); !ok {
		t.Fatal("ok=false after the first window completed")
	}
}

func TestTrackerConstantOffset(t *testing.T) {
	const trueOffset = 2 * time.Millisecond
	tr := NewTracker(5*time.Second, 4)
	t0 := time.Unix(1700000000, 0)
	for i := 0; i <= 26; i++ {
		s, at := exchange(t0.Add(time.Duration(i)*time.Second),
			3*time.Millisecond, time.Millisecond, 3*time.Millisecond,
			constant(trueOffset))
		tr.Feed(s, at)
	}
	off, errB, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false after five windows")
	}
	if err := absDuration(off - trueOffset); err > time.Microsecond {
		t.Errorf("offset = %v, want %v (err %v)", off, trueOffset, err)
	}
	if err := absDuration(off - trueOffset); err > errB {
		t.Errorf("recovered error %v exceeds reported bound %v", err, errB)
	}
	// Symmetric 3ms+3ms path with no jitter: bound is delay/2 = 3ms.
	if want := 3 * time.Millisecond; absDuration(errB-want) > time.Microsecond {
		t.Errorf("errBound = %v, want %v", errB, want)
	}
}

func TestTrackerMinDelayFilter(t *testing.T) {
	// Within one window, only the minimum-delay sample must survive: the two
	// high-delay samples carry badly skewed offsets.
	const trueOffset = 10 * time.Millisecond
	tr := NewTracker(10*time.Second, 4)
	t0 := time.Unix(1700000000, 0)
	tr.Feed(timesync.Sample{Offset: trueOffset + 5*time.Millisecond, Delay: 20 * time.Millisecond}, t0.Add(1*time.Second))
	tr.Feed(timesync.Sample{Offset: trueOffset, Delay: 2 * time.Millisecond}, t0.Add(2*time.Second))
	tr.Feed(timesync.Sample{Offset: trueOffset - 7*time.Millisecond, Delay: 30 * time.Millisecond}, t0.Add(3*time.Second))
	// Complete the window.
	tr.Feed(timesync.Sample{Offset: trueOffset, Delay: 2 * time.Millisecond}, t0.Add(11*time.Second))

	off, errB, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false after window completed")
	}
	if off != trueOffset {
		t.Errorf("offset = %v, want %v (min-delay sample not selected)", off, trueOffset)
	}
	// Single fitted point: zero residual, bound = 2ms/2.
	if want := time.Millisecond; errB != want {
		t.Errorf("errBound = %v, want %v", errB, want)
	}
}

func TestTrackerSingleWindow(t *testing.T) {
	tr := NewTracker(time.Second, 8)
	t0 := time.Unix(1700000000, 0)
	tr.Feed(timesync.Sample{Offset: -4 * time.Millisecond, Delay: 6 * time.Millisecond}, t0)
	tr.Feed(timesync.Sample{Offset: 0, Delay: 40 * time.Millisecond}, t0.Add(time.Second)) // completes window, starts next
	off, errB, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false with one completed window")
	}
	if off != -4*time.Millisecond {
		t.Errorf("offset = %v, want -4ms", off)
	}
	if want := 3 * time.Millisecond; errB != want {
		t.Errorf("errBound = %v, want %v", errB, want)
	}
}

func TestTrackerDrift(t *testing.T) {
	// Remote clock drifts +50ppm relative to local: offset(x) = base + 50µs/s.
	const (
		base  = 10 * time.Millisecond
		drift = 50e-6
	)
	t0 := time.Unix(1700000000, 0)
	offsetAt := func(x time.Time) time.Duration {
		return base + time.Duration(drift*float64(x.Sub(t0)))
	}
	tr := NewTracker(10*time.Second, 6)

	feedUntil := func(from, until time.Duration) time.Time {
		var lastAt time.Time
		for dt := from; dt <= until; dt += 2 * time.Second {
			s, at := exchange(t0.Add(dt), 4*time.Millisecond, 500*time.Microsecond, 4*time.Millisecond, offsetAt)
			tr.Feed(s, at)
			lastAt = at
		}
		return lastAt
	}

	lastAt1 := feedUntil(0, 60*time.Second)
	off1, errB1, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false after 60s of feeds")
	}
	want1 := offsetAt(lastAt1)
	if err := absDuration(off1 - want1); err > 5*time.Microsecond {
		t.Errorf("offset at 60s = %v, want %v (err %v)", off1, want1, err)
	}
	if err := absDuration(off1 - want1); err > errB1 {
		t.Errorf("recovered error %v exceeds reported bound %v", err, errB1)
	}

	lastAt2 := feedUntil(62*time.Second, 120*time.Second)
	off2, errB2, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false after 120s of feeds")
	}
	want2 := offsetAt(lastAt2)
	if err := absDuration(off2 - want2); err > 5*time.Microsecond {
		t.Errorf("offset at 120s = %v, want %v (err %v)", off2, want2, err)
	}
	if err := absDuration(off2 - want2); err > errB2 {
		t.Errorf("recovered error %v exceeds reported bound %v", err, errB2)
	}

	// The tracked offset must have advanced by drift × elapsed local time.
	wantAdvance := time.Duration(drift * float64(lastAt2.Sub(lastAt1)))
	if gotAdvance := off2 - off1; absDuration(gotAdvance-wantAdvance) > 10*time.Microsecond {
		t.Errorf("offset advanced %v over %v, want %v (50ppm not tracked)",
			gotAdvance, lastAt2.Sub(lastAt1), wantAdvance)
	}
}

func TestTrackerErrBoundHonest(t *testing.T) {
	// Asymmetric jitter (and asymmetric base delay) bias the per-sample
	// offsets; the recovered error must stay within the reported bound in
	// every trial.
	rng := rand.New(rand.NewSource(42))
	cases := []struct {
		name               string
		baseFwd, baseRev   time.Duration
		jitterF, jitterR   time.Duration
		maxAcceptableBound time.Duration
	}{
		{"asymmetric jitter", 4 * time.Millisecond, 4 * time.Millisecond, 3 * time.Millisecond, 300 * time.Microsecond, 10 * time.Millisecond},
		{"asymmetric base", 6 * time.Millisecond, 2 * time.Millisecond, time.Millisecond, time.Millisecond, 10 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for trial := 0; trial < 150; trial++ {
				trueOffset := time.Duration(rng.Int63n(int64(100*time.Millisecond))) - 50*time.Millisecond
				tr := NewTracker(5*time.Second, 6)
				t0 := time.Unix(1700000000, 0)
				for i := 0; i <= 40; i++ {
					fwd := tc.baseFwd + time.Duration(rng.Int63n(int64(tc.jitterF)))
					rev := tc.baseRev + time.Duration(rng.Int63n(int64(tc.jitterR)))
					s, at := exchange(t0.Add(time.Duration(i)*time.Second),
						fwd, 500*time.Microsecond, rev, constant(trueOffset))
					tr.Feed(s, at)
				}
				off, errB, ok := tr.Offset()
				if !ok {
					t.Fatalf("trial %d: ok=false", trial)
				}
				if err := absDuration(off - trueOffset); err > errB {
					t.Fatalf("trial %d: recovered error %v exceeds reported bound %v (offset %v, true %v)",
						trial, err, errB, off, trueOffset)
				}
				if errB > tc.maxAcceptableBound {
					t.Fatalf("trial %d: errBound %v implausibly large", trial, errB)
				}
			}
		})
	}
}

func TestTrackerWindowEviction(t *testing.T) {
	// n=2: after an offset step, two completed windows of new data must fully
	// evict the old level from the fit.
	oldOffset := 10 * time.Millisecond
	newOffset := 4 * time.Millisecond
	tr := NewTracker(time.Second, 2)
	t0 := time.Unix(1700000000, 0)
	for k := 0; k <= 7; k++ {
		off := oldOffset
		if k >= 5 {
			off = newOffset
		}
		tr.Feed(timesync.Sample{Offset: off, Delay: 2 * time.Millisecond}, t0.Add(time.Duration(k)*time.Second))
	}
	off, errB, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false after eight windows")
	}
	if err := absDuration(off - newOffset); err > time.Microsecond {
		t.Errorf("offset = %v, want %v — old windows not evicted", off, newOffset)
	}
	if want := time.Millisecond; absDuration(errB-want) > time.Microsecond {
		t.Errorf("errBound = %v, want %v", errB, want)
	}
}

func TestTrackerEmptyWindowGap(t *testing.T) {
	// A feed gap spanning several empty windows must not block completion or
	// corrupt the grid.
	tr := NewTracker(time.Second, 4)
	t0 := time.Unix(1700000000, 0)
	tr.Feed(timesync.Sample{Offset: time.Millisecond, Delay: 2 * time.Millisecond}, t0)
	tr.Feed(timesync.Sample{Offset: time.Millisecond, Delay: 2 * time.Millisecond}, t0.Add(10*time.Second))
	off, _, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false after a gap completed the first window")
	}
	if off != time.Millisecond {
		t.Errorf("offset = %v, want 1ms", off)
	}
	// The sample after the gap belongs to the current window; completing that
	// window must retire it too.
	tr.Feed(timesync.Sample{Offset: 3 * time.Millisecond, Delay: 2 * time.Millisecond}, t0.Add(11*time.Second))
	if _, _, ok := tr.Offset(); !ok {
		t.Fatal("ok=false after second window completed")
	}
}

func TestTrackerRejectsNegativeDelay(t *testing.T) {
	tr := NewTracker(time.Second, 4)
	t0 := time.Unix(1700000000, 0)
	bogus := timesync.Sample{Offset: 99 * time.Millisecond, Delay: -time.Millisecond}
	for i := 0; i < 5; i++ {
		tr.Feed(bogus, t0.Add(time.Duration(i)*time.Second))
	}
	if _, _, ok := tr.Offset(); ok {
		t.Fatal("tracker became ready on negative-delay samples alone")
	}
	// A negative-delay sample must also never win the min-delay filter.
	tr.Feed(timesync.Sample{Offset: 2 * time.Millisecond, Delay: 4 * time.Millisecond}, t0.Add(5*time.Second))
	tr.Feed(bogus, t0.Add(5500*time.Millisecond))
	tr.Feed(timesync.Sample{Offset: 2 * time.Millisecond, Delay: 4 * time.Millisecond}, t0.Add(7*time.Second))
	off, _, ok := tr.Offset()
	if !ok {
		t.Fatal("ok=false after a valid window completed")
	}
	if off != 2*time.Millisecond {
		t.Errorf("offset = %v, want 2ms (negative-delay sample leaked into the filter)", off)
	}
}

func TestTrackerConcurrent(t *testing.T) {
	tr := NewTracker(100*time.Millisecond, 8)
	t0 := time.Unix(1700000000, 0)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				at := t0.Add(time.Duration(g*500+i) * 10 * time.Millisecond)
				tr.Feed(timesync.Sample{Offset: time.Millisecond, Delay: 5 * time.Millisecond}, at)
				tr.Offset()
			}
		}(g)
	}
	wg.Wait()
	if off, _, ok := tr.Offset(); !ok || absDuration(off-time.Millisecond) > time.Microsecond {
		t.Errorf("after concurrent feeds: offset=%v ok=%v, want 1ms true", off, ok)
	}
}
