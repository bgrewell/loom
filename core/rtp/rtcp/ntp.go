// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"math"
	"time"
)

// ntpEpochOffset is the seconds from the NTP epoch (1900-01-01T00:00:00Z) to
// the Unix epoch (RFC 3550 §4).
const ntpEpochOffset = 2208988800

// NTPNow converts t to the 64-bit NTP timestamp format of RFC 3550 §4:
// sec is seconds since the 1900 epoch (Unix time + 2208988800) and frac is
// the fraction of a second in binary fixed point, nanos·2^32/10^9 — computed
// in integer arithmetic, never via floating-point seconds. It is named for
// its usual call site, NTPNow(time.Now()).
//
// The seconds field wraps modulo 2^32 on 2036-02-07T06:28:16Z (the NTP era
// rollover). That is fine for RTCP: LSR/DLSR round-trip arithmetic is
// modular (see RTTFromReport), and SR NTP timestamps are compared only
// across spans far shorter than an era.
func NTPNow(t time.Time) (sec, frac uint32) {
	sec = uint32(t.Unix() + ntpEpochOffset)
	frac = uint32(uint64(t.Nanosecond()) << 32 / 1_000_000_000)
	return sec, frac
}

// CompactNTP folds a 64-bit NTP timestamp to the compact 32-bit form used by
// LSR, LastRR, and the RFC 3611 reference-time fields: the middle 32 bits —
// low 16 of the seconds, high 16 of the fraction — a 16.16 fixed-point value
// in seconds (RFC 3550 §4).
func CompactNTP(sec, frac uint32) uint32 {
	return sec<<16 | frac>>16
}

// DLSRFromDuration converts a delay to the DLSR/DLRR wire unit of 1/65536
// seconds (RFC 3550 §6.4.1), truncating toward zero. Negative delays clamp
// to 0 and delays of 2^16 seconds or more (≈18.2 h, beyond the field's
// range) saturate at the maximum.
func DLSRFromDuration(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	if d >= (1<<16)*time.Second {
		return math.MaxUint32
	}
	return uint32(uint64(d) << 16 / uint64(time.Second))
}

// RTTFromReport computes the round-trip time a report block implies for the
// member whose SR the block echoes, per RFC 3550 §6.4.1 Figure 2:
//
//	RTT = A − LSR − DLSR
//
// where A is arrival (the wall-clock instant this report was received)
// folded to compact NTP form. The subtraction runs ENTIRELY in 16.16 fixed
// point modulo 2^32 — surviving the 18.2-hour compact wrap and the 2036 era
// rollover — and is converted to a time.Duration only at the end.
//
// It returns ok=false with no measurement when LSR is 0 (the reporter has
// not received an SR yet, §6.4.1) and when the modular result is negative
// when read as a signed 16.16 value (clock skew or a corrupt report; an
// honest refusal beats an ≈18-hour "RTT").
func RTTFromReport(arrival time.Time, rb ReportBlock) (time.Duration, bool) {
	if rb.LSR == 0 {
		return 0, false
	}
	a := CompactNTP(NTPNow(arrival))
	rtt := a - rb.LSR - rb.DLSR // 16.16 fixed point, modulo 2^32
	if int32(rtt) < 0 {
		return 0, false
	}
	return time.Duration(uint64(rtt) * uint64(time.Second) >> 16), true
}
