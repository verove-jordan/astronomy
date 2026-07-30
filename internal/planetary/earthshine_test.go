package planetary

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// The synthetic scene shared by the earthshine tests: a crescent (litK 0.6) with a real-but-faint
// dark-side signal in the linear master and a stretched finish where the dark side is still black.
const (
	esTestW, esTestH   = 900, 900
	esTestCX, esTestCY = 450.0, 450.0
	esTestR            = 320.0
	esTestLitK         = 0.6
	esTestSkyLin       = 0.0005
	esTestDarkLin      = 0.003
	esTestFinSky       = 0.004
	esTestFinDark      = 0.02
	esTestFinLit       = 0.85
)

func writeCrescentFixtures(t *testing.T, dir string, masterDark float64) (monoBase, outBase string, before *fits.Image) {
	t.Helper()
	master := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, masterDark, esTestSkyLin, 0.0004)
	monoBase = filepath.Join(dir, "master_mono")
	require.NoError(t, master.WriteFITS(monoBase+".fits"))
	finish := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	outBase = filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))
	return monoBase, outBase, finish
}

func boxMedian(im *fits.Image, ch, x0, y0, half int) float64 {
	var vals []float64
	for y := y0 - half; y <= y0+half; y++ {
		for x := x0 - half; x <= x0+half; x++ {
			vals = append(vals, float64(im.Pix[ch][y*im.W+x]))
		}
	}
	return medianOf(vals)
}

func assertRegionEqual(t *testing.T, before, after *fits.Image, x0, y0, size int, what string) {
	t.Helper()
	for c := range before.Pix {
		for y := y0; y < y0+size; y++ {
			for x := x0; x < x0+size; x++ {
				i := y*before.W + x
				require.Equal(t, before.Pix[c][i], after.Pix[c][i],
					"%s: pixel (%d,%d) ch %d must be untouched", what, x, y, c)
			}
		}
	}
}

// assertLitSurfaceIdentical is the v2 strengthened contract: EVERY pixel above the lit
// threshold in the linear master — the entire lit surface, old guard band included — must be
// byte-identical in every channel (the mask's hard-zero support covers the dilated lit set,
// a superset of this).
func assertLitSurfaceIdentical(t *testing.T, master, before, after *fits.Image) {
	t.Helper()
	bg, pk, ok := litStats(master.Pix[0])
	require.True(t, ok, "master must have a usable range")
	thr := litThreshold(bg, pk)
	lit, mismatches := 0, 0
	for i, v := range master.Pix[0] {
		if v <= thr {
			continue
		}
		lit++
		for c := range before.Pix {
			if before.Pix[c][i] != after.Pix[c][i] {
				mismatches++
			}
		}
	}
	require.Greater(t, lit, 10000, "fixture must keep a solid lit surface")
	assert.Zero(t, mismatches, "every lit-surface pixel must stay byte-identical (of %d lit px)", lit)
}

func TestApplyEarthshine_LiftsDarkSide(t *testing.T) {
	dir := t.TempDir()
	monoBase, outBase, before := writeCrescentFixtures(t, dir, esTestDarkLin)
	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1

	notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "earthshine: disc r=")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	// The dark-side median lands at the target display level (0.10 × gain).
	darkMed := boxMedian(out, 0, int(esTestCX-esTestR/2), int(esTestCY), 25)
	assert.InDelta(t, 0.10, darkMed, 0.03, "dark side lifted to the target level")
	// The sky far outside the limb is bit-identical.
	assertRegionEqual(t, before, out, 20, 20, 60, "sky")
	// v2 contract: the ENTIRE lit surface is byte-identical, not just a deep sample.
	master, err := fits.ReadImage(monoBase + ".fits")
	require.NoError(t, err)
	assertLitSurfaceIdentical(t, master, before, out)
	// Luminance stays monotone across the terminator band: no bright band from the lift.
	assertNoTerminatorBump(t, out)
	// Provenance dropped beside the outputs.
	data, err := os.ReadFile(filepath.Join(dir, "earthshine.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"applied": true`)
}

// assertNoTerminatorBump walks the centre row from deep dark side to deep lit side and requires the
// luminance profile to be (near-)monotone: a local maximum in the blend band would be a visible arc.
func assertNoTerminatorBump(t *testing.T, im *fits.Image) {
	t.Helper()
	assertTerminatorProfile(t, im, 0.01)
}

// assertTerminatorProfile is the monotone walk with an explicit dip tolerance. The fixture's
// terminator is a 6-px cliff — far steeper than any seeing-blurred real one — so the knob's
// MAX feather (whose whole point is a wider byte-identical margin that renders the finish
// itself) is allowed a proportionally deeper dip there; the default feather keeps the strict
// 0.01 bound.
func assertTerminatorProfile(t *testing.T, im *fits.Image, tol float64) {
	t.Helper()
	y := int(esTestCY)
	smooth := func(x int) float64 { // 11-px box along the row to suppress pixel noise
		var s float64
		for dx := -5; dx <= 5; dx++ {
			s += float64(im.Pix[0][y*im.W+x+dx])
		}
		return s / 11
	}
	prev := smooth(int(esTestCX - esTestR/2))
	for x := int(esTestCX - esTestR/2); x <= int(esTestCX+0.75*esTestR); x += 4 {
		cur := smooth(x)
		require.GreaterOrEqual(t, cur, prev-tol, "luminance dips then rises around x=%d — terminator band artifact", x)
		if cur > prev {
			prev = cur
		}
	}
}

func TestApplyEarthshine_Skips(t *testing.T) {
	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1

	t.Run("gain off is a no-op", func(t *testing.T) {
		off := siril.DefaultPlanetaryFinish()
		assert.Nil(t, applyEarthshine("", "", "", "", "whatever", "also", off))
	})
	t.Run("dark side below the noise floor", func(t *testing.T) {
		dir := t.TempDir()
		monoBase, outBase, before := writeCrescentFixtures(t, dir, esTestSkyLin) // dark == sky
		notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
		require.Len(t, notes, 1)
		assert.Contains(t, notes[0], "below the noise floor")
		out, err := fits.ReadImage(outBase + ".fits")
		require.NoError(t, err)
		assertRegionEqual(t, before, out, 0, 0, 64, "untouched finish")
	})
	t.Run("full moon has nothing to lift", func(t *testing.T) {
		dir := t.TempDir()
		master := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, -1, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
		monoBase := filepath.Join(dir, "master_mono")
		require.NoError(t, master.WriteFITS(monoBase+".fits"))
		finish := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, -1, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
		outBase := filepath.Join(dir, "moon_stack")
		require.NoError(t, finish.WriteFITS(outBase+".fits"))
		notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
		require.Len(t, notes, 1)
		assert.Contains(t, notes[0], "nothing to lift")
	})
}

// stampRect writes a constant value into a rectangle of every channel (test-fixture detail).
func stampRect(im *fits.Image, x0, y0, w, h int, v float32) {
	for c := range im.Pix {
		for y := y0; y < y0+h; y++ {
			for x := x0; x < x0+w; x++ {
				im.Pix[c][y*im.W+x] = v
			}
		}
	}
}

// The 2026-07-12 real-sky defect class: DIM LIT surface must never be filled by the earthshine
// layer. The 0.08 band is dimmer than the relative cap (protected ONLY by the lit guard) and the
// 0.20 band sits above it (protected ONLY by the cap) — the two mechanisms are pinned
// independently. Both bands sit ≥3σ inside the lit region, away from the terminator ramp and
// off the monotonicity-walk row.
func TestApplyEarthshine_DimLitPatchesUntouched(t *testing.T) {
	dir := t.TempDir()
	master := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
	stampRect(master, 690, 380, 40, 40, 0.30) // bright lit ground in the linear master
	monoBase := filepath.Join(dir, "master_mono")
	require.NoError(t, master.WriteFITS(monoBase+".fits"))
	finish := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	stampRect(finish, 690, 380, 40, 20, 0.08) // dim lit detail below the relative cap → guard territory
	stampRect(finish, 690, 400, 40, 20, 0.20) // mid lit detail above the relative cap → cap territory
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))
	before := finish.Clone()

	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1
	notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "earthshine: disc", "the lift must still apply")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	assertRegionEqual(t, before, out, 690, 380, 20, "dim lit band (guard)")
	assertRegionEqual(t, before, out, 690, 400, 20, "mid lit band (cap)")
	darkMed := boxMedian(out, 0, int(esTestCX-esTestR/2), int(esTestCY), 25)
	assert.InDelta(t, 0.10, darkMed, 0.03, "the guard must not kill the dark-side lift")
}

// A large shadow connected to nothing bright within ~σ, deep inside the lit zone, is genuinely
// earthshine-lit and receives the fill — the documented enclave semantics (small enclaves ≤0.64σ
// stay bit-identical; this 44 px bay does not).
func TestApplyEarthshine_ShadowBayFills(t *testing.T) {
	dir := t.TempDir()
	master := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
	stampRect(master, 660, 500, 44, 44, esTestDarkLin) // earthshine-level signal inside the lit zone
	monoBase := filepath.Join(dir, "master_mono")
	require.NoError(t, master.WriteFITS(monoBase+".fits"))
	finish := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	stampRect(finish, 660, 500, 44, 44, esTestFinDark)
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))

	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1
	notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "earthshine: disc")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	assert.InDelta(t, 0.10, boxMedian(out, 0, 682, 522, 12), 0.03,
		"a wide shadow bay is earthshine-lit and fills to the target")
}

func TestApplyEarthshine_GainScalesTheTarget(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	mono1, out1, _ := writeCrescentFixtures(t, dir1, esTestDarkLin)
	mono2, out2, _ := writeCrescentFixtures(t, dir2, esTestDarkLin)
	fin := siril.DefaultPlanetaryFinish()

	fin.EarthshineGain = 1
	applyEarthshine("", "", "", "", mono1, out1, fin)
	fin.EarthshineGain = 2
	applyEarthshine("", "", "", "", mono2, out2, fin)

	a, err := fits.ReadImage(out1 + ".fits")
	require.NoError(t, err)
	b, err := fits.ReadImage(out2 + ".fits")
	require.NoError(t, err)
	x, y := int(esTestCX-esTestR/2), int(esTestCY)
	assert.InDelta(t, 0.20, boxMedian(b, 0, x, y, 25), 0.05, "gain 2 → target 0.20")
	assert.Greater(t, boxMedian(b, 0, x, y, 25), boxMedian(a, 0, x, y, 25)+0.05, "higher gain lifts more")
	// The relative cap scales with gain (0.32 here) — the terminator handover must stay smooth.
	assertNoTerminatorBump(t, b)
}

func TestApplyEarthshine_ColourTint(t *testing.T) {
	dir := t.TempDir()
	const w2, h2 = 700, 700
	const ccx, ccy, cr = 350.0, 350.0, 250.0
	bases := map[string]float64{"R": 0.0024, "G": 0.003, "B": 0.0036} // bluish real dark side
	paths := map[string]string{}
	for label, dark := range bases {
		im := drawMoon(w2, h2, ccx, ccy, cr, esTestLitK, 0.5, dark, esTestSkyLin, 0.0004)
		paths[label] = filepath.Join(dir, "master_"+label)
		require.NoError(t, im.WriteFITS(paths[label]+".fits"))
	}
	l := drawMoon(w2, h2, ccx, ccy, cr, esTestLitK, 0.5, 0.003, esTestSkyLin, 0.0004)
	lBase := filepath.Join(dir, "master_L")
	require.NoError(t, l.WriteFITS(lBase+".fits"))

	plane := drawMoon(w2, h2, ccx, ccy, cr, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	finish := fits.NewImage(w2, h2, 3)
	for c := 0; c < 3; c++ {
		copy(finish.Pix[c], plane.Pix[0])
	}
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))

	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1
	notes := applyEarthshine(paths["R"], paths["G"], paths["B"], lBase, "", outBase, fin)
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "earthshine: disc")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	x, y := int(ccx-cr/2), int(ccy)
	for c := 0; c < 3; c++ {
		assert.InDelta(t, 0.10, boxMedian(out, c, x, y, 20), 0.04, "channel %d lifted to ~target", c)
	}
	assert.Greater(t, boxMedian(out, 2, x, y, 20), boxMedian(out, 0, x, y, 20),
		"the real bluish dark-side colour survives (desaturated, not erased)")
}

// A 4×4 master-dark enclave DEEP inside the lit zone (a small crater shadow) stays
// byte-identical: the litGuard's enclave protection — its only remaining v2 job — zeroes the
// mask there even though the master value is at earthshine level.
func TestApplyEarthshine_SmallEnclaveUntouched(t *testing.T) {
	dir := t.TempDir()
	master := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
	stampRect(master, 700, 550, 4, 4, esTestDarkLin)
	monoBase := filepath.Join(dir, "master_mono")
	require.NoError(t, master.WriteFITS(monoBase+".fits"))
	finish := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	stampRect(finish, 700, 550, 4, 4, esTestFinDark)
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))
	before := finish.Clone()

	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1
	notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "earthshine: disc", "the lift must still apply")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	assertRegionEqual(t, before, out, 700, 550, 4, "small crater-shadow enclave")
}

// The v2 shoulder pin: dark-side features at 1×/3×/6× the dark median must stay STRICTLY
// ordered with real gaps — under v1's flat cap (1.6×target = 0.16) the 3× and 6× features
// both clipped to ~0.16 and the relief died. A fine ±50% texture patch must keep spread
// through the moderated denoise.
func TestApplyEarthshine_DarkSideReliefPreserved(t *testing.T) {
	dir := t.TempDir()
	master := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
	stampRect(master, 220, 320, 60, 60, 0.008)  // 3× the dark median (sky-subtracted)
	stampRect(master, 220, 480, 60, 60, 0.0155) // 6× — Aristarchus-class
	for y := 320; y < 384; y++ {                // 8-px checkerboard ±50% texture
		for x := 350; x < 414; x++ {
			v := float32(esTestSkyLin + 0.00125)
			if (x/8+y/8)%2 == 0 {
				v = float32(esTestSkyLin + 0.00375)
			}
			master.Pix[0][y*esTestW+x] = v
		}
	}
	monoBase := filepath.Join(dir, "master_mono")
	require.NoError(t, master.WriteFITS(monoBase+".fits"))
	finish := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))

	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1
	notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "earthshine: disc")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	base := boxMedian(out, 0, int(esTestCX-esTestR/2), int(esTestCY), 20) // plain dark surface = 1×
	b3 := boxMedian(out, 0, 250, 350, 20)
	b6 := boxMedian(out, 0, 250, 510, 20)
	assert.InDelta(t, 0.10, base, 0.02, "1× dark surface stays at the anchored level")
	assert.GreaterOrEqual(t, b3-base, 0.04, "3× feature keeps real contrast over the median (b3=%.3f)", b3)
	assert.GreaterOrEqual(t, b6-b3, 0.02, "6× feature keeps headroom over 3× — the flat cap killed this (b6=%.3f)", b6)
	assert.GreaterOrEqual(t, b6, 0.17, "the brightest earthshine feature rides the shoulder, not a clip")

	var tex []float64
	for y := 326; y < 378; y++ {
		for x := 356; x < 408; x++ {
			tex = append(tex, float64(out.Pix[0][y*esTestW+x]))
		}
	}
	sortFloats := append([]float64(nil), tex...)
	p10 := percentileOf(sortFloats, 0.10)
	p90 := percentileOf(sortFloats, 0.90)
	assert.GreaterOrEqual(t, p90-p10, 0.02, "fine dark-side texture survives the moderated denoise")
}

// percentileOf sorts a copy and returns the q-quantile (test helper).
func percentileOf(vals []float64, q float64) float64 {
	s := append([]float64(nil), vals...)
	for i := 1; i < len(s); i++ { // insertion sort keeps the helper dependency-free
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s[int(q*float64(len(s)-1))]
}

// Two-tone dark side: each half keeps its own hue sign in the output (a single global tint
// cannot satisfy both), desaturated toward the lit tone but not erased.
func TestApplyEarthshine_PerPixelColour(t *testing.T) {
	dir := t.TempDir()
	labels := []string{"R", "G", "B"}
	masters := map[string]*fits.Image{}
	paths := map[string]string{}
	for _, lab := range labels {
		masters[lab] = drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
	}
	// Patch A (y≈350): bluish — B master brighter, R dimmer. Patch B (y≈550): reddish.
	stampRect(masters["B"], 220, 320, 60, 60, 0.0042)
	stampRect(masters["R"], 220, 320, 60, 60, 0.0021)
	stampRect(masters["R"], 220, 520, 60, 60, 0.0042)
	stampRect(masters["B"], 220, 520, 60, 60, 0.0021)
	for _, lab := range labels {
		paths[lab] = filepath.Join(dir, "master_"+lab)
		require.NoError(t, masters[lab].WriteFITS(paths[lab]+".fits"))
	}
	l := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
	lBase := filepath.Join(dir, "master_L")
	require.NoError(t, l.WriteFITS(lBase+".fits"))
	plane := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	finish := fits.NewImage(esTestW, esTestH, 3)
	for c := 0; c < 3; c++ {
		copy(finish.Pix[c], plane.Pix[0])
	}
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))

	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1
	notes := applyEarthshine(paths["R"], paths["G"], paths["B"], lBase, "", outBase, fin)
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "earthshine: disc")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	bA, rA := boxMedian(out, 2, 250, 350, 18), boxMedian(out, 0, 250, 350, 18)
	bB, rB := boxMedian(out, 2, 250, 550, 18), boxMedian(out, 0, 250, 550, 18)
	assert.Greater(t, bA, rA+0.005, "patch A keeps its bluish hue (B=%.3f R=%.3f)", bA, rA)
	assert.Greater(t, rB, bB+0.005, "patch B keeps its reddish hue (R=%.3f B=%.3f)", rB, bB)
	// Desaturated: the linear ratio was 2×; the rendered ratio must be far tamer.
	assert.Less(t, bA/rA, 1.35, "the hue survives desaturated, not amplified")
}

// A missing colour master no longer falls back to NEUTRAL grey (v1's colour-step defect):
// the dark side adopts the lit disc's own rendered tone.
func TestApplyEarthshine_ColourFallbackMatchesLitSide(t *testing.T) {
	dir := t.TempDir()
	l := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
	lBase := filepath.Join(dir, "master_L")
	require.NoError(t, l.WriteFITS(lBase+".fits"))
	plane := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	finish := fits.NewImage(esTestW, esTestH, 3)
	tints := [3]float32{1.06, 1.0, 0.94} // a warm mineral lit tone
	for c := 0; c < 3; c++ {
		for i, v := range plane.Pix[0] {
			finish.Pix[c][i] = v * tints[c]
		}
	}
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))

	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1
	notes := applyEarthshine("", "", "", lBase, "", outBase, fin) // no R/G/B masters at all
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "earthshine: disc")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	x, y := int(esTestCX-esTestR/2), int(esTestCY)
	rOut, bOut := boxMedian(out, 0, x, y, 20), boxMedian(out, 2, x, y, 20)
	assert.InDelta(t, float64(tints[0]/tints[2]), rOut/bOut, 0.06,
		"the lifted dark side matches the LIT side's tone, not neutral grey (R/B=%.3f)", rOut/bOut)
	data, err := os.ReadFile(filepath.Join(dir, "earthshine.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"chroma_mode": "lit-fallback"`)
}

// The feather knob moves the terminator protection band; the lit surface stays byte-identical
// and the profile monotone at both extremes.
func TestApplyEarthshine_FeatherKnob(t *testing.T) {
	outs := map[float64]*fits.Image{}
	var master *fits.Image
	for _, feather := range []float64{0.002, 0.02} {
		dir := t.TempDir()
		monoBase, outBase, before := writeCrescentFixtures(t, dir, esTestDarkLin)
		fin := siril.DefaultPlanetaryFinish()
		fin.EarthshineGain = 1
		fin.EarthshineFeather = feather
		notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
		require.NotEmpty(t, notes)
		require.Contains(t, notes[0], "earthshine: disc")
		out, err := fits.ReadImage(outBase + ".fits")
		require.NoError(t, err)
		m, err := fits.ReadImage(monoBase + ".fits")
		require.NoError(t, err)
		master = m
		assertLitSurfaceIdentical(t, m, before, out)
		if feather <= esFeatherFracDefault {
			assertNoTerminatorBump(t, out)
		} else {
			// Max feather = a deliberately wider protected margin; on the fixture's 6-px
			// terminator cliff that margin shows the finish's own darkness (see
			// assertTerminatorProfile) — bounded, and invisible on real seeing-wide terminators.
			assertTerminatorProfile(t, out, 0.035)
		}
		outs[feather] = out
		data, err := os.ReadFile(filepath.Join(dir, "earthshine.json"))
		require.NoError(t, err)
		assert.Contains(t, string(data), fmt.Sprintf(`"feather": %g`, feather))
	}
	diff := 0
	for i := range outs[0.002].Pix[0] {
		if outs[0.002].Pix[0][i] != outs[0.02].Pix[0][i] {
			diff++
		}
	}
	_ = master
	assert.Greater(t, diff, 500, "the two feathers must render a different terminator band")
}

// Property pin for the hard-zero support: LIT-level stamps anywhere on the disc — even on the
// dark side — are above the lit threshold and must stay byte-identical (no arithmetic ever
// touches a pixel the mask excludes).
func TestApplyEarthshine_LitMaskHardZero(t *testing.T) {
	stamps := []struct{ x, y, size int }{{250, 250, 24}, {320, 600, 10}, {420, 200, 40}, {560, 640, 16}}
	dir := t.TempDir()
	master := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, 0.5, esTestDarkLin, esTestSkyLin, 0.0004)
	finish := drawMoon(esTestW, esTestH, esTestCX, esTestCY, esTestR, esTestLitK, esTestFinLit, esTestFinDark, esTestFinSky, 0.0005)
	for _, s := range stamps {
		stampRect(master, s.x, s.y, s.size, s.size, 0.5) // lit-level "hot rock" on the dark side
		stampRect(finish, s.x, s.y, s.size, s.size, esTestFinLit)
	}
	monoBase := filepath.Join(dir, "master_mono")
	require.NoError(t, master.WriteFITS(monoBase+".fits"))
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, finish.WriteFITS(outBase+".fits"))
	before := finish.Clone()

	fin := siril.DefaultPlanetaryFinish()
	fin.EarthshineGain = 1
	notes := applyEarthshine("", "", "", "", monoBase, outBase, fin)
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "earthshine: disc")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	for _, s := range stamps {
		assertRegionEqual(t, before, out, s.x, s.y, s.size, fmt.Sprintf("lit-level stamp at (%d,%d)", s.x, s.y))
	}
}

// writeBareFITS writes a minimal BITPIX -32 FITS without a ROWORDER card — the legacy bottom-up
// convention readAligned must reconcile against a TOP-DOWN finish.
func writeBareFITS(t *testing.T, path string, im *fits.Image) {
	t.Helper()
	pad := func(s string) string { return s + strings.Repeat(" ", 80-len(s)) }
	header := pad("SIMPLE  =                    T") +
		pad("BITPIX  =                  -32") +
		pad("NAXIS   =                    2") +
		pad(fmt.Sprintf("NAXIS1  = %20d", im.W)) +
		pad(fmt.Sprintf("NAXIS2  = %20d", im.H)) +
		pad("END")
	if rem := len(header) % 2880; rem != 0 {
		header += strings.Repeat(" ", 2880-rem)
	}
	data := make([]byte, im.W*im.H*4)
	for i, v := range im.Pix[0] {
		binary.BigEndian.PutUint32(data[i*4:], math.Float32bits(v))
	}
	if rem := len(data) % 2880; rem != 0 {
		data = append(data, make([]byte, 2880-rem)...)
	}
	require.NoError(t, os.WriteFile(path, append([]byte(header), data...), 0o644))
}

func TestReadAligned_FlipsMismatchedRowOrder(t *testing.T) {
	dir := t.TempDir()
	im := drawMoon(64, 64, 32, 20, 18, 0, 0.8, 0.01, 0.001, 0) // vertically asymmetric scene
	flipped := im.Clone()
	flipRows(flipped)
	base := filepath.Join(dir, "bare")
	writeBareFITS(t, base+".fits", flipped)

	ref := fits.NewImage(64, 64, 1) // stands in for a Siril TOP-DOWN finish
	got, err := readAligned(base, ref, "TOP-DOWN")
	require.NoError(t, err)
	assert.Equal(t, im.Pix[0], got.Pix[0], "a ROWORDER-less (bottom-up) master is flipped onto the finish grid")
}

func TestFlipRows_OddHeight(t *testing.T) {
	im := fits.NewImage(2, 3, 1)
	copy(im.Pix[0], []float32{1, 2, 3, 4, 5, 6})
	flipRows(im)
	assert.Equal(t, []float32{5, 6, 3, 4, 1, 2}, im.Pix[0])
}
