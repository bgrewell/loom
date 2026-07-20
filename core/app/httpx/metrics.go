// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/bgrewell/loom/core/metrics"
)

// sample is one completed request's timing record. Connect and TLSHandshake
// are zero for requests served on reused keep-alive connections (no dial
// happened); TTFB and Object always cover request start to first body byte
// and to last body byte respectively. For Err samples TTFB/Object hold the
// failure duration, not a latency — the recorder excludes them from the
// timing percentiles.
type sample struct {
	Connect      time.Duration
	TLSHandshake time.Duration
	TTFB         time.Duration
	Object       time.Duration
	Bytes        uint64
	Proto        string // "HTTP/1.1", "HTTP/2.0"
	Err          bool   // transport failure or non-2xx status
}

// recorder aggregates request samples into metrics.HTTP snapshots with the
// same window discipline as core/app/voip: Metrics closes one observation
// interval (percentiles/means/goodput over the requests completed since the
// previous Metrics call), CumulativeMetrics covers the whole run and closes
// nothing. Samples are appended for the run's lifetime — requests are bounded
// by the flow duration, so memory is bounded by the run like voip's MediaGaps.
type recorder struct {
	mu sync.Mutex

	start time.Time // run start (cumulative goodput denominator)
	stop  time.Time // run end; zero while running

	samples []sample
	errs    uint64
	bytes   uint64

	// Window marks: index/counts at the previous Metrics call.
	winIdx   int
	winErrs  uint64
	winBytes uint64
	winStart time.Time
}

// newRecorder returns a recorder whose window clock starts now, so a Metrics
// call before Run still has a sane (if uninteresting) denominator.
func newRecorder() *recorder {
	now := time.Now()
	return &recorder{start: now, winStart: now}
}

// runStarted stamps the run's start, resetting the goodput baselines.
func (r *recorder) runStarted() {
	r.mu.Lock()
	now := time.Now()
	r.start, r.winStart, r.stop = now, now, time.Time{}
	r.mu.Unlock()
}

// runStopped freezes the cumulative goodput denominator.
func (r *recorder) runStopped() {
	r.mu.Lock()
	r.stop = time.Now()
	r.mu.Unlock()
}

// observe appends one completed request.
func (r *recorder) observe(s sample) {
	r.mu.Lock()
	r.samples = append(r.samples, s)
	if s.Err {
		r.errs++
	}
	r.bytes += s.Bytes
	r.mu.Unlock()
}

// snapshot builds a metrics.HTTP over samples[from:] with the given byte/error
// deltas and elapsed wall time. Callers hold mu.
func (r *recorder) snapshotLocked(from int, errs, bytes uint64, elapsed time.Duration) metrics.HTTP {
	win := r.samples[from:]
	h := metrics.HTTP{
		Requests: uint64(len(win)),
		Errors:   errs,
	}
	var ttfb, object []float64
	var connSum, tlsSum float64
	var connN, tlsN int
	for _, s := range win {
		// Failed requests carry no first-byte or transfer time — their TTFB is
		// the failure duration (anything from a ~50µs ECONNREFUSED to a 15s
		// handshake timeout), which is not a latency of the path under test.
		// They stay out of the timing pools; Requests/Errors carry the failure
		// signal. Connect/TLS times are kept when present — those handshakes
		// really completed even if the request then failed.
		if !s.Err {
			ttfb = append(ttfb, float64(s.TTFB)/float64(time.Millisecond))
			object = append(object, float64(s.Object)/float64(time.Millisecond))
		}
		if s.Connect > 0 {
			connSum += float64(s.Connect) / float64(time.Millisecond)
			connN++
		}
		if s.TLSHandshake > 0 {
			tlsSum += float64(s.TLSHandshake) / float64(time.Millisecond)
			tlsN++
		}
	}
	if connN > 0 {
		h.ConnectMs = connSum / float64(connN)
	}
	if tlsN > 0 {
		h.TLSHandshakeMs = tlsSum / float64(tlsN)
	}
	h.TTFBMsP50 = percentile(ttfb, 0.50)
	h.TTFBMsP95 = percentile(ttfb, 0.95)
	h.TTFBMsP99 = percentile(ttfb, 0.99)
	h.ObjectMsP50 = percentile(object, 0.50)
	h.ObjectMsP95 = percentile(object, 0.95)
	h.ObjectMsP99 = percentile(object, 0.99)
	if elapsed > 0 {
		h.GoodputMbps = float64(bytes) * 8 / elapsed.Seconds() / 1e6
	}
	return h
}

// Metrics closes one observation interval and returns its metrics.HTTP.
func (r *recorder) Metrics() metrics.HTTP {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if !r.stop.IsZero() && r.stop.After(r.winStart) {
		now = r.stop // run ended inside this window: don't dilute goodput
	}
	h := r.snapshotLocked(r.winIdx, r.errs-r.winErrs, r.bytes-r.winBytes, now.Sub(r.winStart))
	r.winIdx, r.winErrs, r.winBytes, r.winStart = len(r.samples), r.errs, r.bytes, now
	return h
}

// Cumulative returns the whole-run metrics.HTTP without closing an interval.
func (r *recorder) Cumulative() metrics.HTTP {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.stop
	if end.IsZero() {
		end = time.Now()
	}
	return r.snapshotLocked(0, r.errs, r.bytes, end.Sub(r.start))
}

// percentile returns the nearest-rank percentile (q in (0,1]) of v in place-
// independent fashion (v is copied), 0 for an empty slice.
func percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	idx := int(math.Ceil(float64(len(s))*q)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}
