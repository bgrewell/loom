// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package emul is loom's application-behavior emulation engine: realistic traffic
// *shapes* (VoIP, HTTPS browsing, Prometheus, SSH) compiled to one shared
// behavior-script primitive and run over any datapath. It is the stateful,
// low-rate counterpart to the high-rate pump (DESIGN §10, ADR-0019 review). An
// Emulation compiles to a BehaviorScript; the Runner executes it as a flow.
package emul

import (
	"math"
	"math/rand"

	"github.com/bgrewell/loom/core/units"
)

// Dist is a probability distribution over a non-negative scalar (a byte size or a
// duration in nanoseconds). Sample draws a value with the flow's seeded RNG, so
// emulated shapes are reproducible.
type Dist struct {
	kind distKind
	a, b float64
}

type distKind uint8

const (
	distConstant distKind = iota
	distUniform
	distNormal
	distExponential
)

// Constant always returns v.
func Constant(v float64) Dist { return Dist{kind: distConstant, a: v} }

// Uniform draws uniformly from [lo, hi].
func Uniform(lo, hi float64) Dist {
	if hi < lo {
		lo, hi = hi, lo
	}
	return Dist{kind: distUniform, a: lo, b: hi}
}

// Normal draws from a normal distribution (clamped at 0).
func Normal(mean, stddev float64) Dist { return Dist{kind: distNormal, a: mean, b: stddev} }

// Exponential draws from an exponential distribution with the given mean.
func Exponential(mean float64) Dist { return Dist{kind: distExponential, a: mean} }

// Sample draws a non-negative value from the distribution.
func (d Dist) Sample(r *rand.Rand) float64 {
	var v float64
	switch d.kind {
	case distUniform:
		v = d.a + r.Float64()*(d.b-d.a)
	case distNormal:
		v = d.a + r.NormFloat64()*d.b
	case distExponential:
		v = d.a * r.ExpFloat64()
	default:
		v = d.a
	}
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	return v
}

// SizeDist parses a size param (e.g. "1400", "8KB", "8KB..512KB") into a Dist
// over bytes — a scalar is constant, a "lo..hi" range is uniform.
func SizeDist(s string) (Dist, error) {
	r, err := units.ParseSizeRange(s)
	if err != nil {
		return Dist{}, err
	}
	if r.Hi == r.Lo {
		return Constant(float64(r.Lo)), nil
	}
	return Uniform(float64(r.Lo), float64(r.Hi)), nil
}

// DurationDist parses a duration param (e.g. "20ms", "200ms..2s") into a Dist
// over nanoseconds — scalar is constant, range is uniform.
func DurationDist(s string) (Dist, error) {
	r, err := units.ParseDurationRange(s)
	if err != nil {
		return Dist{}, err
	}
	if r.Hi == r.Lo {
		return Constant(float64(r.Lo)), nil
	}
	return Uniform(float64(r.Lo), float64(r.Hi)), nil
}
