// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package owd

import (
	"sync"
	"time"

	"github.com/bgrewell/loom/core/timesync"
)

// Default Tracker parameters, used when NewTracker is given non-positive
// values. A 30s window at the standard ~10s exchange cadence holds a few
// samples to filter, and 8 windows give the drift fit four minutes of history.
const (
	DefaultWindow  = 30 * time.Second
	DefaultWindows = 8
)

// minimum is one window's minimum-delay sample: when it was observed, the
// offset it measured, and its round-trip delay.
type minimum struct {
	at     time.Time
	offset time.Duration
	delay  time.Duration
}

// Tracker turns repeated four-timestamp exchanges (timesync.Sample) into a
// filtered clock-offset estimate with drift. Per window of the configured
// duration it keeps only the minimum-delay sample (NTP clock-filter style,
// RFC 5905 §10); over the minima of the last N completed windows it fits a
// linear offset(t) model by least squares, so a steadily drifting remote
// clock is tracked rather than smeared.
//
// Feed it from whatever loop runs the exchanges, over a SYMMETRIC path (the
// management network, never through a tunnel under test — path asymmetry
// biases the offset by half the asymmetry and is invisible to this filter).
//
// Offset evaluates the drift model at the most recently fed sample's time;
// feed exchanges regularly, as the bound does not cover drift accumulated
// after the last exchange. Samples with a negative round-trip delay are
// discarded as invalid. A Tracker is safe for concurrent use.
type Tracker struct {
	mu     sync.Mutex
	window time.Duration
	n      int

	winStart time.Time // start of the current (incomplete) window
	winHas   bool      // current window has at least one sample
	winMin   minimum   // minimum-delay sample of the current window

	minima []minimum // completed-window minima, oldest first, at most n
	lastAt time.Time // time of the most recently fed sample
}

// NewTracker returns a Tracker that keeps the minimum-delay sample per
// `window` of feed time and fits drift over the minima of the last n
// completed windows. Non-positive arguments fall back to [DefaultWindow] and
// [DefaultWindows].
func NewTracker(window time.Duration, n int) *Tracker {
	if window <= 0 {
		window = DefaultWindow
	}
	if n <= 0 {
		n = DefaultWindows
	}
	return &Tracker{window: window, n: n}
}

// Feed records one time-sync exchange observed at local time `at` (the local
// receive time t4 of the exchange). The first feed anchors the window grid;
// a feed at or past the current window's end completes that window, retiring
// its minimum-delay sample into the fit set. Samples with negative round-trip
// delay are discarded; samples timestamped before the current window join it
// rather than rewinding the grid.
func (t *Tracker) Feed(s timesync.Sample, at time.Time) {
	if s.Delay < 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.winStart.IsZero() && !t.winHas && len(t.minima) == 0 {
		t.winStart = at
	}
	// Complete the current window (and skip any empty ones) if `at` has
	// moved past its end.
	if elapsed := at.Sub(t.winStart); elapsed >= t.window {
		if t.winHas {
			t.retire(t.winMin)
			t.winHas = false
		}
		t.winStart = t.winStart.Add(elapsed / t.window * t.window)
	}
	if !t.winHas || s.Delay < t.winMin.delay {
		t.winMin = minimum{at: at, offset: s.Offset, delay: s.Delay}
		t.winHas = true
	}
	if at.After(t.lastAt) {
		t.lastAt = at
	}
}

// retire appends a completed window's minimum, evicting the oldest beyond n.
// Callers must hold t.mu.
func (t *Tracker) retire(m minimum) {
	if len(t.minima) == t.n {
		copy(t.minima, t.minima[1:])
		t.minima[len(t.minima)-1] = m
		return
	}
	t.minima = append(t.minima, m)
}

// Offset returns the estimated remote-minus-local clock offset at the time of
// the most recently fed sample, with a half-width error bound, satisfying
// [OffsetProvider]. The value is the least-squares offset-drift line over the
// retained window minima evaluated at that time; the bound is the fit's
// largest absolute residual plus half the smallest round-trip delay among the
// minima. ok is false until the first window has completed.
func (t *Tracker) Offset() (offset, errBound time.Duration, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.minima) == 0 {
		return 0, 0, false
	}

	// Least squares over (seconds since first minimum, offset in ns).
	tref := t.minima[0].at
	xbar, ybar := 0.0, 0.0
	for _, m := range t.minima {
		xbar += m.at.Sub(tref).Seconds()
		ybar += float64(m.offset)
	}
	fn := float64(len(t.minima))
	xbar /= fn
	ybar /= fn

	sxx, sxy := 0.0, 0.0
	for _, m := range t.minima {
		dx := m.at.Sub(tref).Seconds() - xbar
		sxx += dx * dx
		sxy += dx * float64(m.offset)
	}
	slope := 0.0 // ns per second of local time
	if sxx > 0 {
		slope = sxy / sxx
	}

	resid := 0.0
	minDelay := t.minima[0].delay
	for _, m := range t.minima {
		r := float64(m.offset) - (ybar + slope*(m.at.Sub(tref).Seconds()-xbar))
		if r < 0 {
			r = -r
		}
		if r > resid {
			resid = r
		}
		if m.delay < minDelay {
			minDelay = m.delay
		}
	}

	value := ybar + slope*(t.lastAt.Sub(tref).Seconds()-xbar)
	return time.Duration(value), time.Duration(resid) + minDelay/2, true
}
