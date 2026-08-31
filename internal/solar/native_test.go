package solar

import (
	"math"
	"testing"
)

// native_test.go pins the measured-colour path without going near ffmpeg.
//
// The decode is one command and the interesting part is everything after it, so the tests build a
// NativeChroma directly and check the two claims that matter: a ramp built from it reproduces the
// hue it describes, and it never asks for a colour a display cannot show.

// chromaOf normalises an RGB triple to unit luminance, the form NativeChroma stores.
func chromaOf(r, g, b float64) (float64, float64, float64) {
	l := lumaOf(r, g, b)
	return r / l, g / l, b / l
}

// flatChroma is a measurement of one hue held across every quantile — the Hα case, where the whole
// scene is a single colour and only its brightness varies.
func flatChroma(r, g, b float64) NativeChroma {
	cr, cg, cb := chromaOf(r, g, b)
	var n NativeChroma
	for i := 0; i < nativeQuantiles; i++ {
		n.Q = append(n.Q, (float64(i)+0.5)/nativeQuantiles)
		n.R, n.G, n.B = append(n.R, cr), append(n.G, cg), append(n.B, cb)
	}
	return n
}

// TestNativeRamp_ReproducesTheMeasuredHue is the round trip: a ramp built from a measurement must
// render in the colour that measurement describes.
//
// The check is on the RATIOS rather than on the values, because brightness is deliberately not the
// measurement's business — the finish's tone curve owns that, which is what lets the colour come
// from the recording while the exposure stays as it was tuned.
func TestNativeRamp_ReproducesTheMeasuredHue(t *testing.T) {
	// The colour actually measured on the 12 Aug clips, in the bright part of the frame.
	const wr, wg, wb = 0.5925, 0.1550, 0.1472

	// A rendered plane spanning the full range, so every quantile has somewhere to land.
	rendered := make([]float32, 4096)
	for i := range rendered {
		rendered[i] = float32(i) / float32(len(rendered)-1)
	}
	ramp := nativeRamp(flatChroma(wr, wg, wb), rendered, nil)
	if len(ramp) < 2 {
		t.Fatal("no ramp was built from a perfectly good measurement")
	}

	// Mid-tones, where the hue is reproduced without any headroom trouble.
	r, g, b := sampleRamp(ramp, 0.35)
	gotRG, wantRG := g/r, wg/wr
	gotBR, wantBR := b/r, wb/wr
	t.Logf("at t=0.35 the ramp renders %.3f %.3f %.3f — G/R %.3f (want %.3f), B/R %.3f (want %.3f)",
		r, g, b, gotRG, wantRG, gotBR, wantBR)
	if math.Abs(gotRG-wantRG) > 0.05 || math.Abs(gotBR-wantBR) > 0.05 {
		t.Errorf("the ramp does not reproduce the measured hue")
	}
	// And the brightness is the render's, not the measurement's.
	if l := lumaOf(r, g, b); math.Abs(l-0.35) > 0.02 {
		t.Errorf("t=0.35 rendered at luminance %.3f; brightness must stay the tone curve's", l)
	}
}

// TestNativeRamp_IsMonotoneAndInRange guards the two ways a measured ramp can be malformed in a way
// the tabulated ones never are.
//
// sampleRamp walks its stops assuming t increases, so a ramp with a repeated or decreasing stop
// silently returns the wrong colour; and a saturated hue at high luminance asks for channel values
// above one, which if clipped per channel would swing the hue as the image brightened.
func TestNativeRamp_IsMonotoneAndInRange(t *testing.T) {
	// A deep red, which is the hardest case: unit luminance would need a red channel of 3.3.
	rendered := make([]float32, 2048)
	for i := range rendered {
		// Deliberately degenerate: half the plane sits at one value, so several quantiles collide.
		if i < len(rendered)/2 {
			rendered[i] = 0.2
		} else {
			rendered[i] = float32(i) / float32(len(rendered)-1)
		}
	}
	ramp := nativeRamp(flatChroma(1.0, 0.04, 0.02), rendered, nil)
	if len(ramp) < 2 {
		t.Fatal("no ramp was built")
	}
	for i := 1; i < len(ramp); i++ {
		if ramp[i].t <= ramp[i-1].t {
			t.Fatalf("stop %d is at t=%.5f, not past its predecessor's %.5f", i, ramp[i].t, ramp[i-1].t)
		}
	}
	for i, s := range ramp {
		for _, v := range []float64{s.r, s.g, s.b} {
			if v < 0 || v > 1 {
				t.Errorf("stop %d at t=%.3f asks for %.3f, outside 0..1", i, s.t, v)
			}
		}
	}
	// The ramp must reach both ends, or everything below the first stop renders in its colour.
	if ramp[0].t > 1e-6 {
		t.Errorf("the ramp starts at t=%.4f; everything darker would be extrapolated", ramp[0].t)
	}
	if last := ramp[len(ramp)-1]; last.t < 1-1e-6 {
		t.Errorf("the ramp ends at t=%.4f; everything brighter would be extrapolated", last.t)
	}
}

// TestFitToLuma_DesaturatesRatherThanClips pins the highlight behaviour.
//
// A saturated hue cannot be made bright without exceeding the display's range. Clipping each channel
// on its own would hold red at 1 while green and blue kept climbing, so the hue would drift towards
// yellow as the image brightened — visible as a colour cast on plage and flares specifically. Pulling
// the whole triple towards the neutral of the same luminance keeps the hue as long as it can and then
// gives it up gracefully, which is what a highlight roll-off does and what the phone itself did.
func TestFitToLuma_DesaturatesRatherThanClips(t *testing.T) {
	cr, cg, cb := chromaOf(1.0, 0.04, 0.02)
	var lastSat float64 = -1
	for _, lum := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
		r, g, b := fitToLuma(cr, cg, cb, lum)
		if r > 1.0001 || g > 1.0001 || b > 1.0001 {
			t.Fatalf("luminance %.1f produced %.3f %.3f %.3f, outside range", lum, r, g, b)
		}
		if got := lumaOf(r, g, b); math.Abs(got-lum) > 0.02 {
			t.Errorf("asked for luminance %.2f, got %.3f", lum, got)
		}
		// Saturation must fall as it brightens, never rise.
		sat := (math.Max(r, math.Max(g, b)) - math.Min(r, math.Min(g, b))) / math.Max(r, math.Max(g, b))
		if lastSat >= 0 && sat > lastSat+1e-6 {
			t.Errorf("saturation rose from %.3f to %.3f between luminance steps", lastSat, sat)
		}
		lastSat = sat
		t.Logf("luminance %.1f → %.3f %.3f %.3f (saturation %.3f)", lum, r, g, b, sat)
	}
}

// TestNativeChroma_RefusesWhatItCannotMeasure keeps the fallback honest: a run pointed at something
// with no colour to read must say so rather than render in an invented one.
func TestNativeChroma_RefusesWhatItCannotMeasure(t *testing.T) {
	if (NativeChroma{}).OK() {
		t.Error("an empty measurement reports itself usable")
	}
	if (NativeChroma{Q: []float64{0.5}, R: []float64{1}, G: []float64{1}, B: []float64{1}}).OK() {
		t.Error("a single-point measurement reports itself usable; a ramp needs at least two")
	}
	if (NativeChroma{Q: []float64{0.2, 0.8}, R: []float64{1}}).OK() {
		t.Error("a ragged measurement reports itself usable")
	}
	// And a ramp cannot be built from one.
	if r := nativeRamp(NativeChroma{}, make([]float32, 100), nil); r != nil {
		t.Errorf("built a %d-stop ramp from nothing", len(r))
	}
}
