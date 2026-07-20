// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package voip

import (
	"strings"

	"github.com/bgrewell/loom/core/rtp"
	"github.com/bgrewell/loom/core/rtp/codec"
)

// sourceFor picks the payload source for a codec: the wire-format-true
// synthetic sources core/rtp provides for G.711 and Opus, and a deterministic
// pseudo-random filler for any other row (right size and cadence on the wire,
// content synthetic — the same rule, minus decodability).
func sourceFor(c codec.Codec, payloadBytes int) rtp.PayloadSource {
	switch strings.ToLower(c.Name) {
	case "pcmu":
		return rtp.NewG711Source("mulaw")
	case "pcma":
		return rtp.NewG711Source("alaw")
	case "opus":
		// NewOpusSource sizes packets as bitrate/400 bytes; invert the codec
		// table's per-ptime byte count back into a bitrate so the two agree.
		return rtp.NewOpusSource(payloadBytes * 400)
	default:
		return prngSource{}
	}
}

// prngSource fills packets with xorshift64 bytes keyed by packet index:
// deterministic (reproducible streams, stable captures) and payload-size
// exact, used for codec rows without a dedicated synthesizer (e.g. g729).
type prngSource struct{}

// Fill implements rtp.PayloadSource.
func (prngSource) Fill(buf []byte, pktIndex uint64) int {
	s := pktIndex*0x9E3779B97F4A7C15 + 0xD1B54A32D192ED03
	for i := range buf {
		s ^= s << 13
		s ^= s >> 7
		s ^= s << 17
		buf[i] = byte(s)
	}
	return len(buf)
}
