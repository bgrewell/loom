// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package emodel scores conversational voice quality with the ITU-T E-model:
// narrowband per ITU-T G.107 (06/2015) and wideband per ITU-T G.107.1
// (06/2019). It implements the full default-parameter formulas — the basic
// signal-to-noise ratio Ro from the noise summation (G.107 §7.2, eqs 7-2..7-7),
// the simultaneous impairment Is = Iolr + Ist + Iq (§7.3, eqs 7-8..7-17), the
// delay impairments Id = Idte + Idle + Idd (§7.4, eqs 7-18..7-28) and the
// burst-adjusted effective equipment impairment Ie,eff (§7.5, eq. 7-29) — with
// every parameter the caller does not supply pinned at its G.107 Table 3
// default (SLR 8, RLR 2, STMR 15, LSTR 18, Ds 3, TELR 65, WEPL 110, Nc −70,
// Nfor −64 narrowband / −96 wideband, Ps = Pr = 35, qdu 1, sT 1, mT 100 ms).
// No component is a curve fit or a hardcoded constant: with zero impairment
// the formulas themselves reproduce G.107's documented default rating
// R = 93.2 (Ro ≈ 94.77, Is ≈ 1.41, Id ≈ 0.15), and golden tests pin exactly
// that, alongside the Annex B / Table B.1 R→MOS reference values.
//
// Input semantics (these are the two places naive E-model wiring goes wrong):
//
//   - Ta is the ABSOLUTE mouth-to-ear delay, not the raw network one-way
//     delay: network OWD + jitter-buffer nominal depth + packetization
//     interval + codec encoder lookahead. [ComposeTa] performs exactly that
//     composition (frame buffering overlaps the packetization wait, so it is
//     covered by the ptime term, never added again); feeding raw OWD
//     understates Idd and therefore overstates MOS.
//   - Ppl is the loss percentage (0..100) INCLUDING jitter-buffer discards
//     (RFC 3611 discard semantics): a packet that arrives too late to play
//     out is as lost as one that never arrived. Excluding discards is what
//     makes delay spikes invisibly "free" — they must hurt MOS through Ppl.
//   - BurstR is the burst ratio from core/quality/gilbert (1 = random loss),
//     clamped to ≥ 1 here. Per G.107 eq. 7-29, burstier loss at the same Ppl
//     yields a HIGHER Ie,eff and therefore a lower R. G.107 Table 3 Note 6:
//     predictions for BurstR > 2 are only valid for Ppl < 2%.
//
// Delay mapping: the echo-path delays are derived from Ta assuming a
// symmetric path with echo coupling at the far end — T = Ta (mean one-way
// echo-path delay) and Tr = 2·Ta (round-trip delay in the 4-wire loop). Ta,
// T and Tr are never conflated: Idd is driven by Ta, Idte by T and Idle by
// Tr. Idd is zero for Ta ≤ mT = 100 ms and follows eq. 7-27 with the default
// delay-sensitivity class (sT = 1) beyond it — no approximation of the knee.
//
// Wideband (G.107.1): selected by Config.Wideband or a codec row whose
// Wideband flag is set. The rating scale extends to Ro,WB = 129 (G.107.1
// eq. 7-2), Is,WB = 0 (7-3), the talker echo uses the wideband TERV with the
// K correction and the coefficient 3 in Re,WB (7-5..7-10), Idd carries the
// wideband 1.29 factor (7-13), and Ie,eff,WB reads the codec's IeWB/BplWB.
// The R→MOS conversion is G.107.1 Annex A — the polynomial applied to
// Rx = R/1.29 (eq. A-1) — NOT the narrowband Annex B mapping of R itself.
// Two documented interpretations on the wideband path: (a) G.107.1 leaves
// No,WB undefined ("for further study"); Idte,WB needs it via Roe, so it is
// computed with the G.107 §7.2 noise summation using G.107.1 Table 1
// defaults (Nfor = −96 instead of −64), giving No,WB ≈ −68.81. (b) G.107.1
// eq. 7-15 is G.107 eq. 7-29 with BurstR = 1; the same burst-ratio
// generalization is applied on both paths so bursty loss is never scored as
// random merely because the codec is wideband.
//
// Validated ranges (G.107 Table 3): Ta 0..500 ms, Ppl 0..20%, BurstR 1..8.
// Score accepts Ta beyond 500 ms and Ppl up to 100% — degraded paths produce
// such values and refusing to score them would hide the impairment — but
// results outside the table ranges are extrapolations of the same formulas.
//
// Everything here is pure computation — no I/O, no clock, no state — so all
// functions are safe for concurrent use.
package emodel

import (
	"fmt"
	"math"
	"time"

	"github.com/bgrewell/loom/core/rtp/codec"
)

// Method values reported in Result.Method: which recommendation scored the
// call.
const (
	// MethodG107 is narrowband scoring per ITU-T G.107 (06/2015).
	MethodG107 = "g107"
	// MethodG1071 is wideband scoring per ITU-T G.107.1 (06/2019).
	MethodG1071 = "g107.1"
)

// Config selects the codec and scoring variant. The zero value of Wideband
// and A with a registered codec row is the common case.
type Config struct {
	// Codec supplies the equipment impairment parameters: Ie/Bpl for
	// narrowband scoring, IeWB/BplWB for wideband, and the delays ComposeTa
	// folds into Ta.
	Codec codec.Codec
	// Wideband forces G.107.1 scoring (R scale 0..129, wideband Idd
	// coefficient and the Annex A R→MOS map). A codec row with its own
	// Wideband flag set always scores wideband regardless of this field.
	Wideband bool
	// A is the advantage factor (G.107 §7.6): a planning allowance for
	// access advantage (e.g., mobility) that offsets impairment. Default 0 —
	// leave it there for measurement, where hiding impairment is the failure
	// mode. Score rejects values outside G.107 Table 3's 0..20.
	A float64
}

// Input is one measurement interval's worth of impairment inputs.
type Input struct {
	// Ta is the absolute mouth-to-ear delay: network OWD + jitter-buffer
	// nominal + packetization interval + codec encoder lookahead. Use
	// ComposeTa; feeding raw network OWD understates the delay impairment.
	Ta time.Duration
	// Ppl is the packet-loss percentage (0..100) INCLUDING jitter-buffer
	// discards (RFC 3611 discard semantics): network loss% + discard%.
	Ppl float64
	// BurstR is the burst ratio (gilbert.Metrics.BurstR); values below 1
	// (including 0 for "unknown") are clamped to 1 (random loss).
	BurstR float64
}

// Components is the audited breakdown of one Score call, exposing every term
// of R = Ro − Is − Id − IeEff + A so a rating can be attributed to noise,
// simultaneous impairment, echo, delay or loss rather than trusted blindly.
// The JSON tags carry the breakdown through the core/metrics results plane in
// loom's snake_case serialization style.
type Components struct {
	// Ro is the basic signal-to-noise ratio (G.107 §7.2); 129 for wideband
	// (G.107.1 eq. 7-2).
	Ro float64 `json:"ro"`
	// Is is the simultaneous impairment Iolr + Ist + Iq (G.107 §7.3); 0 for
	// wideband (G.107.1 eq. 7-3).
	Is float64 `json:"is"`
	// Idte is the talker-echo impairment (G.107 eq. 7-19 / G.107.1 eq. 7-5)
	// at T = Ta; 0 for T < 1 ms narrowband (echo is sidetone, G.107 §7.4).
	Idte float64 `json:"idte"`
	// Idle is the listener-echo impairment (G.107 eq. 7-25) at Tr = 2·Ta.
	Idle float64 `json:"idle"`
	// Idd is the pure-delay impairment (G.107 eq. 7-27, sT = 1, mT = 100 ms;
	// ×1.29 for wideband per G.107.1 eq. 7-13): 0 for Ta ≤ 100 ms.
	Idd float64 `json:"idd"`
	// Id is Idte + Idle + Idd (G.107 eq. 7-18).
	Id float64 `json:"id"`
	// Ie is the codec's equipment impairment factor at zero loss (IeWB when
	// scoring wideband).
	Ie float64 `json:"ie"`
	// IeEff is the effective equipment impairment after loss and burstiness
	// (G.107 eq. 7-29): Ie + (95 − Ie)·Ppl/(Ppl/BurstR + Bpl).
	IeEff float64 `json:"ie_eff"`
	// A is the advantage factor applied (Config.A).
	A float64 `json:"a"`
	// R is the transmission rating factor: Ro − Is − Id − IeEff + A.
	R float64 `json:"r"`
}

// Result is a scored interval.
type Result struct {
	// R is the transmission rating factor: 0..~100 narrowband (93.2 at
	// defaults), 0..129 wideband. It may go below 0 under extreme
	// impairment; the MOS maps clamp, R itself is not clamped.
	R float64
	// MOSCQ is the estimated conversational-quality mean opinion score
	// (MOS-CQE, 1..4.5): G.107 Annex B for narrowband, G.107.1 Annex A for
	// wideband.
	MOSCQ float64
	// Method is MethodG107 or MethodG1071.
	Method string
	// C is the per-term audit breakdown.
	C Components
}

// Score rates one interval. It returns an error — never a plausible-looking
// number — when the inputs are outside the model's domain: Ppl not in
// [0, 100], negative Ta, A outside G.107 Table 3's [0, 20], or loss reported
// for a codec with no packet-loss robustness factor.
func Score(cfg Config, in Input) (Result, error) {
	if math.IsNaN(cfg.A) || cfg.A < 0 || cfg.A > 20 {
		return Result{}, fmt.Errorf("emodel: advantage factor %v outside G.107 Table 3 range [0, 20]", cfg.A)
	}
	if math.IsNaN(in.Ppl) || in.Ppl < 0 || in.Ppl > 100 {
		return Result{}, fmt.Errorf("emodel: Ppl %v%% outside [0, 100]", in.Ppl)
	}
	if in.Ta < 0 {
		return Result{}, fmt.Errorf("emodel: negative Ta %v", in.Ta)
	}

	wideband := cfg.Wideband || cfg.Codec.Wideband
	ie, bpl := cfg.Codec.Ie, cfg.Codec.Bpl
	if wideband {
		ie, bpl = cfg.Codec.IeWB, cfg.Codec.BplWB
	}
	if in.Ppl > 0 && !(bpl > 0) {
		return Result{}, fmt.Errorf("emodel: codec %q has no packet-loss robustness factor for %s scoring of Ppl %v%%",
			cfg.Codec.Name, method(wideband), in.Ppl)
	}

	// Delay mapping (symmetric path, far-end echo coupling): T = Ta,
	// Tr = 2·Ta, all in ms as the recommendations expect.
	ta := float64(in.Ta) / float64(time.Millisecond)
	t, tr := ta, 2*ta

	var c Components
	if wideband {
		c = Components{
			Ro:   roWB,      // G.107.1 eq. 7-2
			Is:   0,         // G.107.1 eq. 7-3
			Idte: idteWB(t), // G.107.1 eqs 7-5..7-10
			Idle: idle(roWB, tr),
			Idd:  iddWBFactor * idd(ta), // G.107.1 eq. 7-13
		}
	} else {
		no := noise(defNforNB)
		ro := basicSNR(no)
		c = Components{
			Ro:   ro,
			Is:   iolr(no) + ist(t) + iq(ro),
			Idte: idte(no, t),
			Idle: idle(ro, tr),
			Idd:  idd(ta),
		}
	}
	c.Id = c.Idte + c.Idle + c.Idd
	c.Ie = ie
	c.IeEff = IeEff(ie, bpl, in.Ppl, in.BurstR)
	c.A = cfg.A
	c.R = c.Ro - c.Is - c.Id - c.IeEff + c.A

	mos := MOSFromR(c.R)
	if wideband {
		mos = MOSFromRWB(c.R)
	}
	return Result{R: c.R, MOSCQ: mos, Method: method(wideband), C: c}, nil
}

// method names the scoring recommendation.
func method(wideband bool) string {
	if wideband {
		return MethodG1071
	}
	return MethodG107
}

// ComposeTa builds the E-model's absolute delay Ta from its parts:
//
//	Ta = networkOWD + jbNominal + c.FrameLookahead + c.Ptime
//
// networkOWD is the measured network one-way delay and jbNominal the
// jitter-buffer nominal (playout) depth. The sender-side encode budget is
// Ptime + FrameLookahead: a sample captured at the start of a packet waits
// one packetization interval for the packet to fill, and the codec's frames
// are encoded as they complete DURING that wait (frame buffering and
// packetization overlap — the G.114-style budget max(frame, ptime) +
// lookahead with ptime ≥ frame), so only the encoder's algorithmic lookahead
// (codec.Codec.FrameLookahead, which excludes frame time) is added on top of
// Ptime. Adding a frame+lookahead figure here would double-count the frame
// time. A zero Ptime falls back to codec.DefaultPtime, matching
// codec.Register's defaulting.
func ComposeTa(networkOWD, jbNominal time.Duration, c codec.Codec) time.Duration {
	ptime := c.Ptime
	if ptime <= 0 {
		ptime = codec.DefaultPtime
	}
	return networkOWD + jbNominal + c.FrameLookahead + ptime
}

// IeEff is the effective equipment impairment factor of G.107 eq. 7-29:
//
//	Ie,eff = Ie + (95 − Ie) · Ppl / (Ppl/BurstR + Bpl)
//
// with ppl in PERCENT (0..100, clamped) and burstR clamped to ≥ 1. At
// ppl = 0 it is exactly ie; it approaches 95 as loss grows. Burstier loss
// (burstR > 1) shrinks the denominator and therefore RAISES Ie,eff at the
// same ppl. bpl must be positive for a meaningful result (Score enforces
// that; G.113 rows always carry one). G.107.1 eq. 7-15 is this formula with
// BurstR = 1; the burst generalization is applied to wideband parameters
// unchanged (see the package comment).
func IeEff(ie, bpl, ppl, burstR float64) float64 {
	if math.IsNaN(burstR) || burstR < 1 {
		burstR = 1
	}
	if ppl > 100 {
		ppl = 100
	}
	if !(ppl > 0) { // includes NaN and negatives
		return ie
	}
	return ie + (95-ie)*ppl/(ppl/burstR+bpl)
}

// MOSFromR converts a narrowband rating factor to conversational MOS-CQE per
// G.107 Annex B (eq. B-4):
//
//	R ≤ 0:         1
//	0 < R < 100:   1 + 0.035·R + R·(R − 60)·(100 − R)·7·10⁻⁶
//	R ≥ 100:       4.5
//
// The polynomial itself hits both endpoints (MOS(0) = 1, MOS(100) = 4.5), so
// the mapping is continuous. NaN propagates. For wideband ratings use
// MOSFromRWB — applying this map to a 0..129-scale R misreads the scale.
func MOSFromR(r float64) float64 {
	switch {
	case math.IsNaN(r):
		return math.NaN()
	case r <= 0:
		return 1
	case r >= 100:
		return 4.5
	}
	return 1 + 0.035*r + r*(r-60)*(100-r)*7e-6
}

// MOSFromRWB converts a wideband rating factor (0..129 scale) to MOS-CQE per
// G.107.1 Annex A: Rx = R/1.29 (eq. A-1), then the eq. A-2 polynomial with
// the same clamps in Rx. This is deliberately NOT MOSFromR applied to R —
// the 1.29 rescale is what maps R = 129 to MOS 4.5.
func MOSFromRWB(r float64) float64 {
	return MOSFromR(r / 1.29)
}
