package nightscape

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestNormalizeToRef_MatchesReference checks that a frame which is an affine transform of the reference
// (a different ISO gain k and white-balance offset d per channel — the auto-mode drift) is mapped back
// onto the reference's background and gain, i.e. normalizeToRef inverts the affine. Recovery is exact
// because percentiles transform linearly under x→k·x+d (for k in the unclamped [0.5,2] range).
func TestNormalizeToRef_MatchesReference(t *testing.T) {
	const n = 400
	ref := fits.NewImage(n, 1, 3)
	for c := 0; c < 3; c++ {
		for i := 0; i < n; i++ {
			// A varied, per-channel distribution so bg (P40) and gain (P95−P40) are well-defined.
			ref.Pix[c][i] = float32(0.02*float64(c) + float64(i)/float64(n)*(0.4+0.1*float64(c)))
		}
	}
	// frame = k·ref + d per channel, with k in [0.5,2] so the gain scale isn't clamped.
	k := [3]float64{1.4, 0.8, 1.6}
	d := [3]float64{0.05, -0.01, 0.03}
	frame := ref.Clone()
	for c := 0; c < 3; c++ {
		for i := range frame.Pix[c] {
			frame.Pix[c][i] = float32(k[c]*float64(frame.Pix[c][i]) + d[c])
		}
	}

	normalizeToRef(frame, measureFrame(frame), measureFrame(ref))

	var maxErr float64
	for c := 0; c < 3; c++ {
		for i := range frame.Pix[c] {
			e := math.Abs(float64(frame.Pix[c][i] - ref.Pix[c][i]))
			if e > maxErr {
				maxErr = e
			}
		}
	}
	if maxErr > 1e-4 {
		t.Fatalf("normalized frame did not recover the reference: max error %g", maxErr)
	}
}

// TestMeasureFrame_BackgroundAndGain checks the per-channel background (P40) and gain (P95−P40) on a
// known ramp.
func TestMeasureFrame_BackgroundAndGain(t *testing.T) {
	const n = 1000
	im := fits.NewImage(n, 1, 1)
	for i := 0; i < n; i++ {
		im.Pix[0][i] = float32(i) / float32(n-1) // 0..1 ramp → P40≈0.4, P95≈0.95
	}
	fn := measureFrame(im)
	if math.Abs(fn.bg[0]-0.4) > 0.02 {
		t.Fatalf("bg = %v, want ~0.4", fn.bg[0])
	}
	if math.Abs(fn.gain[0]-0.55) > 0.02 {
		t.Fatalf("gain = %v, want ~0.55", fn.gain[0])
	}
}
