// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emodel

import "math"

// ITU-T G.107.1 (06/2019) wideband constants.
const (
	// roWB is the wideband basic signal-to-noise ratio: fixed at 129
	// (G.107.1 eq. 7-2; noise effects are "for further study" there).
	roWB = 129.0
	// iddWBFactor scales the narrowband Idd curve for wideband scoring
	// (G.107.1 eq. 7-13: Idd = 25·1.29·{…}).
	iddWBFactor = 1.29
)

// idteWB is the wideband talker-echo impairment (G.107.1 eqs 7-5..7-10) for
// echo-path delay tMs. Differences from the narrowband idte: the TERV gains
// the K correction (K = 0.08·T + 10 below 100 ms, 18 at and above — the two
// branches meet at T = 100), Re,WB weighs TERV with coefficient 3 instead of
// 2.5, and there is no sidetone interaction. Roe needs No,WB, which G.107.1
// leaves undefined; it is computed with the G.107 noise summation at the
// G.107.1 Table 1 defaults (Nfor = −96), see the package comment.
func idteWB(tMs float64) float64 {
	roe := -1.5 * (noise(defNforWB) - defRLR) // eq. 7-6
	k := 0.08*tMs + 10                        // eq. 7-9
	if tMs >= 100 {
		k = 18 // eq. 7-10
	}
	terv := defTELR + k - 40*math.Log10((1+tMs/10)/(1+tMs/150)) +
		6*math.Exp(-0.3*sq(tMs)) // eq. 7-8
	re := 80 + 3*(terv-14) // eq. 7-7
	d := roe - re
	return (d/2 + math.Sqrt(sq(d)/4+100) - 1) * (1 - math.Exp(-tMs)) // eq. 7-5
}
