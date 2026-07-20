// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package voip

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/bgrewell/loom/core/rtp/codec"
)

// Defaults applied by NewMediaSession when the corresponding MediaConfig
// field is zero.
const (
	// DefaultJitterBufferMs is the fixed playout-buffer depth assumed when
	// JitterBufferMs is 0: 40 ms, two 20 ms packets of headroom.
	DefaultJitterBufferMs = 40
	// DefaultHandshakeTimeout bounds how long a caller waits for the first
	// return RTP or RTCP packet before Run fails with a *HandshakeError.
	DefaultHandshakeTimeout = 5 * time.Second
)

// Direction is the media direction of a session, named from this end's
// perspective like the SDP attributes it mirrors (sendrecv/sendonly/recvonly).
type Direction int

const (
	// SendRecv sends and receives media (the default; a normal call leg).
	SendRecv Direction = iota
	// SendOnly sends media and skips receive-side quality scoring (return
	// packets still drive the latch and are still counted).
	SendOnly
	// RecvOnly sends no media but still receives, scores, and emits RTCP
	// receiver reports and XR.
	RecvOnly
)

// String returns the SDP-style attribute name.
func (d Direction) String() string {
	switch d {
	case SendRecv:
		return "sendrecv"
	case SendOnly:
		return "sendonly"
	case RecvOnly:
		return "recvonly"
	default:
		return fmt.Sprintf("direction(%d)", int(d))
	}
}

// valid reports whether d is one of the three defined directions.
func (d Direction) valid() bool { return d >= SendRecv && d <= RecvOnly }

// MediaConfig describes one media session. It is EXACTLY the parameter set an
// SDP offer/answer produces — the SIP seam: the future "sip" app negotiates
// these values on the wire and hands the struct to NewMediaSession unchanged,
// replacing the symmetric-RTP latch with explicit addresses while leaving the
// media engine untouched.
type MediaConfig struct {
	// Codec is the negotiated codec row (codec.ByName). A row whose sizing
	// funcs are nil but whose Name is set is resolved through codec.ByName as
	// a convenience.
	Codec codec.Codec
	// LocalRTP is the local RTP bind address. A zero (or unspecified) address
	// binds all interfaces; a zero port binds an ephemeral port, preferring
	// an even one per the RTP convention (RFC 3550 §11). RTCP shares the same
	// socket — rtcp-mux (RFC 5761) is on by default; a separate RTCP port is
	// a future SDP-negotiated option.
	LocalRTP netip.AddrPort
	// RemoteRTP is the peer's RTP address. A zero RemoteRTP (port 0) selects
	// answerer mode: the session latches onto the first source that passes
	// RTP validity and A.1 probation (see the package comment). Non-zero
	// selects caller mode: media starts immediately toward this address.
	RemoteRTP netip.AddrPort
	// SSRC is the local synchronization source identifier; 0 draws one from
	// crypto/rand (RFC 3550 §8).
	SSRC uint32
	// Direction selects which way media flows (SendRecv default).
	Direction Direction
	// JitterBufferMs is the fixed playout-point depth in milliseconds
	// (default 40): a packet arriving later than first-arrival-anchored
	// media-clock time plus this depth counts as a discard feeding Ppl. See
	// the package comment for the exact model. Negative is invalid.
	JitterBufferMs int
	// HandshakeTimeout bounds the caller's wait for first return RTP/RTCP
	// (default 5 s); on expiry Run returns a *HandshakeError. Ignored in
	// answerer mode, where the flow's own duration bounds the session.
	HandshakeTimeout time.Duration
}

// HandshakeError is returned by MediaSession.Run when a caller receives no
// valid return RTP or RTCP within the handshake timeout — the peer is absent,
// filtered, or misaddressed. Match it with errors.As.
type HandshakeError struct {
	// Timeout is the deadline that expired (MediaConfig.HandshakeTimeout).
	Timeout time.Duration
}

// Error implements error.
func (e *HandshakeError) Error() string {
	return fmt.Sprintf("voip: no return RTP or RTCP within %v handshake timeout", e.Timeout)
}
