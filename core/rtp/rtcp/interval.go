// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtcp

import (
	"math"
	"math/rand"
	"time"
)

// DefaultTmin is RFC 3550 §6.2's RECOMMENDED minimum RTCP interval of 5
// seconds (halved for a member's first interval, §6.3.1).
const DefaultTmin = 5 * time.Second

// RFC 3550 §6.2/A.7 bandwidth constants.
const (
	// rtcpBWFraction is the RTCP share of the session bandwidth (5%).
	rtcpBWFraction = 0.05
	// senderBWFraction and receiverBWFraction split the RTCP share 25/75
	// between senders and receivers while senders are ≤ 25% of the members.
	senderBWFraction   = 0.25
	receiverBWFraction = 0.75
	// defaultAvgSize is the avg_rtcp_size assumed when none is supplied:
	// a small SR+SDES compound plus UDP/IPv4 overhead, the "probable size
	// of the first RTCP packet" seed of §6.3.2.
	defaultAvgSize = 128.0
)

// compensation is A.7's COMPENSATION constant, e − 3/2 ≈ 1.21828: timer
// reconsideration converges to an interval 1.21828 times smaller than the
// target, so the drawn interval is divided by it.
var compensation = math.E - 1.5

// Interval computes the RTCP transmission interval of RFC 3550 §6.3.1
// exactly as coded in Appendix A.7. It is pure data (ADR-0006 spirit): the
// caller maintains the session view (member/sender counts, whether we have
// sent, the running average compound size) and draws each timer from Next.
//
// Next supplies ONLY the randomized draw; the caller owns the §6.3.3 timer
// reconsideration check (and §6.3.4 reverse reconsideration when members
// leave): at timer expiry tc, recompute tn = tp + Next(rng) with the CURRENT
// membership view and transmit only if tn <= tc, otherwise re-arm the timer
// for tn without sending. The COMPENSATION divisor inside Next pre-shrinks
// the draw exactly because reconsideration lengthens realized intervals —
// scheduling Next's value directly without the expiry-time recheck biases
// the mean interval ~18% below the bandwidth target and forfeits A.7's
// damping when membership grows suddenly. (For a fixed two-member session
// the recheck draws the same distribution and only costs the ~18% bias.)
type Interval struct {
	// SessionBW is the session bandwidth in BITS per second (the same
	// figure the media budget uses, e.g. 87.2 kbit/s for one G.711 stream
	// with IP/UDP/RTP overhead). RTCP gets a 5% share. Zero or negative
	// means "unknown" and pins the deterministic interval at Tmin.
	SessionBW float64
	// Members is the current estimate of session members including
	// ourselves (clamped to ≥ 1); Senders is how many of them sent RTP in
	// the last two report intervals.
	Members, Senders int
	// WeSent selects the sender share of the RTCP bandwidth when senders
	// are ≤ 25% of the membership.
	WeSent bool
	// Initial halves the minimum for a member's first interval (§6.3.1),
	// trading a little burst risk for faster first reports.
	Initial bool
	// Tmin is the minimum deterministic interval; zero or negative selects
	// DefaultTmin (5 s).
	Tmin time.Duration
	// AvgSize is the running average compound RTCP packet size in OCTETS,
	// including lower-layer (UDP/IP) overhead, maintained per §6.3.3
	// (avg = 1/16·size + 15/16·avg). Zero or negative selects a
	// 128-octet estimate.
	AvgSize float64
}

// Td returns the deterministic calculated interval of §6.3.1/A.7 before
// randomization: avg_rtcp_size·n over this member's share of the 5% RTCP
// bandwidth, floored at Tmin (halved when Initial). Exposed so callers (and
// tests) can reason about the target mean, which is Td/1.21828.
func (iv *Interval) Td() time.Duration {
	tmin := iv.Tmin
	if tmin <= 0 {
		tmin = DefaultTmin
	}
	if iv.Initial {
		tmin /= 2
	}
	members := iv.Members
	if members < 1 {
		members = 1
	}
	senders := iv.Senders
	if senders < 0 {
		senders = 0
	}
	// rtcp_bw in octets per second: 5% of the session bandwidth.
	rtcpBW := rtcpBWFraction * iv.SessionBW / 8
	n := float64(members)
	if float64(senders) <= senderBWFraction*float64(members) {
		if iv.WeSent {
			rtcpBW *= senderBWFraction
			n = float64(senders)
		} else {
			rtcpBW *= receiverBWFraction
			n = float64(members - senders)
		}
	}
	avg := iv.AvgSize
	if avg <= 0 {
		avg = defaultAvgSize
	}
	td := tmin
	if rtcpBW > 0 {
		if t := time.Duration(avg * n / rtcpBW * float64(time.Second)); t > td {
			td = t
		}
	}
	return td
}

// Next draws the next transmission interval: Td randomized uniformly over
// [0.5·Td, 1.5·Td) and divided by the COMPENSATION constant 1.21828, so the
// mean interval under timer reconsideration hits the bandwidth target and
// independent members never synchronize (the A.7 defense against report
// storms). The caller must apply the §6.3.3 reconsideration check at timer
// expiry (see the type comment) — the 1.21828 divisor assumes it. rng must
// be non-nil; sessions seed their own generator so fleet members draw
// independent sequences.
func (iv *Interval) Next(rng *rand.Rand) time.Duration {
	td := iv.Td().Seconds()
	return time.Duration(td * (rng.Float64() + 0.5) / compensation * float64(time.Second))
}
