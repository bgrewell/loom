// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emodel

import "math"

// ITU-T G.107 (06/2015) Table 3 default parameter values. Everything the
// Config/Input surface does not expose is pinned here; the equations below
// consume these — none of Ro, Is or Id is a stored constant.
const (
	defSLR  = 8.0   // send loudness rating, dB
	defRLR  = 2.0   // receive loudness rating, dB
	defSTMR = 15.0  // sidetone masking rating, dB
	defLSTR = 18.0  // listener sidetone rating, dB (STMR + Dr)
	defDs   = 3.0   // D-value of telephone, send side
	defTELR = 65.0  // talker echo loudness rating, dB
	defWEPL = 110.0 // weighted echo path loss, dB
	defNc   = -70.0 // circuit noise referred to the 0 dBr point, dBm0p
	defPs   = 35.0  // room noise at the send side, dB(A)
	defPr   = 35.0  // room noise at the receive side, dB(A)
	defQdu  = 1.0   // quantization distortion units

	// Noise floor at the receive side, dBmp: G.107 Table 3 vs G.107.1
	// Table 1 differ here (−64 vs −96), which is why noise() takes it as a
	// parameter.
	defNforNB = -64.0
	defNforWB = -96.0

	// Delay-sensitivity class "Default" (G.107 Table 1): minimum perceivable
	// delay mT in ms and sensitivity sT; eq. 7-27's exponent is 6·sT.
	defMT = 100.0
	defST = 1.0
)

// noise is the total noise power No (G.107 eqs 7-3..7-7, dBm0p): the power
// sum of circuit noise Nc, the room-noise equivalents Nos and Nor, and the
// receive-side noise floor Nfo = Nfor + RLR.
func noise(nfor float64) float64 {
	olr := defSLR + defRLR
	// Equivalent circuit noise from send-side room noise Ps (eq. 7-4).
	nos := defPs - defSLR - defDs - 100 + 0.004*sq(defPs-olr-defDs-14)
	// Effective room noise from the listener sidetone path (eq. 7-6) and the
	// receive-side equivalent Nor (eq. 7-5).
	pre := defPr + 10*math.Log10(1+math.Pow(10, (10-defLSTR)/10))
	nor := defRLR - 121 + pre + 0.008*sq(pre-35)
	nfo := nfor + defRLR // eq. 7-7
	return 10 * math.Log10(math.Pow(10, defNc/10)+
		math.Pow(10, nos/10)+math.Pow(10, nor/10)+math.Pow(10, nfo/10)) // eq. 7-3
}

// basicSNR is Ro = 15 − 1.5·(SLR + No) (G.107 eq. 7-2). With Table 3
// defaults it evaluates to ≈ 94.77.
func basicSNR(no float64) float64 { return 15 - 1.5*(defSLR+no) }

// iolr is the impairment from too-low overall loudness rating (G.107
// eqs 7-9/7-10).
func iolr(no float64) float64 {
	xolr := defSLR + defRLR + 0.2*(64+no-defRLR)
	return 20 * (math.Pow(1+math.Pow(xolr/8, 8), 1.0/8) - xolr/8)
}

// ist is the non-optimum sidetone impairment (G.107 eqs 7-11/7-12). tMs is
// the echo-path delay T in ms: the talker echo joins the sidetone path
// attenuated by e^(−T/4) in STMRo.
func ist(tMs float64) float64 {
	stmro := -10 * math.Log10(math.Pow(10, -defSTMR/10)+
		math.Exp(-tMs/4)*math.Pow(10, -defTELR/10))
	return 12*math.Pow(1+math.Pow((stmro-13)/6, 8), 1.0/8) -
		28*math.Pow(1+math.Pow((stmro+1)/19.4, 35), 1.0/35) -
		13*math.Pow(1+math.Pow((stmro-3)/33, 13), 1.0/13) + 29
}

// iq is the quantizing-distortion impairment (G.107 eqs 7-13..7-17) at the
// default qdu = 1.
func iq(ro float64) float64 {
	q := 37 - 15*math.Log10(defQdu) // eq. 7-17
	g := 1.07 + 0.258*q + 0.0602*q*q
	y := (ro-100)/15 + 46/8.4 - g/9
	z := 46.0/30 - g/40
	return 15 * math.Log10(1+math.Pow(10, y)+math.Pow(10, z))
}

// idte is the narrowband talker-echo impairment (G.107 eqs 7-19..7-22) for
// echo-path delay tMs. For T < 1 ms the echo is perceived as sidetone and
// Idte = 0 (G.107 §7.4). The STMR < 9 dB and STMR > 20 dB adjustments
// (TERVs/Idtes, eqs 7-23/7-24) are unreachable at the pinned STMR = 15. The
// raw eq. 7-19 value is used unclamped, as the Recommendation writes it; it
// can be marginally negative (≈ −0.1) around T ≈ 1..3 ms where the
// 6·e^(−0.3T²) term still boosts TERV.
func idte(no, tMs float64) float64 {
	if tMs < 1 {
		return 0
	}
	roe := -1.5 * (no - defRLR) // eq. 7-20
	terv := defTELR - 40*math.Log10((1+tMs/10)/(1+tMs/150)) +
		6*math.Exp(-0.3*sq(tMs)) // eq. 7-22
	re := 80 + 2.5*(terv-14) // eq. 7-21
	d := roe - re
	return (d/2 + math.Sqrt(sq(d)/4+100) - 1) * (1 - math.Exp(-tMs)) // eq. 7-19
}

// idle is the listener-echo impairment (G.107 eqs 7-25/7-26) for 4-wire-loop
// round-trip delay trMs, shared verbatim by G.107.1 eqs 7-11/7-12 with
// Ro,WB in place of Ro. Nonzero (≈ 0.15) even at Tr = 0 because WEPL is
// finite — the residual that closes the default budget to R = 93.2.
func idle(ro, trMs float64) float64 {
	rle := 10.5 * (defWEPL + 7) * math.Pow(trMs+1, -0.25)
	d := ro - rle
	return d/2 + math.Sqrt(sq(d)/4+169)
}

// idd is the pure-delay impairment (G.107 eqs 7-27/7-28) with the default
// delay-sensitivity class (mT = 100 ms, sT = 1): exactly 0 up to the 100 ms
// knee, then
//
//	X = log10(Ta/100)/log10(2)
//	Idd = 25·[(1 + X⁶)^(1/6) − 3·(1 + (X/3)⁶)^(1/6) + 2]
//
// The wideband variant multiplies this by iddWBFactor (G.107.1 eq. 7-13).
func idd(taMs float64) float64 {
	if taMs <= defMT {
		return 0
	}
	x := math.Log10(taMs/defMT) / math.Log10(2)
	e := 6 * defST
	return 25 * (math.Pow(1+math.Pow(x, e), 1/e) -
		3*math.Pow(1+math.Pow(x/3, e), 1/e) + 2)
}

// sq is x².
func sq(x float64) float64 { return x * x }
