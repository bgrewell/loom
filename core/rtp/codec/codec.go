// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package codec is the audio-codec table for loom's RTP engine: payload types
// and clock rates (RFC 3551 §6 static assignments; RFC 7587 §4.1 for Opus),
// per-ptime payload sizing, codec algorithmic delay, and the ITU-T G.113
// Appendix I equipment-impairment parameters (Ie, Bpl) the G.107 E-model
// consumes. The table is pure data plus sizing functions — no I/O, no clock —
// so codec rows are registry-safe values (ADR-0006 spirit) that core/rtp
// packetizes with and core/quality/emodel scores with.
//
// Spec provenance, per row:
//
//   - pcmu/pcma: RFC 3551 §4.5.14 (PCMU/PCMA, 8000 Hz, one byte per sample),
//     static payload types 0 and 8 (§6). Ie/Bpl from G.113 Appendix I; Bpl is
//     PLC-dependent — 25.1 with packet-loss concealment (the default here,
//     [G711BplPLC]) versus 4.3 without ([G711BplNoPLC]).
//   - g729: RFC 3551 §4.5.6 (10-byte frames per 10 ms), static payload
//     type 18. Ie=11/Bpl=19.0 are the G.729-A + VAD rows of G.113 Appendix I
//     Tables I.1/I.2 (the only G.729 variant with a published Bpl).
//   - opus: RFC 7587. The RTP clock rate is ALWAYS 48000 regardless of the
//     audio bandwidth actually coded (§4.1), so 20 ms is 960 timestamp units.
//     The payload type is dynamic; 111 is the conventional default. Opus has
//     no ITU-assigned Ie/Bpl — the seeded values are PROVISIONAL (see below).
//
// FrameLookahead is the codec's encoder lookahead (algorithmic) delay ONLY:
// G.711 0.25 ms, G.729 5 ms, Opus 6.5 ms. Frame-buffering time is deliberately
// NOT included — a packet's frames accumulate concurrently with the
// packetization wait, so a sample captured at the start of a packet is
// transmittable after ptime + lookahead (the G.114-style budget
// max(frame, ptime) + lookahead, with ptime always ≥ one frame). ComposeTa
// therefore adds FrameLookahead on top of the packetization interval; seeding
// frame+lookahead here would double-count the frame time.
//
// Provisional Opus scoring values: ITU-T has not published Ie/Bpl (G.113) or
// Ie,wb/Bpl,wb (G.113 Amendment for G.107.1) rows for Opus. The seeded values
// (IeWB=5, BplWB=15, mirrored into Ie/Bpl) are engineering placeholders — Opus
// at its default rate is near-transparent for wideband speech and its PLC/FEC
// is robust — NOT standard values. Results scored with them must be treated as
// provisional; calibrated deployments override the row via [Register].
package codec

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// G.711 packet-loss robustness (Bpl) from G.113 Appendix I: the value depends
// on whether the receiver applies packet-loss concealment.
const (
	// G711BplPLC is Bpl for G.711 with packet-loss concealment (the default
	// assumed by the seeded pcmu/pcma rows).
	G711BplPLC = 25.1
	// G711BplNoPLC is Bpl for G.711 without concealment. To score a no-PLC
	// receiver, Register a copy of the row with Bpl set to this value.
	G711BplNoPLC = 4.3
)

// DefaultPtime is the packetization interval assumed when a Codec is
// registered with a zero Ptime (RFC 3551's customary 20 ms default).
const DefaultPtime = 20 * time.Millisecond

// ErrUnknown is returned by ByName for a codec that has not been registered.
var ErrUnknown = errors.New("unknown codec")

// Codec describes one audio codec: its RTP identity (payload type, clock
// rate, channels), packetization sizing, algorithmic delay, and the G.113
// impairment parameters the E-model needs. Rows are plain values — copying is
// cheap and safe (the sizing funcs are stateless).
type Codec struct {
	// Name is the lower-case registry key: "pcmu", "pcma", "g729", "opus".
	Name string
	// PayloadType is the RTP payload type: 0, 8, 18 static (RFC 3551 §6);
	// Opus is dynamic, seeded with the conventional 111.
	PayloadType uint8
	// ClockRate is the RTP timestamp clock in Hz. 8000 for the narrowband
	// rows; for Opus it is ALWAYS 48000 (RFC 7587 §4.1), independent of the
	// coded audio bandwidth.
	ClockRate uint32
	// Channels is the channel count declared for the payload format. Opus is
	// 2 because RFC 7587 §4.1 fixes the declared channel count at 2 (actual
	// encoded channels may be fewer).
	Channels uint8
	// Ptime is the packetization interval; Register defaults it to
	// DefaultPtime (20 ms) when zero.
	Ptime time.Duration
	// PayloadBytes returns the RTP payload size in bytes for one packet at
	// the given ptime (0 for a non-positive or sub-frame ptime).
	PayloadBytes func(ptime time.Duration) int
	// SamplesPerPacket returns the RTP timestamp advance per packet at the
	// given ptime, on this codec's clock: ClockRate·ptime. Opus at 20 ms is
	// 960 on the 48 kHz clock.
	SamplesPerPacket func(ptime time.Duration) uint32
	// FrameLookahead is the codec's encoder lookahead (algorithmic) delay,
	// excluding frame buffering — frames fill during the packetization wait,
	// which emodel.ComposeTa accounts as Ptime (see the package comment).
	FrameLookahead time.Duration
	// Ie and Bpl are the G.113 Appendix I equipment-impairment factor and
	// packet-loss-robustness factor for narrowband (G.107) scoring.
	Ie, Bpl float64
	// Wideband selects G.107.1 scoring, which reads IeWB/BplWB instead of
	// Ie/Bpl.
	Wideband bool
	// IeWB and BplWB are the wideband (G.107.1) equivalents of Ie/Bpl. For
	// Opus they are provisional non-ITU values (see the package comment).
	IeWB, BplWB float64
}

var (
	mu    sync.RWMutex
	table = map[string]Codec{}
)

// Register adds c to the table under strings.ToLower(c.Name), REPLACING any
// existing row — unlike the component registries, overriding is the point:
// calibrated deployments swap in measured Ie/Bpl values (e.g. Opus, or G.711
// without PLC) without forking the table. A zero Ptime is defaulted to
// DefaultPtime. Register panics on an empty Name or nil sizing funcs, which
// are programming errors.
func Register(c Codec) {
	if c.Name == "" {
		panic("codec: Register with empty Name")
	}
	if c.PayloadBytes == nil || c.SamplesPerPacket == nil {
		panic("codec: Register with nil sizing func for " + c.Name)
	}
	if c.Ptime == 0 {
		c.Ptime = DefaultPtime
	}
	mu.Lock()
	defer mu.Unlock()
	table[strings.ToLower(c.Name)] = c
}

// aliases maps common alternate codec spellings onto table names, so every
// path that names a codec — `loom rtp --codec`, a scenario's `codec:` param,
// an embedder's app.Options — accepts the same vocabulary. "g711" is the
// usual way to ask for what RFC 3551 registers as PCMU/PCMA.
var aliases = map[string]string{
	"g711":  "pcmu",
	"g711u": "pcmu",
	"g711a": "pcma",
}

// ByName returns the codec registered under name (case-insensitive; the
// g711/g711u/g711a aliases resolve to pcmu/pcma), or an error wrapping
// ErrUnknown that lists the registered names.
func ByName(name string) (Codec, error) {
	key := strings.ToLower(name)
	if canonical, ok := aliases[key]; ok {
		key = canonical
	}
	mu.RLock()
	defer mu.RUnlock()
	c, ok := table[key]
	if !ok {
		names := make([]string, 0, len(table))
		for n := range table {
			names = append(names, n)
		}
		sort.Strings(names)
		return Codec{}, fmt.Errorf("codec: %w %q (have %v)", ErrUnknown, name, names)
	}
	return c, nil
}

// samplesAt returns clockRate·ptime in timestamp units (0 if ptime <= 0).
func samplesAt(clockRate uint32, ptime time.Duration) uint32 {
	if ptime <= 0 {
		return 0
	}
	return uint32(uint64(clockRate) * uint64(ptime) / uint64(time.Second))
}

// g711Bytes: one byte per 8 kHz sample (RFC 3551 §4.5.14), so bytes track the
// sample count exactly (20 ms → 160).
func g711Bytes(ptime time.Duration) int { return int(samplesAt(8000, ptime)) }

// g729Bytes: 10 bytes per whole 10 ms frame (RFC 3551 §4.5.6); a sub-frame
// remainder does not produce a partial frame (20 ms → 20).
func g729Bytes(ptime time.Duration) int {
	if ptime <= 0 {
		return 0
	}
	return int(ptime/(10*time.Millisecond)) * 10
}

// opusBytes sizes an Opus packet at the nominal 32 kbit/s CBR target this
// table assumes for synthetic media (RFC 7587 imposes no fixed size; rtp's
// NewOpusSource takes the actual bitrate). 20 ms → 80 bytes.
func opusBytes(ptime time.Duration) int {
	if ptime <= 0 {
		return 0
	}
	return int(uint64(32000/8) * uint64(ptime) / uint64(time.Second))
}

func init() {
	Register(Codec{
		Name:             "pcmu",
		PayloadType:      0,
		ClockRate:        8000,
		Channels:         1,
		Ptime:            DefaultPtime,
		PayloadBytes:     g711Bytes,
		SamplesPerPacket: func(p time.Duration) uint32 { return samplesAt(8000, p) },
		FrameLookahead:   250 * time.Microsecond,
		Ie:               0,
		Bpl:              G711BplPLC,
	})
	Register(Codec{
		Name:             "pcma",
		PayloadType:      8,
		ClockRate:        8000,
		Channels:         1,
		Ptime:            DefaultPtime,
		PayloadBytes:     g711Bytes,
		SamplesPerPacket: func(p time.Duration) uint32 { return samplesAt(8000, p) },
		FrameLookahead:   250 * time.Microsecond,
		Ie:               0,
		Bpl:              G711BplPLC,
	})
	Register(Codec{
		Name:             "g729",
		PayloadType:      18,
		ClockRate:        8000,
		Channels:         1,
		Ptime:            DefaultPtime,
		PayloadBytes:     g729Bytes,
		SamplesPerPacket: func(p time.Duration) uint32 { return samplesAt(8000, p) },
		FrameLookahead:   5 * time.Millisecond, // encoder lookahead; 10 ms frames ride inside ptime
		Ie:               11,                   // G.113 App. I Table I.1, G.729-A + VAD
		Bpl:              19.0,                 // G.113 App. I Table I.2, G.729-A + VAD
	})
	Register(Codec{
		Name:             "opus",
		PayloadType:      111, // dynamic; conventional default
		ClockRate:        48000,
		Channels:         2, // declared count fixed at 2 by RFC 7587 §4.1
		Ptime:            DefaultPtime,
		PayloadBytes:     opusBytes,
		SamplesPerPacket: func(p time.Duration) uint32 { return samplesAt(48000, p) },
		FrameLookahead:   6*time.Millisecond + 500*time.Microsecond, // encoder lookahead; 20 ms frames ride inside ptime
		Wideband:         true,
		// PROVISIONAL, non-ITU (no G.113 row exists for Opus); override via
		// Register once calibrated. Mirrored into Ie/Bpl so a narrowband
		// (G.107) scorer fed this row degrades gracefully.
		Ie:    5,
		Bpl:   15,
		IeWB:  5,
		BplWB: 15,
	})
}
