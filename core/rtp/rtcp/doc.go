// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package rtcp implements the RTCP wire format and timing discipline for
// loom's real-media engines: sender and receiver reports, source description,
// and goodbye packets (RFC 3550 §6.4.1, §6.4.2, §6.5, §6.6), the extended
// report blocks loom uses for non-sender RTT and on-wire VoIP quality
// (RFC 3611 §4.4 Receiver Reference Time, §4.5 DLRR, §4.7 VoIP Metrics),
// compound-packet construction and parsing (RFC 3550 §6.1), RTP/RTCP
// demultiplexing on a shared port (RFC 5761 §4), the NTP timestamp
// discipline behind LSR/DLSR round-trip measurement (RFC 3550 §4, §6.4.1),
// and the randomized transmission-interval algorithm of RFC 3550
// §6.3/Appendix A.7.
//
// # NTP discipline
//
// RTCP timestamps use the 64-bit NTP format: seconds since the 1900 epoch
// (Unix time + 2208988800) in the top 32 bits and binary fractions of a
// second (nanoseconds·2^32/10^9) in the bottom 32. [NTPNow] performs exactly
// that conversion. The compact 32-bit form used by LSR and by RFC 3611
// reference-time fields is the MIDDLE 32 bits — low 16 of the seconds, high
// 16 of the fraction — a 16.16 fixed-point value with 1/65536-second
// resolution that wraps every 2^16 seconds (≈18.2 hours). DLSR is expressed
// in the same 1/65536-second units.
//
// Round-trip time at the report's recipient is RFC 3550 §6.4.1 Figure 2:
// A − LSR − DLSR, where A is the arrival time in compact form. The
// subtraction is carried out ENTIRELY in 16.16 fixed point modulo 2^32 —
// which is what makes it immune to both the 18.2-hour compact wrap and the
// 2036 NTP era rollover — and converted to a duration only at the very end
// ([RTTFromReport]). An LSR of zero means "no SR received yet" (§6.4.1) and
// yields no measurement, not an RTT of A.
//
// # Transmission interval
//
// [Interval] implements the calculation of RFC 3550 §6.3.1 as coded in
// Appendix A.7: the deterministic interval Td is avg_rtcp_size·n divided by
// the 5% RTCP share of the session bandwidth, with 25% of that share
// dedicated to senders and 75% to receivers while senders are ≤ 25% of the
// membership, floored at Tmin (5 s, halved for the first interval). The
// emitted interval is uniformly randomized over [0.5·Td, 1.5·Td) and divided
// by e−3/2 ≈ 1.21828 to compensate for the fact that timer reconsideration
// converges to a value 1.21828 times smaller. The randomization is what
// prevents RTCP synchronization storms when thousands of sessions start
// together (fleet mode). The timer-reconsideration algorithm itself
// (§6.3.3/§6.3.6) lives with the CALLER, which owns the timers and the
// membership table: recompute the draw at expiry with current counts and
// send only if it has passed (see the [Interval] type comment) — Next alone
// is not a complete scheduler.
//
// # Checklist: naive-implementation mistakes this package avoids
//
// Each of these produces plausible-but-wrong numbers and each is pinned by a
// test in this package:
//
//   - NTP fractions computed via floating-point seconds instead of
//     nanos·2^32/10^9 (drifts up to hundreds of nanoseconds per stamp).
//   - RTT terms converted to durations BEFORE the A − LSR − DLSR
//     subtraction (each conversion rounds; worse, the modular wrap
//     arithmetic that survives the 18.2-hour and 2036 rollovers is lost).
//   - LSR == 0 fed to the RTT formula (scores "no SR seen yet" as an RTT
//     equal to the receiver's absolute clock).
//   - A fixed RTCP timer with no randomization or 1.21828 compensation
//     (synchronized report storms at scale, and a mean interval that
//     misses the bandwidth target).
//   - Compound packets without a leading SR/RR or without an SDES CNAME —
//     both violate RFC 3550 §6.1 and break receivers that key on them;
//     [MarshalCompound] refuses to build them.
//   - Cumulative loss carried unsigned into the 24-bit report field
//     (duplication legitimately makes it negative; the wire clamp is
//     [−0x800000, 0x7FFFFF]).
//
// The XR VoIP Metrics block (RFC 3611 §4.7) carries loss/discard rates and
// burst/gap densities in 1/256 fixed point, durations and delays in
// milliseconds, R factors and MOS×10 with 127 meaning "unavailable" — the
// fields core/quality/gilbert and core/quality/emodel compute. See DESIGN.md
// §7 for the measurement plane, core/rtp for the receiver statistics that
// feed report blocks, and core/rtp/codec for the codec table.
package rtcp
