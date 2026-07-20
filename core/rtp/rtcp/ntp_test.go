// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"math"
	"testing"
	"time"
)

// ntpRolloverUnix is the Unix time of the NTP era rollover,
// 2036-02-07T06:28:16Z: 2^32 − 2208988800.
const ntpRolloverUnix = 1<<32 - ntpEpochOffset

// TestNTPNow pins the 1900-epoch second offset and the integer
// nanos·2^32/10^9 fraction, including the 2036 era rollover: the seconds
// field wraps modulo 2^32 there, which is correct and harmless because all
// RTCP timestamp arithmetic (RTTFromReport) is modular.
func TestNTPNow(t *testing.T) {
	tests := []struct {
		name     string
		t        time.Time
		wantSec  uint32
		wantFrac uint32
	}{
		{"NTP epoch", time.Unix(-2208988800, 0), 0, 0},
		{"Unix epoch", time.Unix(0, 0), 2208988800, 0},
		{"quarter second", time.Unix(0, 250_000_000), 2208988800, 0x40000000},
		{"half second", time.Unix(0, 500_000_000), 2208988800, 0x80000000},
		{"three quarters", time.Unix(0, 750_000_000), 2208988800, 0xC0000000},
		{"one nanosecond", time.Unix(0, 1), 2208988800, 4}, // 2^32/10^9 = 4.29…, truncated
		{"last nanosecond", time.Unix(0, 999_999_999), 2208988800, 0xFFFFFFFB},
		{"modern instant", time.Unix(1_600_000_000, 250_000_000), 3808988800, 0x40000000},
		{"second before 2036 rollover", time.Unix(ntpRolloverUnix-1, 0), 0xFFFFFFFF, 0},
		{"2036 era rollover", time.Unix(ntpRolloverUnix, 0), 0, 0},
		{"after 2036 rollover", time.Unix(ntpRolloverUnix+90, 500_000_000), 90, 0x80000000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sec, frac := NTPNow(tt.t.UTC())
			if sec != tt.wantSec || frac != tt.wantFrac {
				t.Fatalf("NTPNow = %#x, %#x; want %#x, %#x", sec, frac, tt.wantSec, tt.wantFrac)
			}
		})
	}
}

// TestNTPRolloverDate pins that ntpRolloverUnix really is
// 2036-02-07T06:28:16Z, documenting the era boundary the wrap tests use.
func TestNTPRolloverDate(t *testing.T) {
	want := time.Date(2036, time.February, 7, 6, 28, 16, 0, time.UTC)
	if got := time.Unix(ntpRolloverUnix, 0).UTC(); !got.Equal(want) {
		t.Fatalf("rollover instant = %v, want %v", got, want)
	}
}

// TestCompactNTP pins the middle-32-bits fold: low 16 of the seconds, high
// 16 of the fraction.
func TestCompactNTP(t *testing.T) {
	tests := []struct {
		sec, frac, want uint32
	}{
		{0x12345678, 0x9ABCDEF0, 0x56789ABC},
		{0, 0, 0},
		{0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFF},
		{1, 0x80000000, 0x00018000}, // 1.5 s in 16.16
	}
	for _, tt := range tests {
		if got := CompactNTP(tt.sec, tt.frac); got != tt.want {
			t.Errorf("CompactNTP(%#x, %#x) = %#x, want %#x", tt.sec, tt.frac, got, tt.want)
		}
	}
}

// TestDLSRFromDuration pins the 1/65536-second wire unit with truncation,
// the zero clamp, and saturation past the field's 18.2-hour range.
func TestDLSRFromDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want uint32
	}{
		{"zero", 0, 0},
		{"negative", -time.Second, 0},
		{"one tick exactly needs 15259ns", 15259 * time.Nanosecond, 1},
		{"just under one tick", 15258 * time.Nanosecond, 0},
		{"100ms", 100 * time.Millisecond, 6553}, // 0.1·65536 = 6553.6, truncated
		{"one second", time.Second, 65536},
		{"one hour", time.Hour, 3600 * 65536},
		{"at the field limit", (1 << 16) * time.Second, math.MaxUint32},
		{"past the field limit", 24 * time.Hour, math.MaxUint32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DLSRFromDuration(tt.d); got != tt.want {
				t.Fatalf("DLSRFromDuration(%v) = %d, want %d", tt.d, got, tt.want)
			}
		})
	}
}

// TestRTTFromReport pins the §6.4.1 A − LSR − DLSR computation on
// hand-computed 16.16 vectors: exact fixed-point results, the compact-wrap
// case, and the refusal cases (LSR == 0, negative result).
func TestRTTFromReport(t *testing.T) {
	// arrival: NTP sec 3808988800 = 0xE3088E80, frac 0x40000000
	// → A = 0x8E80_4000.
	arrival := time.Unix(1_600_000_000, 250_000_000)
	// wrapArrival: NTP sec 0xE3090000, frac 0x10000000 → A = 0x0000_1000
	// (the compact form wrapped its 16-bit seconds field).
	wrapArrival := time.Unix(1_600_029_056, 62_500_000)

	tests := []struct {
		name    string
		arrival time.Time
		rb      ReportBlock
		want    time.Duration
		wantOK  bool
	}{
		{
			// A − LSR = 0x14000 (1.25 s), DLSR = 0x8000 (0.5 s) → 0.75 s.
			name:    "golden 750ms",
			arrival: arrival,
			rb:      ReportBlock{LSR: 0x8E7F0000, DLSR: 0x8000},
			want:    750 * time.Millisecond,
			wantOK:  true,
		},
		{
			name:    "zero DLSR",
			arrival: arrival,
			rb:      ReportBlock{LSR: 0x8E7F0000},
			want:    1250 * time.Millisecond,
			wantOK:  true,
		},
		{
			// One 16.16 tick: 1/65536 s = 15258.789… ns, truncated.
			name:    "single tick resolution",
			arrival: arrival,
			rb:      ReportBlock{LSR: 0x8E803FFF},
			want:    15258 * time.Nanosecond,
			wantOK:  true,
		},
		{
			name:    "exactly zero RTT",
			arrival: arrival,
			rb:      ReportBlock{LSR: 0x8E804000},
			want:    0,
			wantOK:  true,
		},
		{
			// A = 0x1000 with LSR from before the compact wrap: modular
			// arithmetic must yield 0x1000 + 0x8000 − 0x800 = 0x8800
			// = 0.53125 s, not an ≈18-hour garbage value.
			name:    "compact 18.2h wrap between LSR and arrival",
			arrival: wrapArrival,
			rb:      ReportBlock{LSR: 0xFFFF8000, DLSR: 0x800},
			want:    531_250_000 * time.Nanosecond,
			wantOK:  true,
		},
		{
			name:    "LSR zero means no SR yet",
			arrival: arrival,
			rb:      ReportBlock{LSR: 0, DLSR: 0x8000},
			want:    0,
			wantOK:  false,
		},
		{
			// DLSR exceeds A − LSR: negative 16.16 result (skew or corrupt
			// report) is refused, not reported as ≈18 hours.
			name:    "negative result refused",
			arrival: arrival,
			rb:      ReportBlock{LSR: 0x8E804000, DLSR: 0x10000},
			want:    0,
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := RTTFromReport(tt.arrival, tt.rb)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("RTTFromReport = %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestRTTRoundTrip simulates the full SR → RR exchange, including one across
// the 2036 era rollover: the sender stamps an SR, the receiver echoes its
// compact form plus a DLSR, and the recovered RTT must match the true
// network round trip within the 16.16 quantization of LSR, DLSR, and A
// (three truncations of ≤15.26 µs each).
func TestRTTRoundTrip(t *testing.T) {
	const tick = 15259 * time.Nanosecond // one 1/65536 s tick, rounded up
	tests := []struct {
		name    string
		srSent  time.Time
		rtt     time.Duration
		holdOff time.Duration // receiver delay before reporting (DLSR)
	}{
		{"LAN scale", time.Unix(1_600_000_000, 123_456_789), 350 * time.Microsecond, 40 * time.Millisecond},
		{"WAN scale", time.Unix(1_700_000_000, 987_654_321), 187 * time.Millisecond, 5 * time.Second},
		{"across the 2036 rollover", time.Unix(ntpRolloverUnix, 0).Add(-100 * time.Millisecond), 250 * time.Millisecond, 100 * time.Millisecond},
		{"across a compact 18.2h wrap", time.Unix(1_600_029_056, 0).Add(-time.Minute), 42 * time.Millisecond, 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lsr := CompactNTP(NTPNow(tt.srSent))
			dlsr := DLSRFromDuration(tt.holdOff)
			arrival := tt.srSent.Add(tt.rtt + tt.holdOff)
			got, ok := RTTFromReport(arrival, ReportBlock{LSR: lsr, DLSR: dlsr})
			if !ok {
				t.Fatal("RTTFromReport not ok")
			}
			if diff := (got - tt.rtt).Abs(); diff > 3*tick {
				t.Fatalf("RTT = %v, want %v ± %v (diff %v)", got, tt.rtt, 3*tick, diff)
			}
		})
	}
}
