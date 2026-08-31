package solar

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// pairstack_test.go measures what a moving occulter does to a stack.
//
// The Moon travels 0.508 arcseconds per second against the Sun. At the 3.1"/px plate scale of the
// 12 Aug 2026 clips that is 4.9 px across a thirty-second window, against a point spread function
// measured at about 2 px full width — so the edge a stack renders is dominated by the occulter's
// motion, not by the optics, unless something is done about it.
//
// The failure is not subtle once it is looked at directly, and it is invisible on every whole-image
// metric: averaging Moon with Sun along the edge's path replaces a step with a RAMP, and a ramp is
// smooth, so band-pass detail, limb PSF and disc contrast all read it as a perfectly good stack.
// This measures the thing itself — the width of the transition — which is the only number that can
// tell a rendered edge from an averaged one.

// sweepingOcculter builds a window of frames sharing one Sun, with the occulter marching across by
// driftPx in total. withMoon decides whether the frames carry the occulter's geometry, which is what
// the stack masks against — so the two cases differ in nothing but that.
func sweepingOcculter(t *testing.T, dir string, n int, driftPx float64, withMoon bool) []Frame {
	t.Helper()
	base := defaultSun()
	base.w, base.h = 1000, 1000
	base.cx, base.cy, base.r = 500, 500, 330
	base.features = 18
	base.ringAmp = 0
	base.psfSigma = 1.2
	// The occulter sits a third of a radius to the +x side, so the crescent survives on the −x side
	// and the edge under test is the occulter's own −x limb.
	base.moonR = 1.02 * base.r
	base.moonCY = base.cy

	tag := "masked"
	if !withMoon {
		tag = "plain"
	}
	frames := make([]Frame, 0, n)
	for i := 0; i < n; i++ {
		s := base
		s.noiseSeed = uint64(2000 + i)
		// The Moon moves and the Sun does not — the whole point. Its centre marches by driftPx over
		// the window, exactly as it does across a real thirty seconds.
		s.moonCX = base.cx + 0.33*base.r + driftPx*(float64(i)/float64(n-1)-0.5)
		im := drawSun(s)
		p := filepath.Join(dir, fmt.Sprintf("%s_%03d.fits", tag, i))
		require.NoError(t, im.WriteFITS(p))

		g, ok := FitPair(im)
		require.True(t, ok, "frame %d: the two-body fit refused a frame it drew itself", i)
		require.True(t, g.Eclipsed(), "frame %d: no occulter found", i)
		f := Frame{Path: p, Index: i, Limb: g.Sun, Score: FrameSharpness(im, g.Sun)}
		if withMoon {
			f.Moon = g.Moon
		}
		frames = append(frames, f)
	}
	return frames
}

func TestStack_AMovingOcculterRendersAnEdgeNotARamp(t *testing.T) {
	const (
		n     = 16
		drift = 10.0 // px across the window: twice what a real 30 s window costs, so the effect is plain
	)
	ctx := context.Background()
	dir := t.TempDir()

	// The single frame is the target: an occulter's edge is opaque, so a stack of a static scene can
	// do no better and should do no worse.
	oneFrame := sweepingOcculter(t, dir, 2, 0, true)
	single, err := fits.ReadImage(oneFrame[0].Path)
	require.NoError(t, err)
	want := occulterEdgeWidth(t, firstPlane(single), oneFrame[0].Moon.CY)

	widths := map[string]float64{}
	for _, c := range []struct {
		name     string
		withMoon bool
	}{{"masked", true}, {"plain", false}} {
		frames := sweepingOcculter(t, dir, n, drift, c.withMoon)
		// NoRefine on BOTH, so the only difference between the two cases is the masking. Leaving the
		// correlation refinement on would make the unmasked case look almost fine, because it
		// silently registers on the Moon — see TestStack_TheRefinerRegistersOnTheMoon.
		res, err := Stack(ctx, frames, StackOptions{NoRefine: true})
		require.NoError(t, err)
		widths[c.name] = occulterEdgeWidth(t, res.Master, res.Limb.CY)
		if c.withMoon {
			assertSweptBandIsFilled(t, res)
		}
	}
	t.Logf("occulter edge 10–90%% width: single frame %.2f px | masked stack %.2f px | unmasked stack %.2f px (drift %.0f px)",
		want, widths["masked"], widths["plain"], drift)

	// The edge must land at the width the OPTICS give it — the single frame's — because that is what
	// the occulter-anchored stack images. Two failures are being excluded at once and they look
	// nothing alike: a much WIDER edge is the occulter's motion averaged in, and a much NARROWER one
	// is a coverage boundary rendered as though it were a limb, which is what this produced before the
	// second anchor existed (0.80 px against a single frame's 3.21).
	if got := widths["masked"]; math.Abs(got-want) > 1.5 {
		t.Errorf("the occulter's edge renders at %.2f px against a single frame's %.2f px", got, want)
	}
	// And the unmasked stack must fail, or this test is guarding nothing: the ramp is the defect.
	if widths["plain"] <= widths["masked"]+2 {
		t.Fatalf("fixture proves nothing: without masking the edge measured %.2f px, "+
			"barely worse than the masked %.2f px — the drift is not reaching the stack",
			widths["plain"], widths["masked"])
	}
}

// occulterEdgeWidth measures the 10–90% width of the occulter's leading edge along a horizontal cut.
//
// It reads the transition directly rather than fitting anything, because what is being told apart
// is a step from a ramp and every fitted model of an edge assumes one or the other.
func occulterEdgeWidth(t *testing.T, im *fits.Image, cy float64) float64 {
	t.Helper()
	y := clampInt(int(cy+0.5), 0, im.H-1)
	row := make([]float64, im.W)
	// Three rows averaged, to keep per-pixel noise from moving the 10% and 90% crossings.
	for x := 0; x < im.W; x++ {
		var sum float64
		for dy := -1; dy <= 1; dy++ {
			yy := clampInt(y+dy, 0, im.H-1)
			sum += float64(im.Pix[0][yy*im.W+x])
		}
		row[x] = sum / 3
	}
	// The occulter's leading edge is the steepest FALL along +x: crescent, then Moon.
	best, bestSlope := -1, 0.0
	for x := 2; x < im.W-2; x++ {
		if d := row[x+1] - row[x-1]; d < bestSlope {
			best, bestSlope = x, d
		}
	}
	require.Greater(t, best, 0, "no falling edge found along the cut")

	hi, lo := row[best], row[best]
	for x := best; x >= 0 && x > best-40; x-- {
		hi = math.Max(hi, row[x])
	}
	for x := best; x < im.W && x < best+40; x++ {
		lo = math.Min(lo, row[x])
	}
	if hi-lo < 1e-6 {
		t.Fatal("no contrast across the occulter's edge")
	}
	at := func(frac float64) float64 {
		want := lo + frac*(hi-lo)
		for x := best - 40; x < best+40 && x+1 < im.W; x++ {
			if x < 0 {
				continue
			}
			if row[x] >= want && row[x+1] < want {
				// Linear interpolation between the straddling samples.
				return float64(x) + (row[x]-want)/(row[x]-row[x+1])
			}
		}
		return math.NaN()
	}
	x90, x10 := at(0.9), at(0.1)
	require.False(t, math.IsNaN(x90) || math.IsNaN(x10), "edge crossings not found")
	return x10 - x90
}

// assertSweptBandIsFilled checks that the band the occulter swept carries real data.
//
// The sun-anchored stack cannot produce it — a pixel the limb crossed is Moon in some frames and Sun
// in others, so the whole sweep is dropped from coverage and comes out empty. Registered on the
// occulter instead, every frame contributes to every pixel of that band. Left unfilled it is the
// finish that invents it, from the disc's radial model: smooth, plausible, and not a measurement.
func assertSweptBandIsFilled(t *testing.T, res *StackResult) {
	t.Helper()
	if res.Moon.R <= 0 {
		t.Fatal("the stack reports no occulter, so there is no swept band to check")
	}
	// An annulus just outside the occulter's mid-window edge: entirely inside the sweep the sun
	// anchor dropped, and entirely Sun at the window's mid-point.
	lo, hi := res.Moon.R+1, res.Moon.R+5
	var empty, total int
	for y := 0; y < res.Master.H; y++ {
		dy := float64(y) - res.Moon.CY
		for x := 0; x < res.Master.W; x++ {
			dx := float64(x) - res.Moon.CX
			d := math.Hypot(dx, dy)
			if d < lo || d > hi {
				continue
			}
			total++
			if res.Master.Pix[0][y*res.Master.W+x] == 0 {
				empty++
			}
		}
	}
	if total == 0 {
		t.Fatal("no band around the occulter to check")
	}
	if frac := float64(empty) / float64(total); frac > 0.02 {
		t.Errorf("%.0f%% of the band just outside the occulter is empty; the second anchor did not fill it",
			100*frac)
	}
}

// TestStack_TheRefinerRegistersOnTheMoon is why an eclipse run turns the correlation refinement off.
//
// The refiner maximises whole-disc agreement between a frame and the reference. On an eclipsed Sun
// the occulter's edge is one of the strongest features in the frame, and it is the one feature that
// MOVES between frames — so the shift that best matches the two images is partly the Moon's motion,
// applied to the Sun. The result is a beautifully sharp lunar edge sitting on a Sun that has been
// smeared by a share of the Moon's travel, and every whole-image metric reports an improvement.
//
// Measured here as the transition width, which is the one number that can see it: with the refiner
// on, a ten-pixel sweep renders in under four pixels — the Moon has been registered, not the Sun.
func TestStack_TheRefinerRegistersOnTheMoon(t *testing.T) {
	const drift = 10.0
	dir := t.TempDir()
	ctx := context.Background()

	widths := map[bool]float64{}
	for _, noRefine := range []bool{false, true} {
		frames := sweepingOcculter(t, dir, 16, drift, false)
		res, err := Stack(ctx, frames, StackOptions{NoRefine: noRefine})
		require.NoError(t, err)
		widths[noRefine] = occulterEdgeWidth(t, res.Master, res.Limb.CY)
	}
	t.Logf("occulter sweep of %.0f px renders as %.2f px with the refiner on, %.2f px with it off",
		drift, widths[false], widths[true])

	// With the refiner off the sweep must show up at something like its true width.
	if widths[true] < drift*0.6 {
		t.Errorf("without the refiner a %.0f px sweep rendered in %.2f px; the fixture is not drifting",
			drift, widths[true])
	}
	// And with it on it must NOT, or the pull this guards against has gone away and the preset's
	// NoRefine can be reconsidered.
	if widths[false] > widths[true]*0.75 {
		t.Errorf("the refiner no longer collapses the sweep (%.2f px on, %.2f px off); "+
			"if that is a real fix, revisit Preset.StackOpts setting NoRefine for two-body runs",
			widths[false], widths[true])
	}
}

// TestStack_TheRecoveredBandIsRealSunNotAModel is the reason the second anchor is worth a whole
// extra pass over every frame.
//
// The band the occulter sweeps is a third of the visible Sun on a thin crescent, and it sits at the
// edge the eye goes to. Something has to go there. Without the second anchor the finish fills it
// from the disc's radial model — smooth, plausible, and carrying no filament, no plage and no
// granulation, because a radial median has none by construction. This measures both against the
// truth: the same Sun, drawn without an occulter over it.
func TestStack_TheRecoveredBandIsRealSunNotAModel(t *testing.T) {
	const n = 12
	ctx := context.Background()
	dir := t.TempDir()

	frames := sweepingOcculter(t, dir, n, 8.0, true)
	res, err := Stack(ctx, frames, StackOptions{NoRefine: true})
	require.NoError(t, err)
	require.Greater(t, res.Moon.R, 0.0, "the stack found no occulter")

	// The same Sun with nothing in front of it, stacked identically: ground truth on the same raster.
	truthFrames := unoccultedTwin(t, dir, n)
	truth, err := Stack(ctx, truthFrames, StackOptions{NoRefine: true})
	require.NoError(t, err)
	require.Equal(t, truth.Master.W, res.Master.W, "the two stacks landed on different rasters")

	band := func(get func(int) float64) float64 {
		var sum float64
		var count int
		for y := 0; y < res.Master.H; y++ {
			dy := float64(y) - res.Moon.CY
			for x := 0; x < res.Master.W; x++ {
				dx := float64(x) - res.Moon.CX
				if d := math.Hypot(dx, dy); d < res.Moon.R+1 || d > res.Moon.R+5 {
					continue
				}
				i := y*res.Master.W + x
				if truth.Master.Pix[0][i] == 0 {
					continue
				}
				sum += get(i)
				count++
			}
		}
		require.Greater(t, count, 500, "too little band to measure")
		return sum / float64(count)
	}
	rmsAgainstTruth := func(p []float32) float64 {
		return math.Sqrt(band(func(i int) float64 {
			d := float64(p[i]) - float64(truth.Master.Pix[0][i])
			return d * d
		}))
	}
	recovered := rmsAgainstTruth(res.Master.Pix[0])
	// The Sun's own contrast across the same band, as the scale the error is judged against. An
	// absolute RMS means nothing on its own; what matters is whether the recovered band resembles the
	// Sun more than it resembles a flat guess.
	truthRMS := math.Sqrt(band(func(i int) float64 {
		d := float64(truth.Master.Pix[0][i]) - truthBandMean(truth, res.Moon)
		return d * d
	}))
	t.Logf("band vs the true Sun: recovered RMS %.5f against the Sun's own spread of %.5f there (%.0f%%)",
		recovered, truthRMS, 100*recovered/truthRMS)

	// Half the scene's own variation is a generous bar and a meaningful one: a flat fill would score
	// 100%, and anything near that is not a measurement of the Sun.
	if recovered > 0.5*truthRMS {
		t.Errorf("the recovered band is %.0f%% of the Sun's own spread away from it; that is not Sun",
			100*recovered/truthRMS)
	}
}

// truthBandMean is the mean of the true Sun over the band, for scaling the error against the scene.
func truthBandMean(truth *StackResult, moon Limb) float64 {
	var sum float64
	var n int
	for y := 0; y < truth.Master.H; y++ {
		dy := float64(y) - moon.CY
		for x := 0; x < truth.Master.W; x++ {
			dx := float64(x) - moon.CX
			if d := math.Hypot(dx, dy); d < moon.R+1 || d > moon.R+5 {
				continue
			}
			i := y*truth.Master.W + x
			if truth.Master.Pix[0][i] == 0 {
				continue
			}
			sum += float64(truth.Master.Pix[0][i])
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// unoccultedTwin draws the same Sun the sweep fixture does, with no occulter over it.
func unoccultedTwin(t *testing.T, dir string, n int) []Frame {
	t.Helper()
	base := defaultSun()
	base.w, base.h = 1000, 1000
	base.cx, base.cy, base.r = 500, 500, 330
	base.features = 18
	base.ringAmp = 0
	base.psfSigma = 1.2
	frames := make([]Frame, 0, n)
	for i := 0; i < n; i++ {
		s := base
		s.noiseSeed = uint64(2000 + i) // the same noise the sweep fixture used, frame for frame
		im := drawSun(s)
		p := filepath.Join(dir, fmt.Sprintf("truth_%03d.fits", i))
		require.NoError(t, im.WriteFITS(p))
		l, ok := FitLimb(im)
		require.True(t, ok)
		frames = append(frames, Frame{Path: p, Index: i, Limb: l, Score: FrameSharpness(im, l)})
	}
	return frames
}

// TestStack_TheMaskedRefinerLeavesTheSunAlone is the fix for what the test above measures.
//
// The refiner maximises agreement between a frame and the reference, and on an eclipsed Sun the
// occulter is both the strongest feature and the only one that moves. Told which pixels are the
// occulter's, it stops voting on them — and the shift it returns is then the Sun's alone.
//
// Measured on the SUN, not on the occulter's edge, because that is what the failure damages: a
// refiner following the Moon produces a sharp lunar edge on a smeared Sun, so any metric read off
// the edge reports an improvement. The solar limb is the witness.
func TestStack_TheMaskedRefinerLeavesTheSunAlone(t *testing.T) {
	const drift = 10.0
	ctx := context.Background()
	dir := t.TempDir()

	sunPSF := func(withMoon, refine bool) float64 {
		frames := sweepingOcculter(t, dir, 16, drift, withMoon)
		res, err := Stack(ctx, frames, StackOptions{NoRefine: !refine})
		require.NoError(t, err)
		p := MeasurePSF(res.Master, res.Limb)
		require.True(t, p.OK, "the solar limb could not be measured")
		return p.SigmaPx
	}

	// The Sun is identical in every frame, so anything above the no-refinement baseline is damage the
	// refinement did.
	baseline := sunPSF(true, false)
	blind := sunPSF(false, true)  // refiner on, told nothing about the occulter
	aware := sunPSF(true, true)   // refiner on, told where the occulter is
	t.Logf("solar limb sigma: %.2f no refinement | %.2f refiner blind to the occulter | %.2f refiner told about it",
		baseline, blind, aware)

	if aware > baseline*1.15 {
		t.Errorf("the occulter-aware refiner still smeared the Sun: %.2f against a %.2f baseline",
			aware, baseline)
	}
	// And the blind one must be worse, or the mask is guarding nothing.
	if blind <= aware*1.05 {
		t.Fatalf("fixture proves nothing: the blind refiner scored %.2f against the aware one's %.2f",
			blind, aware)
	}
}
