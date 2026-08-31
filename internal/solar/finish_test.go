package solar

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// effectiveSigma measures the width a blur actually applies, by blurring a delta and taking the
// second moment of the result.
func effectiveSigma(blur func([]float32) []float32, w, h int) float64 {
	p := make([]float32, w*h)
	p[(h/2)*w+w/2] = 1
	out := blur(p)
	var sum, m2 float64
	cy := float64(h / 2)
	for y := 0; y < h; y++ {
		dy := float64(y) - cy
		for x := 0; x < w; x++ {
			v := float64(out[y*w+x])
			sum += v
			m2 += v * dy * dy
		}
	}
	if sum <= 0 {
		return 0
	}
	return math.Sqrt(m2 / sum)
}

// TestGaussianKernel_Accuracy pins that the FIR kernel applies the width it was asked for, and — in
// the same table — documents why imgops.GaussianBlur could not be used for deconvolution. Its
// three-box approximation quantises sigma hard, and the RL PSF width lands squarely in that range.
func TestGaussianKernel_Accuracy(t *testing.T) {
	const w, h = 129, 129
	// A Gaussian sampled on an integer grid has a slightly smaller second moment than the continuous
	// one it came from, and the gap grows as the kernel narrows — at sigma 0.5 the discrete width is
	// genuinely ~0.464. That is inherent to sampling, it varies smoothly, and it is an order of
	// magnitude better than the box approximation below, which is flat-out wrong at the same widths.
	for _, tt := range []struct {
		sigma, tol float64
	}{{0.5, 0.09}, {1.0, 0.03}, {1.3, 0.03}, {2.0, 0.02}, {3.0, 0.02}} {
		t.Run(fmt.Sprintf("fir sigma=%.1f", tt.sigma), func(t *testing.T) {
			k := gaussianKernel(tt.sigma)
			got := effectiveSigma(func(p []float32) []float32 { return blurFIR(p, w, h, k) }, w, h)
			assert.InDelta(t, tt.sigma, got, tt.tol*tt.sigma+0.01)
		})
	}

	t.Run("the box approximation is why this exists", func(t *testing.T) {
		half := effectiveSigma(func(p []float32) []float32 { return imgops.GaussianBlur(p, w, h, 0.5) }, w, h)
		assert.InDelta(t, 0.0, half, 1e-9, "imgops.GaussianBlur at sigma 0.5 is an exact no-op")

		one := effectiveSigma(func(p []float32) []float32 { return imgops.GaussianBlur(p, w, h, 1.0) }, w, h)
		assert.Less(t, one, 0.9, "imgops.GaussianBlur at sigma 1.0 under-blurs by nearly 20%%")
	})

	t.Run("is symmetric, so it is its own adjoint", func(t *testing.T) {
		k := gaussianKernel(1.7)
		for i := range k {
			assert.InDelta(t, float64(k[i]), float64(k[len(k)-1-i]), 1e-9)
		}
		var sum float32
		for _, v := range k {
			sum += v
		}
		assert.InDelta(t, 1.0, float64(sum), 1e-6, "the kernel must conserve flux")
	})
}

// TestFlattenLimbDarkening covers the step that makes detail readable to the edge.
func TestFlattenLimbDarkening(t *testing.T) {
	s := defaultSun()
	s.proms, s.noise = 3, 0
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)

	before := radialSpread(im.Pix[0], im.W, im.H, l)
	p := append([]float32(nil), im.Pix[0]...)
	FlattenLimbDarkening(p, im.W, im.H, l, 1.0, nil)
	after := radialSpread(p, im.W, im.H, l)

	t.Run("flattens the radial profile", func(t *testing.T) {
		t.Logf("radial spread %.4f -> %.4f", before, after)
		assert.Less(t, after, before/5, "the disc must come out substantially flatter")
	})

	t.Run("leaves no bright ring at the limb", func(t *testing.T) {
		// The classic failure: an unbounded correction divides the noisy outermost annulus by an
		// ever-smaller number and paints a rim exactly where the eye goes first.
		inner := annulusMedian(p, im.W, im.H, l, 0.3, 0.6)
		rim := annulusMax(p, im.W, im.H, l, 0.96, 1.0)
		assert.Less(t, rim, 1.35*inner, "limb annulus must not overshoot the disc")
	})

	t.Run("strength 0 is a no-op", func(t *testing.T) {
		q := append([]float32(nil), im.Pix[0]...)
		FlattenLimbDarkening(q, im.W, im.H, l, 0, nil)
		assert.Equal(t, im.Pix[0], q)
	})
}

// TestInstrumentField covers ring and gradient removal.
func TestInstrumentField(t *testing.T) {
	clean := defaultSun()
	clean.noise = 0
	dirty := clean
	dirty.ringAmp, dirty.gradAmp = 0.12, 0.30

	imClean, imDirty := drawSun(clean), drawSun(dirty)
	l, ok := FitLimb(imDirty)
	require.True(t, ok)

	p := append([]float32(nil), imDirty.Pix[0]...)
	Deflat(p, imDirty.W, imDirty.H, l, 1.0)

	t.Run("attenuates the injected rings and gradient", func(t *testing.T) {
		before := lowFrequencySpread(imDirty.Pix[0], imDirty.W, imDirty.H, l)
		after := lowFrequencySpread(p, imDirty.W, imDirty.H, l)
		t.Logf("low-frequency spread %.4f -> %.4f (clean reference %.4f)",
			before, after, lowFrequencySpread(imClean.Pix[0], imClean.W, imClean.H, l))
		// A single blur scale cannot follow rings whose spacing changes across the disc — Newton's
		// rings are periodic in r², so they crowd together towards the edge. It removes the gradient
		// and the wider rings; the tight outer ones need a frequency-domain notch, which is the
		// known next step. What is pinned here is that it is a large, real reduction.
		assert.Less(t, after, before/1.7, "the instrument field must be substantially removed")
	})

	t.Run("does not eat small-scale detail", func(t *testing.T) {
		// A flat that removes high frequencies is removing the Sun, not the instrument.
		d0 := discStats(imDirty, l.CX, l.CY, l.R).detail
		d1 := discStats(&fits.Image{W: imDirty.W, H: imDirty.H, C: 1, Pix: [][]float32{p}}, l.CX, l.CY, l.R).detail
		assert.Greater(t, d1, 0.9*d0, "detail must survive the flat")
	})
}

// TestRichardsonLucy covers the deconvolution's three obligations: it must sharpen, it must not ring
// at the limb, and it must conserve flux.
func TestRichardsonLucy(t *testing.T) {
	sharp := defaultSun()
	sharp.psfSigma, sharp.noise, sharp.proms = 0.6, 0, 0
	blurred := sharp
	blurred.psfSigma = 2.2

	truth, obs := drawSun(sharp), drawSun(blurred)
	l, ok := FitLimb(obs)
	require.True(t, ok)
	out := RichardsonLucy(obs.Pix[0], obs.W, obs.H, l, 1.8, 12, 1e-4)

	t.Run("moves the image towards the truth", func(t *testing.T) {
		before := rmsDiff(obs.Pix[0], truth.Pix[0], obs.W, obs.H, l, 0.85)
		after := rmsDiff(out, truth.Pix[0], obs.W, obs.H, l, 0.85)
		t.Logf("on-disc RMS vs truth: %.5f -> %.5f", before, after)
		assert.Less(t, after, before, "deconvolution must reduce the error, not just add contrast")
	})

	t.Run("does not ring at the limb", func(t *testing.T) {
		// Undamped RL against a 100:1 step grows a dark rim inside and a bright halo outside.
		peak := annulusMedian(out, obs.W, obs.H, l, 0.2, 0.5)
		over := annulusMax(out, obs.W, obs.H, l, 0.97, 1.02)
		assert.Less(t, over, 1.25*peak, "limb overshoot must stay bounded")
	})

	t.Run("conserves flux over the disc", func(t *testing.T) {
		assert.InDelta(t, discSum(obs.Pix[0], obs.W, obs.H, l), discSum(out, obs.W, obs.H, l),
			0.03*discSum(obs.Pix[0], obs.W, obs.H, l))
	})

	t.Run("is a no-op on a flat field", func(t *testing.T) {
		flat := make([]float32, 64*64)
		for i := range flat {
			flat[i] = 0.5
		}
		got := RichardsonLucy(flat, 64, 64, Limb{CX: 32, CY: 32, R: 20}, 1.5, 10, 1e-4)
		for _, v := range got {
			require.InDelta(t, 0.5, float64(v), 1e-3)
		}
	})

	t.Run("zero iterations returns the input untouched", func(t *testing.T) {
		got := RichardsonLucy(obs.Pix[0], obs.W, obs.H, l, 1.8, 0, 1e-4)
		assert.Equal(t, obs.Pix[0], got)
	})
}

// TestStarletSharpen pins the property that keeps sharpening honest: gain and threshold together
// must never make noise worse.
func TestStarletSharpen(t *testing.T) {
	const w, h = 256, 256
	rng := newLCG(11)
	p := make([]float32, w*h)
	for i := range p {
		p[i] = float32(0.5 + 0.02*(rng.float()-0.5))
	}
	o := DefaultSharpen(1.4)
	out := StarletSharpen(p, w, h, o, 0.02/math.Sqrt(12), nil)
	assert.LessOrEqual(t, stdOf(out), stdOf(p),
		"on pure noise the gain-and-threshold pair must not net-amplify")

	t.Run("no gains is a no-op", func(t *testing.T) {
		got := StarletSharpen(p, w, h, SharpenOptions{}, 0.01, nil)
		assert.Equal(t, p, got)
	})
}

// TestPalette covers the colour rendering.
func TestPalette(t *testing.T) {
	im := fits.NewImage(4, 1, 1)
	im.Pix[0] = []float32{0, 0.33, 0.66, 1}

	t.Run("gold is warm and monotone in luminance", func(t *testing.T) {
		out := applyPalette(im.Pix[0], 4, 1, FinishOptions{Palette: PaletteGold, Saturation: 1})
		require.Equal(t, 3, out.C)
		for i := 0; i < 4; i++ {
			assert.GreaterOrEqual(t, out.Pix[0][i], out.Pix[2][i], "red must lead blue in a gold ramp")
		}
		for i := 1; i < 4; i++ {
			assert.Greater(t, out.Pix[0][i], out.Pix[0][i-1], "the ramp must rise")
		}
	})

	t.Run("mono and inverted are greyscale", func(t *testing.T) {
		for _, name := range []string{PaletteMono, PaletteInverted} {
			out := applyPalette(im.Pix[0], 4, 1, FinishOptions{Palette: name})
			for i := 0; i < 4; i++ {
				assert.Equal(t, out.Pix[0][i], out.Pix[1][i], name)
				assert.Equal(t, out.Pix[1][i], out.Pix[2][i], name)
			}
		}
	})

	t.Run("inverted really inverts", func(t *testing.T) {
		out := applyPalette(im.Pix[0], 4, 1, FinishOptions{Palette: PaletteInverted})
		assert.InDelta(t, 1.0, float64(out.Pix[0][0]), 1e-6)
		assert.InDelta(t, 0.0, float64(out.Pix[0][3]), 1e-6)
	})

	t.Run("an unknown palette falls back rather than failing", func(t *testing.T) {
		assert.NotPanics(t, func() { applyPalette(im.Pix[0], 4, 1, FinishOptions{Palette: "nonsense"}) })
	})
}

// TestFinish_EndToEnd runs the whole finish over a synthetic master.
func TestFinish_EndToEnd(t *testing.T) {
	s := defaultSun()
	s.ringAmp, s.gradAmp, s.proms, s.psfSigma = 0.08, 0.25, 3, 2.0
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)

	out := Finish(im, l, DefaultFinish())
	require.Equal(t, 3, out.C)
	require.Equal(t, im.W, out.W)

	for c := range out.Pix {
		for i, v := range out.Pix[c] {
			require.False(t, math.IsNaN(float64(v)), "channel %d pixel %d is NaN", c, i)
			require.GreaterOrEqual(t, v, float32(0))
			require.LessOrEqual(t, v, float32(1))
		}
	}
	t.Run("the disc is brighter than the sky", func(t *testing.T) {
		disc := annulusMedian(out.Pix[0], out.W, out.H, l, 0.2, 0.5)
		sky := annulusMedian(out.Pix[0], out.W, out.H, l, 1.3, 1.4)
		assert.Greater(t, disc, 2*sky)
	})
}

// --- helpers -------------------------------------------------------------------------------

func radialSpread(p []float32, w, h int, l Limb) float64 {
	prof := MeasureRadialProfile(p, w, h, l)
	lo, hi := math.Inf(1), math.Inf(-1)
	for i := 5; i < radialBins-20; i++ { // skip the centre bins and the limb transition
		lo, hi = math.Min(lo, prof.Bins[i]), math.Max(hi, prof.Bins[i])
	}
	if prof.Peak <= 0 {
		return 0
	}
	return (hi - lo) / prof.Peak
}

func lowFrequencySpread(p []float32, w, h int, l Limb) float64 {
	prof := MeasureRadialProfile(p, w, h, l)
	resid := make([]float32, len(p))
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			r := math.Hypot(dx, dy)
			if r > 0.9*l.R {
				continue
			}
			model := prof.Peak / math.Max(prof.rawGain(r/l.R), 1e-6)
			resid[y*w+x] = float32(float64(p[y*w+x]) / math.Max(model, 1e-9))
		}
	}
	blur := imgops.GaussianBlur(resid, w, h, 0.05*l.R)
	var vals []float32
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			if math.Hypot(dx, dy) < 0.75*l.R {
				vals = append(vals, blur[y*w+x])
			}
		}
	}
	return imgops.Percentile(vals, 95) - imgops.Percentile(vals, 5)
}

func annulusMedian(p []float32, w, h int, l Limb, lo, hi float64) float64 {
	return annulusStat(p, w, h, l, lo, hi, 50)
}

func annulusMax(p []float32, w, h int, l Limb, lo, hi float64) float64 {
	return annulusStat(p, w, h, l, lo, hi, 99.5)
}

func annulusStat(p []float32, w, h int, l Limb, lo, hi, pct float64) float64 {
	var vals []float32
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			if d := math.Hypot(dx, dy) / l.R; d >= lo && d <= hi {
				vals = append(vals, p[y*w+x])
			}
		}
	}
	if len(vals) == 0 {
		return 0
	}
	return imgops.Percentile(vals, pct)
}

func discSum(p []float32, w, h int, l Limb) float64 {
	var s float64
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			if math.Hypot(dx, dy) <= 0.9*l.R {
				s += float64(p[y*w+x])
			}
		}
	}
	return s
}

func rmsDiff(a, b []float32, w, h int, l Limb, frac float64) float64 {
	var s float64
	var n int
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			if math.Hypot(dx, dy) > frac*l.R {
				continue
			}
			d := float64(a[y*w+x] - b[y*w+x])
			s += d * d
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(s / float64(n))
}

func stdOf(p []float32) float64 {
	var mean float64
	for _, v := range p {
		mean += float64(v)
	}
	mean /= float64(len(p))
	var ss float64
	for _, v := range p {
		d := float64(v) - mean
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(p)))
}

// TestShoulderCurve_NeverClips pins the highlight roll-off. The disc window is intentionally narrow,
// so plage routinely lands above it; the curve must compress those values rather than flatten them
// into one white blob, and it must join the linear segment without a visible kink.
func TestShoulderCurve_NeverClips(t *testing.T) {
	// Below the threshold it is the identity, so nothing about the midtones changes.
	for _, y := range []float64{0, 0.25, 0.5, shoulderThreshold} {
		require.InDelta(t, y, shoulderCurve(y), 1e-12)
	}
	// Above it, values stay strictly ordered and strictly below 1 across the range plage actually
	// occupies: two different plage brightnesses must never render as the same pixel. Far enough out
	// tanh saturates in float64 — around 3.7 here, which is nearly five times the disc median and not
	// a brightness the Sun produces — so the guarantee is asserted where it has to hold.
	prev := shoulderCurve(shoulderThreshold)
	for _, y := range []float64{0.9, 1.0, 1.2, 1.6, 2.0, 2.5, 3.0} {
		got := shoulderCurve(y)
		require.Greater(t, got, prev, "shoulder must stay monotone at y=%v", y)
		require.Less(t, got, 1.0, "shoulder must never reach the clip at y=%v", y)
		prev = got
	}
	require.LessOrEqual(t, shoulderCurve(50), 1.0, "the shoulder must never overshoot white")
	// And it joins smoothly: slope matches across the threshold, so no edge appears where the
	// shoulder starts.
	const e = 1e-6
	below := (shoulderCurve(shoulderThreshold) - shoulderCurve(shoulderThreshold-e)) / e
	above := (shoulderCurve(shoulderThreshold+e) - shoulderCurve(shoulderThreshold)) / e
	require.InDelta(t, below, above, 1e-4)
}

// TestGain_ContinuousAcrossFreeze pins the limb-darkening correction against a step at the freeze
// radius. The correction is multiplicative, so a discontinuity there is a hard-edged ring just
// inside the limb — and because it looks like deconvolution ringing, it sends you off tuning the
// deconvolution instead of the thing that caused it.
func TestGain_ContinuousAcrossFreeze(t *testing.T) {
	s := defaultSun()
	s.features = 12
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)
	prof := MeasureRadialProfile(im.Pix[0], im.W, im.H, l)
	require.Greater(t, prof.Peak, 0.0)

	for _, strength := range []float64{0.4, 0.85, 1.0} {
		// Step finely across the whole disc and demand no jump between neighbouring samples bigger
		// than a smooth profile could produce.
		var prev, maxJump, at float64
		for f := 0.50; f <= 1.05; f += 0.001 {
			g := prof.Gain(f*l.R, strength)
			if prev > 0 {
				if j := math.Abs(g - prev); j > maxJump {
					maxJump, at = j, f
				}
			}
			prev = g
		}
		require.Less(t, maxJump, 0.01,
			"strength %.2f: gain steps by %.4f at r/R %.3f — that is a ring at the limb", strength, maxJump, at)
	}
	// And the freeze still does its job: past the fit limit the gain is constant, so off-limb noise
	// is never amplified by an unbounded correction.
	a := prof.Gain(0.99*l.R, 0.85)
	b := prof.Gain(1.40*l.R, 0.85)
	require.InDelta(t, a, b, 1e-9, "the correction must stay frozen beyond the fit limit")
}

// TestHalo_PreservesProminences is the safety net on the off-limb background model. The background
// is SUBTRACTED, so whatever it can describe is deleted; prominences are exactly what must not be.
//
// The two halves are measured on SEPARATE fixtures, and that separation is the point. Checking sky
// flatness on a disc that has prominences means the flatness probe finds a prominence and reports it
// as un-flat sky — the test then contradicts its own second half, and tightening it would push you
// to make the model eat prominences to pass.
func TestHalo_PreservesProminences(t *testing.T) {
	// An off-centre background of the kind an eyepiece vignette makes: strongly two-dimensional, so a
	// radial median about the DISC centre cannot describe it at all.
	skew := func(im *fits.Image, s sunSpec) {
		for y := 0; y < im.H; y++ {
			for x := 0; x < im.W; x++ {
				d := math.Hypot(float64(x)-s.cx*0.2, float64(y)-s.cy*0.2) / (2 * s.r)
				im.Pix[0][y*im.W+x] += float32(0.02 * math.Exp(-d*d))
			}
		}
	}

	t.Run("flattens an asymmetric sky", func(t *testing.T) {
		s := defaultSun()
		s.proms, s.features, s.noise = 0, 10, 0.001
		im := drawSun(s)
		skew(im, s)
		l, ok := FitLimb(im)
		require.True(t, ok)
		halo := offLimbProfile(im.Pix[0], im.W, im.H, l, nil)

		// Probe finer than the model is sampled, so it is checked between its own samples too.
		const probes = 16
		var worst, worstAt float64
		for _, frac := range []float64{1.05, 1.10, 1.20, 1.35} {
			for sect := 0; sect < probes; sect++ {
				a0 := float64(sect) / probes * 360
				v := sectorSamples(im.Pix[0], im.W, im.H, l, frac-0.02, frac+0.02, a0, a0+360.0/probes)
				if len(v) < 256 {
					continue
				}
				ang := (a0 + 180.0/probes) * math.Pi / 180
				resid := float64(imgops.Percentile(imgops.Subsample(v, 50000), 50)) - halo.at(frac, ang)
				if math.Abs(resid) > worst {
					worst, worstAt = math.Abs(resid), frac
				}
			}
		}
		t.Logf("worst sky residual %.5f at r/R %.2f (injected background peaks at 0.0200)", worst, worstAt)
		// Well under the level at which the prominence stretch renders sky as visible grey.
		assert.Less(t, worst, 0.0015, "the corrected sky must be flat in every direction")
	})

	t.Run("leaves prominences standing", func(t *testing.T) {
		s := defaultSun()
		s.proms, s.features, s.noise = 5, 10, 0.001
		im := drawSun(s)
		skew(im, s)
		l, ok := FitLimb(im)
		require.True(t, ok)
		halo := offLimbProfile(im.Pix[0], im.W, im.H, l, nil)

		var peak float64
		for y := 0; y < im.H; y++ {
			dy := float64(y) - l.CY
			for x := 0; x < im.W; x++ {
				dx := float64(x) - l.CX
				d := math.Hypot(dx, dy) / l.R
				if d < 1.01 || d > 1.15 {
					continue
				}
				if v := float64(im.Pix[0][y*im.W+x]) - halo.at(d, math.Atan2(dy, dx)); v > peak {
					peak = v
				}
			}
		}
		t.Logf("brightest prominence stands %.5f above the modelled background (drawn at 0.0250)", peak)
		assert.Greater(t, peak, 0.015, "prominences must survive the background subtraction")
	})
}

// TestFinish_TheLimbOnlyEverFalls is the acceptance test for the false bright ring.
//
// The Sun has no ring around it. Across the limb the brightness falls — disc, transition, sky — and
// it never comes back up. That is true of the raw video frames and it must be true of what we
// render from them, so any rise on the way out is something the finish invented.
//
// It has been invented four different ways in this package, all of them landing at the same radius
// and therefore indistinguishable by eye: the prominence curve rendering disc pixels it was never
// meant to reach, the limb-darkening gain read at the wrong radius, an unsharp overshoot on the limb
// step, and the halo model's end bins. Measuring the finished profile rather than any one stage is
// what makes them all fail the same test instead of hiding behind each other.
//
// The fixture is deliberately clean — a limb-darkened disc, real features, prominences, a modest PSF
// and a little noise — so a ring here cannot be blamed on the data.
func TestFinish_TheLimbOnlyEverFalls(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r = 1800, 1800, 903.4, 897.7, 780
	s.proms, s.features = 4, 24
	s.ringAmp, s.gradAmp = 0, 0
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)

	for _, c := range []struct {
		name string
		mut  func(*FinishOptions)
	}{
		{"the shipping recipe", func(*FinishOptions) {}},
		{"without deconvolution", func(o *FinishOptions) { o.DeconvSigma, o.DeconvIters = 0, 0 }},
		{"without the prominence composite", func(o *FinishOptions) { o.ProminenceBoost = 0 }},
		{"without the limb-darkening flatten", func(o *FinishOptions) { o.LimbFlatten = 0 }},
		{"without the starlet pass", func(o *FinishOptions) { o.Sharpen.Gains = nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := DefaultFinish()
			o.DeconvAuto = false
			c.mut(&o)
			prof := renderedRadial(Finish(im, l, o), l)
			amp, at := ringAmplitude(prof)
			t.Logf("largest rise across the limb: %+.4f at %.3fR", amp, at)
			// The floor is the median's own noise over a bin, which at this radius holds thousands of
			// pixels — a real ring is an order of magnitude above it.
			assert.Less(t, amp, 0.01, "the rendered limb brightens by %.4f at %.3fR: that ring is not in the data", amp, at)
		})
	}
}

// TestRadialProfile_GainIsReadAtTheRadiusItWasMeasuredAt pins the profile's round trip.
//
// MeasureRadialProfile bins radius by radialBins/(ldFitLimit·R); every lookup has to invert exactly
// that, and for a long time one of them multiplied by ldFitLimit where it should have divided. The
// error is only three percent of the radius, so the disc still flattened and nothing looked broken —
// the correction was simply applied to the wrong annulus, worst where the profile is steepest, which
// is the last few percent before the limb.
//
// The fixture makes the mistake impossible to miss: a narrow dark annulus at a known radius. The
// correction that undoes it must peak THERE, not three percent inside.
func TestRadialProfile_GainIsReadAtTheRadiusItWasMeasuredAt(t *testing.T) {
	const at = 0.90
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r = 1200, 1200, 601.5, 598.5, 500
	s.u1, s.u2, s.noise, s.psfSigma, s.ringAmp, s.gradAmp = 0, 0, 0, 0, 0, 0
	im := drawSun(s)
	// A dark ring one percent of the radius wide, riding on an otherwise uniform disc.
	for y := 0; y < s.h; y++ {
		dy := float64(y) - s.cy
		for x := 0; x < s.w; x++ {
			dx := float64(x) - s.cx
			f := math.Hypot(dx, dy) / s.r
			im.Pix[0][y*s.w+x] *= float32(1 - 0.35*math.Exp(-(f-at)*(f-at)/(2*0.005*0.005)))
		}
	}
	l, ok := FitLimb(im)
	require.True(t, ok)
	prof := MeasureRadialProfile(im.Pix[0], im.W, im.H, l)
	require.Greater(t, prof.Peak, 0.0)

	best, bestAt := 0.0, 0.0
	for f := 0.5; f < ldFreezeStart; f += 0.001 {
		if g := prof.rawGain(f); g > best {
			best, bestAt = g, f
		}
	}
	t.Logf("the correction peaks at %.3fR (x%.2f); the ring was drawn at %.3fR", bestAt, best, at)
	assert.InDelta(t, at, bestAt, 0.005,
		"the gain that undoes a feature must be applied where the feature is, not %.1f%% away",
		100*math.Abs(bestAt-at)/at)
}

// TestFinish_TheGlowIsBoundedByTheLimbItRisesFrom is the other half of the ring contract.
//
// TestFinish_TheLimbOnlyEverFalls says the finish must not invent brightness at the limb; this says
// the deliberate halo does not become a way of inventing it anyway. The halo's own construction is
// what makes that true rather than a matter of tuning — it is anchored to what the limb itself
// renders at and composited by taking the brighter of the two — so this pins the two properties that
// argument rests on: the halo never out-shines the disc, and it only ever decays outward.
func TestFinish_TheGlowIsBoundedByTheLimbItRisesFrom(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r = 1600, 1600, 803.4, 797.7, 700
	s.proms, s.features = 0, 24 // no prominences: the halo must stand on its own here
	s.ringAmp, s.gradAmp = 0, 0
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)

	o := DefaultFinish()
	o.DeconvAuto = false
	require.Positive(t, o.GlowStrength, "the fixture is meaningless with the glow off by default")

	off := o
	off.GlowStrength = 0
	dark := renderedRadial(Finish(im, l, off), l)
	lit := renderedRadial(Finish(im, l, o), l)

	var brightest, atFrac float64
	inner := dark[0] // 0.90 R, well inside the limb
	for i := range lit {
		f := radiusOfBin(i)
		if f < 1.0 || math.IsNaN(lit[i]) {
			continue
		}
		if lit[i] > brightest {
			brightest, atFrac = lit[i], f
		}
	}
	t.Logf("disc renders at %.4f; the halo peaks at %.4f (%.0f%% of it) at %.3fR",
		inner, brightest, 100*brightest/inner, atFrac)
	sky := dark[len(dark)-1]
	t.Logf("sky without the halo renders at %.4f", sky)
	assert.Greater(t, brightest, 1.3*sky,
		"the halo must actually be there — it should stand clearly above the sky the same render has without it")
	assert.Less(t, brightest, inner, "the halo must never out-shine the disc it surrounds")

	// And outside the limb it only ever decays: a halo with a bump in it reads as a ring, which is the
	// whole defect this was added alongside a fix for.
	prev := math.Inf(1)
	for i := range lit {
		if radiusOfBin(i) < 1.005 || math.IsNaN(lit[i]) {
			continue
		}
		require.LessOrEqual(t, lit[i], prev+1e-4,
			"the halo brightens again at %.3fR", radiusOfBin(i))
		prev = lit[i]
	}
}
