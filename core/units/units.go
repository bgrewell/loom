// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package units parses the scenario value grammar — rates, sizes, durations, and
// "lo..hi" ranges of each (docs/scenario-schema.md). Rates and sizes reuse
// github.com/BGrewell/go-conversions, which distinguishes SI decimal prefixes
// (K/M/G) from IEC binary prefixes (Ki/Mi/Gi); durations and ranges are parsed
// here.
package units

import (
	"errors"
	"fmt"
	"strings"
	"time"

	conv "github.com/BGrewell/go-conversions"
)

// ParseRate parses a bit-rate (e.g. "100Mbps", "1.5G", "1000") to bits/sec.
// SI decimal prefixes (K=1e3, M=1e6, G=1e9, e.g. "100Mbps") are distinct from IEC
// binary prefixes (Ki=2^10, Mi=2^20, Gi=2^30, e.g. "100Mibps"). A value that
// overflows int64 is rejected as out-of-range rather than returned as a poisoned
// (negative) rate.
func ParseRate(s string) (int64, error) {
	v, err := conv.StringBitRateToInt(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid rate %q", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("rate out of range %q", s)
	}
	return v, nil
}

// ParseSize parses a byte size (e.g. "1000", "100K", "1.5MB") to bytes.
// SI decimal prefixes (K=1e3, M=1e6, G=1e9, e.g. "100MB") are distinct from IEC
// binary prefixes (Ki=2^10, Mi=2^20, Gi=2^30, e.g. "100MiB"); a trailing "B" is
// allowed. The underlying parser overflow-guards and returns a non-negative
// int64, which fits in the uint64 result.
func ParseSize(s string) (uint64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, errors.New("empty size")
	}
	v, err := conv.StringByteSizeToInt(trimmed)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return uint64(v), nil
}

// ParseDuration parses a Go duration string (e.g. "100ms", "1m30s").
func ParseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

// RateRange, SizeRange, DurationRange are inclusive [Lo, Hi] bounds. A scalar
// parses to Lo == Hi.
type RateRange struct{ Lo, Hi int64 }

// SizeRange is an inclusive byte range.
type SizeRange struct{ Lo, Hi uint64 }

// DurationRange is an inclusive duration range.
type DurationRange struct{ Lo, Hi time.Duration }

func splitRange(s string) (lo, hi string) {
	if i := strings.Index(s, ".."); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:])
	}
	t := strings.TrimSpace(s)
	return t, t
}

// ParseRateRange parses "lo..hi" or a scalar rate.
func ParseRateRange(s string) (RateRange, error) {
	lo, hi := splitRange(s)
	l, err := ParseRate(lo)
	if err != nil {
		return RateRange{}, err
	}
	h, err := ParseRate(hi)
	if err != nil {
		return RateRange{}, err
	}
	if h < l {
		return RateRange{}, fmt.Errorf("range hi < lo: %q", s)
	}
	return RateRange{l, h}, nil
}

// ParseSizeRange parses "lo..hi" or a scalar size.
func ParseSizeRange(s string) (SizeRange, error) {
	lo, hi := splitRange(s)
	l, err := ParseSize(lo)
	if err != nil {
		return SizeRange{}, err
	}
	h, err := ParseSize(hi)
	if err != nil {
		return SizeRange{}, err
	}
	if h < l {
		return SizeRange{}, fmt.Errorf("range hi < lo: %q", s)
	}
	return SizeRange{l, h}, nil
}

// ParseDurationRange parses "lo..hi" or a scalar duration.
func ParseDurationRange(s string) (DurationRange, error) {
	lo, hi := splitRange(s)
	l, err := ParseDuration(lo)
	if err != nil {
		return DurationRange{}, err
	}
	h, err := ParseDuration(hi)
	if err != nil {
		return DurationRange{}, err
	}
	if h < l {
		return DurationRange{}, fmt.Errorf("range hi < lo: %q", s)
	}
	return DurationRange{l, h}, nil
}
