// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package rtp implements the RTP wire format and receiver statistics of
// RFC 3550 for loom's real-media traffic engines: header marshal/parse
// (RFC 3550 §5.1), a Packetizer whose SSRC, initial sequence number, and
// initial timestamp come from crypto/rand (§5.1, §8), synthetic G.711 and
// Opus payload sources (RFC 3551 §4.5.14 + ITU-T G.711; RFC 6716 §3.1 +
// RFC 7587), and ReceiverStats implementing RFC 3550 Appendix A exactly:
//
//   - A.1 — 16→32-bit sequence extension with cycle counting, probation of
//     MIN_SEQUENTIAL=2 in-order packets before a source is believed,
//     MAX_DROPOUT=3000 forward jumps and MAX_MISORDER=100 stragglers
//     triggering the two-packet re-init handshake.
//   - A.3 — expected = extended_max − base_seq + 1; lost is SIGNED (it goes
//     negative under duplication) and is clamped to the 24-bit wire range
//     [−0x800000, 0x7FFFFF] only at report time; fraction lost is
//     PER-INTERVAL 8-bit fixed point, zero when the interval loss is
//     negative.
//   - A.8 — interarrival jitter on transit-time differences measured in RTP
//     TIMESTAMP UNITS, filtered with the fixed-point estimator
//     J += |D| − ((J+8)>>4) on 16×-scaled state; packets whose RTP timestamp
//     equals the previous packet's are excluded from the estimator.
//
// # Media clock, never wall clock
//
// The Packetizer advances the RTP timestamp by the codec's samples-per-packet
// on the MEDIA clock — it is never derived from time.Now(). RTP timestamps
// describe the sampling instant of the payload, not the moment the packet
// left the sender; if they were stamped from the wall clock, every scheduler
// wobble on the sender would be indistinguishable from network jitter, and
// the receiver's A.8 estimator would measure the sender's runtime instead of
// the path. With media-clock stamping, sender pacing error shows up where it
// belongs: in the receiver's interarrival measurements against a perfectly
// regular timestamp sequence.
//
// # Wire-format-true, content-synthetic
//
// The payload sources produce packets that are indistinguishable from real
// media on the wire but are generated, not captured. The G.711 source encodes
// band-limited synthetic speech (a few sine partials under a slow syllabic
// envelope) through a correct ITU-T G.711 μ-law/A-law encoder, so Wireshark
// decodes — and plays — the stream. The Opus source emits a valid TOC byte
// (RFC 6716 §3.1; 20 ms SILK-WB configuration) followed by a deterministic
// pseudo-random body sized to the CBR target: dissectors accept it, but it is
// not decodable audio.
//
// # Checklist: naive-implementation mistakes this package avoids
//
// Each of these produces plausible-but-wrong numbers and each is pinned by a
// test in this package:
//
//   - RTP timestamps derived from time.Now() (receiver jitter then measures
//     the sender's scheduler — see above).
//   - Unsigned cumulative loss (duplication legitimately drives lost
//     negative; RFC 3550 A.3 requires signed arithmetic with the 24-bit
//     clamp applied only on the wire).
//   - Fraction lost computed cumulatively instead of per report interval,
//     or not floored at zero when the interval loss is negative.
//   - Jitter computed in wall-clock milliseconds or with a floating-point
//     average instead of the A.8 fixed-point estimator in timestamp units.
//   - No probation: counting a source's first packet before MIN_SEQUENTIAL
//     in-order packets confirm it (a single stray packet then poisons the
//     loss baseline).
//   - No re-init on big jumps (MAX_DROPOUT) or ancient stragglers
//     (MAX_MISORDER), so a sender restart is scored as millions of lost
//     packets.
//   - Feeding equal-timestamp packets to the jitter filter (fragments of one
//     frame share a timestamp; their transit "difference" is transport
//     artifact, not jitter).
//
// See DESIGN.md §7 for the measurement plane this package feeds and
// core/rtp/codec for the codec table it packetizes with.
package rtp
