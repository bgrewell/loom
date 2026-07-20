// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package voip is loom's real-media VoIP engine: a bidirectional RTP/RTCP
// session over an injected netpath.Network with live ITU-T G.107 quality
// scoring. One [MediaSession] paces synthetic media out at the codec's ptime
// (core/rtp Packetizer + payload sources), runs RFC 3550 Appendix A receiver
// statistics and a fixed jitter-buffer discard model on the return direction,
// exchanges RTCP SR/RR/SDES/XR/BYE on the RFC 3550 §6.3 randomized interval,
// and assembles a metrics.VoIP snapshot — loss, discard, jitter, RTT, one-way
// delay with provenance, BurstR, and the E-model R/MOS-CQ with its full audit
// breakdown — at every telemetry boundary.
//
// # The SDP seam
//
// [MediaConfig] is deliberately shaped like the outcome of an SDP
// offer/answer: codec row, local/remote RTP addresses, SSRC, direction,
// jitter-buffer depth. The future "sip" app negotiates exactly these values
// over INVITE/SDP and hands the struct to [NewMediaSession] unchanged — the
// media engine never learns SIP. Until then, the symmetric-RTP latch (below)
// stands in for the address exchange.
//
// # Rendezvous and latch
//
// A zero RemoteRTP selects answerer mode: the session binds (inside
// port_min..port_max when driven through the app registry) and latches onto
// the first (source address, SSRC) pair whose packets pass RTP validity
// (parseable version-2 header with the negotiated payload type) and RFC 3550
// A.1 probation (MIN_SEQUENTIAL = 2 in-order packets). Every packet from any
// other source — before or after the latch — is dropped and counted
// ([MediaSession.StrayPackets]); after the latch the same source discipline
// applies to RTCP, and SR/XR state is adopted only from the latched SSRC
// (XR VoIP-metrics blocks additionally have to describe this session's own
// stream). Because a recvonly caller sends no RTP at all, an unlatched
// answerer also adopts the first valid RTCP source as its provisional
// return address — media and reports start flowing toward it, and the RTP
// latch overrides the address if media does arrive. A non-zero RemoteRTP
// selects caller mode: media and RTCP start immediately toward the
// configured address, and if no valid return RTP or RTCP arrives within
// HandshakeTimeout, Run fails with a typed [*HandshakeError] (return RTP
// satisfies the handshake only once its source latches, so a stray packet
// with a common payload type cannot mask an absent peer). RTP and RTCP
// share one socket (RFC 5761 rtcp-mux, classified per packet with
// rtcp.IsRTCP); a zero LocalRTP port binds an ephemeral port, preferring an
// even one per the RTP convention.
//
// # Playout and discard model
//
// The jitter buffer is a fixed playout point, not a queue: the session never
// holds media (there is no decoder), it only decides — per packet — whether a
// real endpoint with a JitterBufferMs-deep buffer would still have played it.
// The first counted packet after the latch anchors the model: its arrival A0
// and RTP timestamp TS0 define the playout deadline of every later packet as
//
//	deadline = A0 + (TS − TS0)/clockRate + JitterBufferMs
//
// i.e. first-arrival-anchored media-clock time plus the buffer depth. A
// packet arriving after its deadline is a DISCARD in the RFC 3611 sense: it
// is counted, fed to the Gilbert estimator as a loss, and included in the
// E-model's Ppl — which is what makes delay spikes hurt MOS instead of being
// invisibly "free". A reordered straggler that arrives behind the extended
// max was already fed to the estimator as a loss when the sequence advanced
// past it; if it also missed its playout deadline it is recorded as a
// discard (its receipt credited the wire-loss count back), so late
// out-of-order arrivals still reach Ppl. The anchor assumes the sender's
// media clock runs at the nominal codec rate from a single timestamp base
// (true of loom's own packetizer); a mid-session sender restart — detected
// by the A.1 loss-baseline reset, wherever the new sequence base lands —
// re-anchors the model instead of fabricating a burst of discards.
//
// # Scoring
//
// Each Metrics call closes one observation interval and scores it: interval
// loss% (RFC 3550 A.3) plus interval discard% form Ppl; the shared Gilbert
// estimator supplies BurstR; Ta is emodel.ComposeTa(OWD, jitter-buffer
// nominal, codec). One-way delay uses the tiered policy of DESIGN's clock
// ladder: SR-NTP arithmetic against an owd.OffsetProvider when one is
// supplied ("timesync"), RTT/2 with an RTT/2 error bar otherwise ("rtt/2"),
// and "none" before any measurement — the method label and error bound
// travel with every value. The same numbers go on the wire in RTCP XR VoIP
// metrics blocks, so the peer's Metrics carry a remote view (RemoteRFactor /
// RemoteMOSCQ) of this end's reception.
//
// Direction is respected: SendOnly sessions still latch and count return
// packets but skip the playout/Gilbert/E-model machinery (scoring a stream
// nobody is meant to send is meaningless); RecvOnly sessions send no media
// but still emit receiver reports and XR so the peer learns the reception
// quality.
//
// # Concurrency and locking
//
// Run starts four goroutines: the TX pacer (an absolute-deadline sleep loop —
// never a time.Ticker, whose slew-corrected ticks are not a pacing clock, and
// never a busy-wait), the RX loop, the RTCP scheduler (rtcp.Interval with
// §6.3.3 reconsideration), and a shutdown watchdog that kicks the blocking
// read via SetReadDeadline on cancellation. All of them terminate
// deterministically on BOTH ctx cancellation and socket close: cancellation
// wakes the pacer and scheduler selects and deadline-kicks the reader, while
// an externally closed socket errors the reader, which cancels the rest; Run
// then sends a best-effort RTCP BYE and closes the socket.
//
// One mutex (MediaSession.mu) guards all measurement and session state:
// receiver stats, the Gilbert estimator, the playout anchor and discard
// counters, latch/candidate state, TX counters, and everything learned from
// inbound RTCP. The RX loop and RTCP builder take it per packet, the pacer
// per send — trivial at media rates (~50 pps) — and Metrics may therefore be
// called concurrently with Run from any goroutine. Byte/packet accounting
// uses core/accounting's lock-free counters outside the mutex, and socket
// I/O happens outside the lock (net.PacketConn is concurrency-safe).
package voip
