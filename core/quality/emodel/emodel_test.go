// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emodel

import (
	"math"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/rtp/codec"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// Test codec rows with pinned impairment parameters, independent of the
// live registry so recalibrating core/rtp/codec cannot silently move these
// golden values. G.113 Appendix I: G.711+PLC Ie=0/Bpl=25.1; G.729A+VAD
// Ie=11/Bpl=19. The wideband rows mirror the provisional Opus seeding.
var (
	g711 = codec.Codec{Name: "g711-test", Bpl: 25.1,
		Ptime: 20 * time.Millisecond, FrameLookahead: 250 * time.Microsecond}
	g729 = codec.Codec{Name: "g729-test", Ie: 11, Bpl: 19,
		Ptime: 20 * time.Millisecond, FrameLookahead: 5 * time.Millisecond}
	opus = codec.Codec{Name: "opus-test", Wideband: true,
		Ie: 5, Bpl: 15, IeWB: 5, BplWB: 15,
		Ptime: 20 * time.Millisecond, FrameLookahead: 6500 * time.Microsecond}
	wbClean = codec.Codec{Name: "wb-clean-test", Wideband: true, BplWB: 15}
)

func score(t *testing.T, cfg Config, in Input) Result {
	t.Helper()
	r, err := Score(cfg, in)
	if err != nil {
		t.Fatalf("Score(%+v, %+v): %v", cfg, in, err)
	}
	return r
}

// TestDefaultsGolden pins the zero-impairment narrowband budget: with all
// G.107 Table 3 defaults the formulas must land on the Recommendation's
// documented R = 93.2 (§7.7: "the calculation results in a very high quality
// with a rating factor of R = 93.2"), decomposed as Ro ≈ 94.77 and
// Is ≈ 1.41 COMPUTED from eqs 7-2..7-17 and Id ≈ 0.15 (residual listener
// echo at finite WEPL), never as stored constants.
func TestDefaultsGolden(t *testing.T) {
	r := score(t, Config{Codec: g711}, Input{})

	if !approx(r.R, 93.2, 0.01) {
		t.Fatalf("default R = %.6f, want 93.2 +- 0.01 (G.107 section 7.7)", r.R)
	}
	if r.Method != MethodG107 {
		t.Fatalf("method = %q, want %q", r.Method, MethodG107)
	}
	// Full-precision component pins (independently derived from the G.107
	// equations; intermediates: No = -61.179214, Iolr = 0.440178,
	// Ist = -0.000715, Iq = 0.974105).
	c := r.C
	for _, chk := range []struct {
		name      string
		got, want float64
	}{
		{"Ro", c.Ro, 94.768822},
		{"Is", c.Is, 1.413568},
		{"Idle", c.Idle, 0.149046},
		{"Id", c.Id, 0.149046},
		{"R", c.R, 93.206208},
	} {
		if !approx(chk.got, chk.want, 1e-4) {
			t.Errorf("default %s = %.6f, want %.6f", chk.name, chk.got, chk.want)
		}
	}
	if c.Idte != 0 || c.Idd != 0 || c.IeEff != 0 || c.A != 0 {
		t.Errorf("default Idte/Idd/IeEff/A = %v/%v/%v/%v, want all 0", c.Idte, c.Idd, c.IeEff, c.A)
	}
	// Annex B: R = 93.2 is quoted as MOS 4.41.
	if !approx(r.MOSCQ, 4.409406, 1e-4) {
		t.Errorf("default MOSCQ = %.6f, want 4.409406", r.MOSCQ)
	}
	// The registry's pcmu row carries the same Ie=0/Bpl=25.1 parameters and
	// must reproduce the default budget end to end.
	pcmu, err := codec.ByName("pcmu")
	if err != nil {
		t.Fatalf("ByName(pcmu): %v", err)
	}
	if reg := score(t, Config{Codec: pcmu}, Input{}); !approx(reg.R, 93.206208, 1e-4) {
		t.Errorf("registry pcmu default R = %.6f, want 93.206208", reg.R)
	}
}

// TestVerificationPoints pins hand-verified evaluations of the G.107
// equations at delay-only and loss-only operating points (the Table B.1
// style spot checks the design mandates). Every expected value was computed
// independently from eqs 7-19..7-29 and cross-checked against the
// intermediates quoted in the comments.
func TestVerificationPoints(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		in   Input
		want Components
		mos  float64
	}{
		{
			// T = 150, Tr = 300: TERV = 65 - 40*log10(16/2) = 28.876,
			// Re = 117.19, Roe = 94.769 -> Idte = 2.8118. Rle = 1228.5 /
			// 301^0.25 = 294.95 -> Idle = 0.8407. X = log10(1.5)/log10(2) =
			// 0.58496 -> Idd = 0.1635.
			name: "g711 Ta=150ms lossless",
			cfg:  Config{Codec: g711},
			in:   Input{Ta: 150 * time.Millisecond},
			want: Components{Ro: 94.768822, Is: 1.413568, Idte: 2.811844,
				Idle: 0.840747, Idd: 0.163531, Id: 3.816122, R: 89.539132},
			mos: 4.327546,
		},
		{
			// Deep past the knee: X = log10(2.5)/log10(2) = 1.3219 ->
			// Idd = 8.9167 dominates Id.
			name: "g711 Ta=250ms lossless",
			cfg:  Config{Codec: g711},
			in:   Input{Ta: 250 * time.Millisecond},
			want: Components{Ro: 94.768822, Is: 1.413568, Idte: 4.242856,
				Idle: 1.018587, Idd: 8.916710, Id: 14.178153, R: 79.177101},
			mos: 3.992519,
		},
		{
			// Exactly at the knee Idd must be identically zero (eq. 7-27
			// applies only for Ta > mT); the echo terms still move R.
			name: "g711 Ta=100ms knee",
			cfg:  Config{Codec: g711},
			in:   Input{Ta: 100 * time.Millisecond},
			want: Components{Ro: 94.768822, Is: 1.413568, Idte: 1.963791,
				Idle: 0.727733, Idd: 0, Id: 2.691524, R: 90.663730},
			mos: 4.354920,
		},
		{
			// Loss only: IeEff = 0 + 95*1/(1 + 25.1) = 95/26.1 = 3.6398.
			name: "g711 Ppl=1% random",
			cfg:  Config{Codec: g711},
			in:   Input{Ppl: 1, BurstR: 1},
			want: Components{Ro: 94.768822, Is: 1.413568, Idle: 0.149046,
				Id: 0.149046, IeEff: 3.639847, R: 89.566361},
			mos: 4.328232,
		},
		{
			// IeEff = 95*5/(5 + 25.1) = 475/30.1 = 15.7807.
			name: "g711 Ppl=5% random",
			cfg:  Config{Codec: g711},
			in:   Input{Ppl: 5, BurstR: 1},
			want: Components{Ro: 94.768822, Is: 1.413568, Idle: 0.149046,
				Id: 0.149046, IeEff: 15.780731, R: 77.425477},
			mos: 3.923091,
		},
		{
			// Analytically exact point: Ie=11, Bpl=19, Ppl=2 ->
			// IeEff = 11 + 84*2/21 = 19 exactly.
			name: "g729 Ppl=2% random",
			cfg:  Config{Codec: g729},
			in:   Input{Ppl: 2, BurstR: 1},
			want: Components{Ro: 94.768822, Is: 1.413568, Idle: 0.149046,
				Id: 0.149046, Ie: 11, IeEff: 19, R: 74.206208},
			mos: 3.787558,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := score(t, tc.cfg, tc.in)
			c := r.C
			for _, chk := range []struct {
				name      string
				got, want float64
			}{
				{"Ro", c.Ro, tc.want.Ro}, {"Is", c.Is, tc.want.Is},
				{"Idte", c.Idte, tc.want.Idte}, {"Idle", c.Idle, tc.want.Idle},
				{"Idd", c.Idd, tc.want.Idd}, {"Id", c.Id, tc.want.Id},
				{"Ie", c.Ie, tc.want.Ie}, {"IeEff", c.IeEff, tc.want.IeEff},
				{"R", c.R, tc.want.R},
			} {
				if !approx(chk.got, chk.want, 1e-4) {
					t.Errorf("%s = %.6f, want %.6f", chk.name, chk.got, chk.want)
				}
			}
			if !approx(r.MOSCQ, tc.mos, 1e-4) {
				t.Errorf("MOSCQ = %.6f, want %.6f", r.MOSCQ, tc.mos)
			}
		})
	}
}

// TestComponentsAudit checks the audit identity R = Ro - Is - Id - IeEff + A
// and Id = Idte + Idle + Idd on a grid of operating points, for both
// scoring methods: the breakdown must always reassemble into the headline
// number exactly.
func TestComponentsAudit(t *testing.T) {
	for _, cdc := range []codec.Codec{g711, g729, opus} {
		for _, ta := range []time.Duration{0, 50 * time.Millisecond, 150 * time.Millisecond, 400 * time.Millisecond} {
			for _, ppl := range []float64{0, 2, 20} {
				for _, burstR := range []float64{1, 2} {
					for _, a := range []float64{0, 10} {
						r := score(t, Config{Codec: cdc, A: a}, Input{Ta: ta, Ppl: ppl, BurstR: burstR})
						c := r.C
						if sum := c.Ro - c.Is - c.Id - c.IeEff + c.A; !approx(c.R, sum, 1e-9) {
							t.Fatalf("%s ta=%v ppl=%v: R = %v but Ro-Is-Id-IeEff+A = %v", cdc.Name, ta, ppl, c.R, sum)
						}
						if sum := c.Idte + c.Idle + c.Idd; !approx(c.Id, sum, 1e-9) {
							t.Fatalf("%s ta=%v: Id = %v but Idte+Idle+Idd = %v", cdc.Name, ta, c.Id, sum)
						}
						if c.R != r.R {
							t.Fatalf("%s: Result.R %v != Components.R %v", cdc.Name, r.R, c.R)
						}
						if c.A != a {
							t.Fatalf("%s: Components.A = %v, want %v", cdc.Name, c.A, a)
						}
					}
				}
			}
		}
	}
}

// TestMonotonicTa: beyond the 100 ms knee R must strictly decrease with Ta
// (Idd rises through eq. 7-27 and the echo terms through 7-19/7-25).
func TestMonotonicTa(t *testing.T) {
	steps := []time.Duration{100, 110, 120, 150, 200, 250, 300, 400, 500}
	for _, cfg := range []Config{{Codec: g711}, {Codec: opus}} {
		prev := math.Inf(1)
		for _, ms := range steps {
			r := score(t, cfg, Input{Ta: ms * time.Millisecond})
			if r.R >= prev {
				t.Fatalf("%s: R(Ta=%dms) = %v, not strictly below R at previous step (%v)",
					r.Method, ms, r.R, prev)
			}
			prev = r.R
		}
	}
}

// TestMonotonicPpl: R must strictly decrease as Ppl grows (eq. 7-29 is
// strictly increasing in Ppl for Bpl > 0).
func TestMonotonicPpl(t *testing.T) {
	for _, cfg := range []Config{{Codec: g711}, {Codec: g729}, {Codec: opus}} {
		prev := math.Inf(1)
		for _, ppl := range []float64{0, 0.5, 1, 2, 5, 10, 20, 50, 100} {
			r := score(t, cfg, Input{Ppl: ppl, BurstR: 1})
			if r.R >= prev {
				t.Fatalf("%s: R(Ppl=%v) = %v, not strictly below previous %v", cfg.Codec.Name, ppl, r.R, prev)
			}
			prev = r.R
		}
	}
}

// TestBurstRatio pins the eq. 7-29 burst behaviour: at the same Ppl a burst
// ratio above 1 shrinks the denominator Ppl/BurstR + Bpl and RAISES IeEff,
// so bursty loss scores strictly WORSE than random loss. (G.107 defines it
// this way — concentrated loss is more audible than scattered loss; a test
// expecting bursty loss to score better would contradict eq. 7-29.)
// Sub-1 values, including the zero value of Input.BurstR, clamp to 1.
func TestBurstRatio(t *testing.T) {
	random := score(t, Config{Codec: g711}, Input{Ppl: 5, BurstR: 1})
	bursty := score(t, Config{Codec: g711}, Input{Ppl: 5, BurstR: 2})
	if !approx(bursty.C.IeEff, 17.210145, 1e-4) { // 95*5/(5/2 + 25.1)
		t.Errorf("IeEff(BurstR=2) = %.6f, want 17.210145", bursty.C.IeEff)
	}
	if bursty.R >= random.R {
		t.Errorf("R(BurstR=2) = %v not below R(BurstR=1) = %v; eq. 7-29 says bursty loss scores worse", bursty.R, random.R)
	}
	for _, br := range []float64{0, 0.5, math.NaN()} {
		clamped := score(t, Config{Codec: g711}, Input{Ppl: 5, BurstR: br})
		if clamped.R != random.R {
			t.Errorf("R(BurstR=%v) = %v, want clamp to BurstR=1 value %v", br, clamped.R, random.R)
		}
	}
}

// TestWideband pins the G.107.1 path: the 129 scale, Is,WB = 0, the
// wideband Idd coefficient and the Annex A MOS map — all distinct from
// narrowband scoring.
func TestWideband(t *testing.T) {
	// Defaults, provisional Opus row (IeWB 5): R = 129 - Idle,WB(0.1537) - 5.
	r := score(t, Config{Codec: opus}, Input{})
	if r.Method != MethodG1071 {
		t.Fatalf("opus row scored as %q; codec.Wideband must select G.107.1", r.Method)
	}
	if !approx(r.R, 123.846315, 1e-4) || !approx(r.MOSCQ, 4.456839, 1e-4) {
		t.Errorf("opus defaults R/MOS = %.6f/%.6f, want 123.846315/4.456839", r.R, r.MOSCQ)
	}
	if r.C.Ro != 129 || r.C.Is != 0 {
		t.Errorf("wideband Ro/Is = %v/%v, want 129/0 (G.107.1 eqs 7-2/7-3)", r.C.Ro, r.C.Is)
	}

	// Zero-impairment wideband: only residual listener echo remains.
	clean := score(t, Config{Codec: wbClean}, Input{})
	if !approx(clean.C.Idle, 0.153685, 1e-4) || !approx(clean.R, 128.846315, 1e-4) {
		t.Errorf("clean WB Idle/R = %.6f/%.6f, want 0.153685/128.846315", clean.C.Idle, clean.R)
	}
	if !approx(clean.MOSCQ, 4.499152, 1e-4) {
		t.Errorf("clean WB MOSCQ = %.6f, want 4.499152", clean.MOSCQ)
	}

	// Delay point Ta = 150 ms: K = 18, TERV,WB = 46.876, Re,WB = 178.63,
	// Roe = 106.208 (No,WB = -68.806) -> Idte,WB = 0.3554; Idle on the 129
	// scale = 1.0123; Idd = 1.29 * 0.163531 = 0.2110.
	d := score(t, Config{Codec: opus}, Input{Ta: 150 * time.Millisecond})
	for _, chk := range []struct {
		name      string
		got, want float64
	}{
		{"Idte", d.C.Idte, 0.355448},
		{"Idle", d.C.Idle, 1.012266},
		{"Idd", d.C.Idd, 0.210955},
		{"R", d.R, 122.421331},
		{"MOSCQ", d.MOSCQ, 4.439743},
	} {
		if !approx(chk.got, chk.want, 1e-4) {
			t.Errorf("WB Ta=150ms %s = %.6f, want %.6f", chk.name, chk.got, chk.want)
		}
	}

	// The wideband Idd is the narrowband curve times 1.29 (G.107.1
	// eq. 7-13), pinned at Ta = 200 ms where X = 1.
	nb := score(t, Config{Codec: g711}, Input{Ta: 200 * time.Millisecond})
	wb := score(t, Config{Codec: wbClean}, Input{Ta: 200 * time.Millisecond})
	if !approx(nb.C.Idd, 3.044414, 1e-4) || !approx(wb.C.Idd, 3.927294, 1e-4) {
		t.Errorf("Idd at 200ms NB/WB = %.6f/%.6f, want 3.044414/3.927294", nb.C.Idd, wb.C.Idd)
	}
	if !approx(wb.C.Idd, 1.29*nb.C.Idd, 1e-9) {
		t.Errorf("Idd,WB = %v, want 1.29 * %v", wb.C.Idd, nb.C.Idd)
	}

	// Config.Wideband forces G.107.1 for a row not flagged wideband.
	forced := score(t, Config{Codec: g711, Wideband: true}, Input{})
	if forced.Method != MethodG1071 || forced.C.Ro != 129 {
		t.Errorf("forced wideband: method %q Ro %v, want %q 129", forced.Method, forced.C.Ro, MethodG1071)
	}
}

// TestWidebandKBelow100 value-pins the K-correction's sub-100 ms branch
// (G.107.1 eq. 7-9: K = 0.08·T + 10), which TestWideband's Ta = 150 ms point
// never reaches (there K = 18, eq. 7-10). Hand computation at Ta = 50 ms:
// K = 0.08·50 + 10 = 14; TERV,WB = 65 + 14 − 40·log10(6/(4/3)) = 52.8715;
// Re,WB = 80 + 3·(TERV−14) = 196.6145; Roe = −1.5·(No,WB − RLR) = 106.2083
// (No,WB = −68.8055) → Idte,WB = 0.092907. Idle at Tr = 100 ms on the 129
// scale = 0.652075; Idd = 0 (Ta ≤ 100 ms) → R = 128.255018, MOS 4.495632.
// A wrong coefficient in the sub-100 ms branch would survive the
// identity/monotonicity checks; this point would catch it.
func TestWidebandKBelow100(t *testing.T) {
	r := score(t, Config{Codec: wbClean}, Input{Ta: 50 * time.Millisecond})
	for _, chk := range []struct {
		name      string
		got, want float64
	}{
		{"Idte", r.C.Idte, 0.092907},
		{"Idle", r.C.Idle, 0.652075},
		{"Idd", r.C.Idd, 0},
		{"R", r.R, 128.255018},
		{"MOSCQ", r.MOSCQ, 4.495632},
	} {
		if !approx(chk.got, chk.want, 1e-4) {
			t.Errorf("WB Ta=50ms %s = %.6f, want %.6f", chk.name, chk.got, chk.want)
		}
	}
}

// TestMOSFromR pins the Annex B map: the endpoint clamps and the eq. B-4
// polynomial, cross-checked against Table B.1 (R 90/70/60/50 -> MOS
// 4.34/3.60/3.10/2.58 after rounding; at R = 80 the polynomial gives 4.024,
// which B.1's source table lists as 4.03).
func TestMOSFromR(t *testing.T) {
	cases := []struct{ r, want float64 }{
		{-5, 1}, {0, 1},
		{50, 2.575},
		{60, 3.1},
		{70, 3.597},
		{80, 4.024},
		{90, 4.339},
		{93.2, 4.409286},
		{100, 4.5}, {129, 4.5},
	}
	for _, tc := range cases {
		if got := MOSFromR(tc.r); !approx(got, tc.want, 1e-6) {
			t.Errorf("MOSFromR(%v) = %.6f, want %.6f", tc.r, got, tc.want)
		}
	}
	if got := MOSFromR(math.NaN()); !math.IsNaN(got) {
		t.Errorf("MOSFromR(NaN) = %v, want NaN", got)
	}
	// Mid-range value pinned: R = 50 -> 1 + 1.75 + 50*(-10)*50*7e-6 = 2.575.
	if got := MOSFromR(50); got != 2.575 {
		t.Errorf("MOSFromR(50) = %v, want exactly 2.575", got)
	}
}

// TestMOSFromRWB pins the Annex A map: Rx = R/1.29, so the wideband ceiling
// 129 (not 100) reaches MOS 4.5, and equal R values score differently under
// the two maps.
func TestMOSFromRWB(t *testing.T) {
	cases := []struct{ r, want float64 }{
		{-3, 1}, {0, 1},
		{64.5, 2.575},  // Rx = 50
		{116.1, 4.339}, // Rx = 90
		{129, 4.5}, {150, 4.5},
	}
	for _, tc := range cases {
		if got := MOSFromRWB(tc.r); !approx(got, tc.want, 1e-6) {
			t.Errorf("MOSFromRWB(%v) = %.6f, want %.6f", tc.r, got, tc.want)
		}
	}
	if nb, wb := MOSFromR(93.2), MOSFromRWB(93.2); approx(nb, wb, 1e-6) {
		t.Errorf("MOSFromR and MOSFromRWB agree at R=93.2 (%v); the maps must be distinct", nb)
	}
}

// TestIeEff exercises eq. 7-29 directly: the Ppl = 0 identity, analytic
// values, and the burstR and ppl clamps.
func TestIeEff(t *testing.T) {
	cases := []struct {
		name                 string
		ie, bpl, ppl, burstR float64
		want                 float64
	}{
		{"no loss returns Ie", 11, 19, 0, 1, 11},
		{"negative ppl returns Ie", 11, 19, -3, 1, 11},
		{"g729 2% exact", 11, 19, 2, 1, 19}, // 11 + 84*2/(2+19)
		{"g711 1%", 0, 25.1, 1, 1, 3.639847},
		{"g711 5% bursty", 0, 25.1, 5, 2, 17.210145},
		{"burstR below 1 clamps", 0, 25.1, 5, 0.5, 15.780731},
		{"ppl above 100 clamps", 0, 25.1, 200, 1, 75.939249}, // 9500/125.1
	}
	for _, tc := range cases {
		if got := IeEff(tc.ie, tc.bpl, tc.ppl, tc.burstR); !approx(got, tc.want, 1e-4) {
			t.Errorf("%s: IeEff(%v,%v,%v,%v) = %.6f, want %.6f",
				tc.name, tc.ie, tc.bpl, tc.ppl, tc.burstR, got, tc.want)
		}
	}
}

// TestComposeTa pins the Ta composition: network OWD + jitter-buffer nominal
// + packetization interval (which covers frame accumulation) + codec encoder
// lookahead — never frame + lookahead + ptime, which double-counts the frame
// time (an Opus 20 ms packet is transmittable at ptime + 6.5 ms lookahead).
func TestComposeTa(t *testing.T) {
	cases := []struct {
		name    string
		owd, jb time.Duration
		c       codec.Codec
		want    time.Duration
	}{
		{"g711 40+40", 40 * time.Millisecond, 40 * time.Millisecond, g711,
			100250 * time.Microsecond}, // 40 + 40 + 0.25 + 20
		{"g729 10+30", 10 * time.Millisecond, 30 * time.Millisecond, g729,
			65 * time.Millisecond}, // 10 + 30 + 5 + 20
		{"opus codec delay only", 0, 0, opus,
			26500 * time.Microsecond}, // 6.5 + 20
		{"zero ptime defaults", 5 * time.Millisecond, 0,
			codec.Codec{Name: "bare"}, 5*time.Millisecond + codec.DefaultPtime},
	}
	for _, tc := range cases {
		if got := ComposeTa(tc.owd, tc.jb, tc.c); got != tc.want {
			t.Errorf("%s: ComposeTa = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAdvantage: A shifts R by exactly A (eq. 7-1) and is reported in the
// breakdown; the MOS map clamps the shifted rating.
func TestAdvantage(t *testing.T) {
	r := score(t, Config{Codec: g711, A: 10}, Input{})
	if !approx(r.R, 103.206208, 1e-4) || r.C.A != 10 {
		t.Fatalf("A=10: R = %.6f (C.A %v), want 103.206208 (10)", r.R, r.C.A)
	}
	if r.MOSCQ != 4.5 {
		t.Fatalf("A=10: MOSCQ = %v, want 4.5 (clamped above R=100)", r.MOSCQ)
	}
}

// TestScoreErrors: out-of-domain inputs must be refused, never scored into
// a plausible number.
func TestScoreErrors(t *testing.T) {
	bad := []struct {
		name string
		cfg  Config
		in   Input
	}{
		{"negative Ppl", Config{Codec: g711}, Input{Ppl: -0.1}},
		{"Ppl above 100", Config{Codec: g711}, Input{Ppl: 100.1}},
		{"NaN Ppl", Config{Codec: g711}, Input{Ppl: math.NaN()}},
		{"negative Ta", Config{Codec: g711}, Input{Ta: -time.Nanosecond}},
		{"negative A", Config{Codec: g711, A: -0.1}, Input{}},
		{"A above 20", Config{Codec: g711, A: 20.1}, Input{}},
		{"NaN A", Config{Codec: g711, A: math.NaN()}, Input{}},
		{"loss without Bpl", Config{Codec: codec.Codec{Name: "no-bpl"}}, Input{Ppl: 1}},
		// g711's test row has no wideband Bpl: forcing G.107.1 with loss
		// must refuse rather than divide by the zero BplWB.
		{"forced wideband without BplWB", Config{Codec: g711, Wideband: true}, Input{Ppl: 1}},
	}
	for _, tc := range bad {
		if _, err := Score(tc.cfg, tc.in); err == nil {
			t.Errorf("%s: Score accepted %+v / %+v", tc.name, tc.cfg, tc.in)
		}
	}
	// Domain edges are valid.
	good := []struct {
		name string
		cfg  Config
		in   Input
	}{
		{"Ppl=100", Config{Codec: g711}, Input{Ppl: 100}},
		{"A=20", Config{Codec: g711, A: 20}, Input{}},
		{"A=0", Config{Codec: g711}, Input{}},
		{"lossless without Bpl", Config{Codec: codec.Codec{Name: "no-bpl"}}, Input{}},
	}
	for _, tc := range good {
		if _, err := Score(tc.cfg, tc.in); err != nil {
			t.Errorf("%s: Score rejected valid input: %v", tc.name, err)
		}
	}
}
