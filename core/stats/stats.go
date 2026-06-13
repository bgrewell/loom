// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package stats provides streaming measurement math — running mean/variance,
// jitter, and sequence-based loss/duplicate/reorder detection — as pure values
// with no global state. See docs/blueprints/stats-engine.md and DESIGN.md §7.
package stats

import "math"

// Stream computes count, min, max, mean, variance, std-dev, and coefficient of
// variation incrementally (Welford's algorithm) without retaining samples.
type Stream struct {
	n    uint64
	mean float64
	m2   float64
	min  float64
	max  float64
}

// Add incorporates one observation.
func (s *Stream) Add(x float64) {
	s.n++
	if s.n == 1 {
		s.min, s.max = x, x
	} else {
		if x < s.min {
			s.min = x
		}
		if x > s.max {
			s.max = x
		}
	}
	d := x - s.mean
	s.mean += d / float64(s.n)
	s.m2 += d * (x - s.mean)
}

// Count returns the number of observations.
func (s *Stream) Count() uint64 { return s.n }

// Mean returns the running mean (0 if empty).
func (s *Stream) Mean() float64 { return s.mean }

// Variance returns the sample variance (0 with fewer than two observations).
func (s *Stream) Variance() float64 {
	if s.n < 2 {
		return 0
	}
	return s.m2 / float64(s.n-1)
}

// StdDev returns the sample standard deviation.
func (s *Stream) StdDev() float64 { return math.Sqrt(s.Variance()) }

// CoV returns the coefficient of variation (std-dev / mean), 0 if mean is 0.
func (s *Stream) CoV() float64 {
	if s.mean == 0 {
		return 0
	}
	return s.StdDev() / s.mean
}

// Min returns the smallest observation (0 if empty).
func (s *Stream) Min() float64 { return s.min }

// Max returns the largest observation (0 if empty).
func (s *Stream) Max() float64 { return s.max }

// Jitter tracks the mean absolute difference between consecutive observations.
type Jitter struct {
	prev float64
	has  bool
	sum  float64
	n    uint64
}

// Add incorporates one observation.
func (j *Jitter) Add(x float64) {
	if j.has {
		d := x - j.prev
		if d < 0 {
			d = -d
		}
		j.sum += d
		j.n++
	}
	j.prev = x
	j.has = true
}

// Mean returns the mean absolute consecutive difference (0 if fewer than two).
func (j *Jitter) Mean() float64 {
	if j.n == 0 {
		return 0
	}
	return j.sum / float64(j.n)
}
