package solar

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// pairfinish_test.go measures what the finish does with the hole the stack leaves behind.
//
// The master reaching the finish is not a dark disc where the Moon was — it is EMPTY there, because
// the stack excludes the occulter's whole sweep from coverage. Every averaging step in the finish
// then folds those zeros in, and the resulting defects all appear somewhere other than their cause:
// a limb-darkening gain measured on half-empty annuli over-brightens the crescent, a tone curve
// anchored on percentiles that include the hole renders everything too bright, and Richardson-Lucy
// rings on a step that is larger than the solar limb's and sits inside the disc.

// eclipsedMaster builds what the stack hands the finish: an eclipsed frame with the occulted region
// emptied, exactly as excluding it from coverage leaves it.
func eclipsedMaster(t *testing.T, sep float64) (*fits.Image, Pair) {
	t.Helper()
	s := eclipsedSun(sep, 1.02)
	s.features = 40
	im := drawSun(s)
	g, ok := FitPair(im)
	if !ok {
		t.Fatal("the two-body fit refused a frame it drew itself")
	}
	// Asserted, not assumed. Without this the helper silently returns a pair with no occulter, the
	// zeroing below does nothing, and every test in this file passes while measuring an ordinary
	// full-disc render — which is exactly what happened before the opacity gate was added.
	if !g.Eclipsed() {
		t.Fatalf("no occulter found in a frame drawn with one at (%.1f,%.1f) r=%.1f",
			s.moonCX, s.moonCY, s.moonR)
	}
	guard := math.Max(pairMaskGuardPx, pairMaskGuardFrac*g.Sun.R)
	r2 := (g.Moon.R + guard) * (g.Moon.R + guard)
	for y := 0; y < im.H; y++ {
		dy := float64(y) - g.Moon.CY
		for x := 0; x < im.W; x++ {
			dx := float64(x) - g.Moon.CX
			if dx*dx+dy*dy <= r2 {
				im.Pix[0][y*im.W+x] = 0
			}
		}
	}
	return im, g
}

// TestFinishPair_InventsNothingInsideTheOcculter is the lunar-limb counterpart of the ring invariant.
//
// Across the SOLAR limb the finished image must only ever fall, because a real limb only falls.
// Across the LUNAR limb it must RISE — dark body, then Sun — so that invariant does not transfer.
// What does transfer is the reason behind it: nothing may appear that the sky did not put there. The
// occulter is opaque, so its interior must render flat, and any structure there was invented by the
// deconvolution ringing on the hole's edge or by the prominence stretch reaching inside it.
func TestFinishPair_InventsNothingInsideTheOcculter(t *testing.T) {
	master, g := eclipsedMaster(t, 0.5)
	o := DefaultFinish()
	fin, _, _ := ResolveFinish(master, g.Sun, o)
	img := FinishPair(master, g, fin)

	// Profile the interior of the occulter, well clear of its own edge.
	prof := renderedRadial(img, g.Moon)
	var lo, hi float64 = math.Inf(1), math.Inf(-1)
	for i, v := range prof {
		if math.IsNaN(v) {
			continue
		}
		if f := radiusOfBin(i); f < renderLo || f > 0.97 {
			continue
		}
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if math.IsInf(lo, 1) {
		t.Fatal("no bins inside the occulter to profile")
	}
	t.Logf("occulter interior renders %.4f..%.4f", lo, hi)
	// A rise of a few thousandths is the palette's own quantisation; a ring is far larger.
	if hi-lo > 0.02 {
		t.Errorf("the occulter's interior varies by %.4f across 0.90..0.97 of its radius; "+
			"it is opaque, so that structure was invented", hi-lo)
	}
}

// TestFinishPair_TheOcculterRendersAsSkyNotAsAHole checks the level it lands at.
//
// Painting it to absolute zero would be wrong in a way that shows: background_level deliberately
// lifts the sky off black, so a Moon at zero reads as a hole punched through the picture rather than
// as a body silhouetted against that sky.
func TestFinishPair_TheOcculterRendersAsSkyNotAsAHole(t *testing.T) {
	master, g := eclipsedMaster(t, 1.0)
	o := DefaultFinish()
	fin, _, _ := ResolveFinish(master, g.Sun, o)
	img := FinishPair(master, g, fin)

	// Sampled by MASK, not by radius. At any real obscuration the occulter covers the Sun's centre,
	// so an annulus taken about the solar centre is mostly Moon and every level comes out equal —
	// which reads as a passing test for a completely broken render.
	moonLevel := medianOverMask(img, g.OccludedMask(img.W, img.H, 0))
	skyLevel := medianOverMask(img, g.SkyMask(img.W, img.H, 0))
	discLevel := medianOverMask(img, g.VisibleSunMask(img.W, img.H, 0))
	t.Logf("rendered levels — occulter %.4f | sky %.4f | disc %.4f", moonLevel, skyLevel, discLevel)

	if math.Abs(moonLevel-skyLevel) > 0.05 {
		t.Errorf("the occulter renders at %.4f against a sky of %.4f; it should sit with the background",
			moonLevel, skyLevel)
	}
	if moonLevel >= discLevel {
		t.Errorf("the occulter (%.4f) is not darker than the disc (%.4f)", moonLevel, discLevel)
	}
}

// TestToneMapDisc_FindsTheDiscLevelOnADeepCrescent is the measurement behind the burnt render.
//
// The curve builds its window around "the disc level", sampled as the median inside 0.6 R. Past
// about seventy percent obscuration the occulter covers the disc's CENTRE, so there is no
// un-occluded Sun anywhere inside 0.6 R at all — masking the occulter out leaves the sample EMPTY,
// the level comes back zero, the window collapses to nothing, and every pixel above the sky renders
// at the top of the curve. On a real 82%-obscured master that is exactly what happened.
//
// Tested on the tone plane rather than on the finished image, because the gold ramp holds red at
// 1.00 for every tone above 0.82 by design — so a check for a saturated red channel reports "bright"
// and never "burnt", and passes just as happily on an image that is entirely clipped.
func TestToneMapDisc_FindsTheDiscLevelOnADeepCrescent(t *testing.T) {
	for _, sep := range []float64{1.0, 0.5, 0.2, 0.07} {
		master, g := eclipsedMaster(t, sep)
		vis := g.VisibleSunMask(master.W, master.H, 0)
		skip := occulterSkip(g)
		level := MaskedMedian(master.Pix[0], vis)

		burnt := func(tone []float32) float64 {
			var hot, total int
			for i, m := range vis {
				if m < 0.5 {
					continue
				}
				total++
				if tone[i] >= 0.999 {
					hot++
				}
			}
			if total == 0 {
				return 0
			}
			return 100 * float64(hot) / float64(total)
		}
		anchored := burnt(toneMapDisc(master.Pix[0], master.W, master.H, g.Sun, 0.5, 1, level, skip))
		unanchored := burnt(toneMapDisc(master.Pix[0], master.W, master.H, g.Sun, 0.5, 1, 0, skip))
		t.Logf("obscuration %4.1f%%: level %.4f | burnt %.1f%% anchored, %.1f%% from the 0.6R sample",
			100*g.Obscuration, level, anchored, unanchored)

		if anchored > 2 {
			t.Errorf("sep %.2fR: %.1f%% of the crescent is at the top of the curve", sep, anchored)
		}
	}
}

// TestOcculterSkip_CoversTheBodyAndNothingElse pins the predicate every averaging step is handed.
func TestOcculterSkip_CoversTheBodyAndNothingElse(t *testing.T) {
	_, g := eclipsedMaster(t, 0.5)
	skip := occulterSkip(g)
	if skip == nil {
		t.Fatal("no predicate for a frame with an occulter")
	}
	if !skip(int(g.Moon.CX), int(g.Moon.CY)) {
		t.Error("the occulter's own centre is not skipped")
	}
	// A point on the crescent, opposite the occulter, must be kept.
	dx, dy := g.Moon.CX-g.Sun.CX, g.Moon.CY-g.Sun.CY
	if d := math.Hypot(dx, dy); d > 1 {
		x := int(g.Sun.CX - 0.9*g.Sun.R*dx/d)
		y := int(g.Sun.CY - 0.9*g.Sun.R*dy/d)
		if skip(x, y) {
			t.Error("a point on the crescent is being skipped")
		}
	}
	// And with no occulter there is nothing to skip, so the finish takes its original path exactly.
	if occulterSkip(Pair{Sun: g.Sun}) != nil {
		t.Error("a predicate was built for an unoccluded Sun")
	}
}

// medianOverMask is the median rendered luminance where a 0..1 mask is set.
func medianOverMask(img *fits.Image, mask []float32) float64 {
	vals := make([]float32, 0, 1<<14)
	for i, m := range mask {
		if m < 0.5 {
			continue
		}
		var sum float32
		n := minInt(img.C, 3)
		for c := 0; c < n; c++ {
			sum += img.Pix[c][i]
		}
		vals = append(vals, sum/float32(n))
	}
	if len(vals) == 0 {
		return 0
	}
	return float64(imgops.Percentile(imgops.Subsample(vals, 100000), 50))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
