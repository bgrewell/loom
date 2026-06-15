// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package units

import (
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	cases := map[string]int64{
		"1000":    1000,
		"100K":    100_000,
		"100Mbps": 100_000_000,
		"1.5G":    1_500_000_000,
		"1Gbps":   1_000_000_000,
	}
	for in, want := range cases {
		got, err := ParseRate(in)
		if err != nil || got != want {
			t.Errorf("ParseRate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseRate("bogus"); err == nil {
		t.Error("ParseRate(bogus) should error")
	}
	// A rate that overflows int64 must be rejected, not wrapped negative.
	if got, err := ParseRate("99999999999G"); err == nil {
		t.Errorf("ParseRate(overflow) = %d, want error", got)
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]uint64{
		"1000":   1000,
		"100K":   100_000,       // SI decimal
		"100KB":  100_000,       // SI decimal
		"1.5MB":  1_500_000,     // SI decimal
		"1GB":    1_000_000_000, // SI decimal
		"100KiB": 102_400,       // IEC binary
		"100MiB": 104_857_600,   // IEC binary (100 * 2^20)
		"1GiB":   1 << 30,       // IEC binary
		"512B":   512,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	// The crux of the fix: SI MB and IEC MiB must not be the same size.
	mb, _ := ParseSize("100MB")
	mib, _ := ParseSize("100MiB")
	if mb == mib {
		t.Errorf("100MB (%d) must differ from 100MiB (%d)", mb, mib)
	}
	if _, err := ParseSize(""); err == nil {
		t.Error("ParseSize(empty) should error")
	}
	// Out-of-range / scientific values must error, not silently saturate.
	for _, in := range []string{"1e30", "2e19", "99999999999999999999G"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want error", in, got)
		}
	}
}

func TestParseDuration(t *testing.T) {
	d, err := ParseDuration("1m30s")
	if err != nil || d != 90*time.Second {
		t.Fatalf("ParseDuration = %v, %v", d, err)
	}
}

func TestRanges(t *testing.T) {
	sr, err := ParseSizeRange("100KB..3MB")
	if err != nil || sr.Lo != 100_000 || sr.Hi != 3_000_000 {
		t.Fatalf("size range = %+v, %v", sr, err)
	}
	// scalar → Lo == Hi
	dr, err := ParseDurationRange("50ms")
	if err != nil || dr.Lo != dr.Hi || dr.Lo != 50*time.Millisecond {
		t.Fatalf("duration scalar range = %+v, %v", dr, err)
	}
	rr, err := ParseRateRange("10M..100M")
	if err != nil || rr.Lo != 10_000_000 || rr.Hi != 100_000_000 {
		t.Fatalf("rate range = %+v, %v", rr, err)
	}
	if _, err := ParseSizeRange("3MB..1MB"); err == nil {
		t.Error("hi < lo should error")
	}
}
