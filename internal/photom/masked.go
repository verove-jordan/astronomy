package photom

import (
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// minMaskedSamples is the floor below which a masked measurement is statistically meaningless. The
// caller must then degrade to a NO-OP — never to a whole-frame measurement: comparing a masked
// curve against a whole-frame one is exactly the different-sky-mix mismatch the mask exists to
// avoid.
const minMaskedSamples = 20_000

// MeasureImageMasked summarises only the pixels keep(x, y) admits, with the same percentile curve,
// background and MAD noise as MeasureImage (mono plane 0; RGB per-pixel channel mean). ok is false
// when fewer than minMaskedSamples pixels were admitted.
func MeasureImageMasked(im *fits.Image, keep func(x, y int) bool) (FrameCurve, bool) {
	plane := sampleSlice(im)
	if len(plane) == 0 || keep == nil {
		return FrameCurve{}, false
	}
	accepted := countAccepted(im.W, im.H, keep)
	if accepted < minMaskedSamples {
		return FrameCurve{}, false
	}
	stride := accepted / sampleLimit
	if stride < 1 {
		stride = 1
	}
	sample := make([]float32, 0, accepted/stride+1)
	seen := 0
	for y := 0; y < im.H; y++ {
		row := y * im.W
		for x := 0; x < im.W; x++ {
			if !keep(x, y) {
				continue
			}
			if seen%stride == 0 {
				sample = append(sample, plane[row+x])
			}
			seen++
		}
	}
	return curveOf(sample), true
}

// MedianCurve returns the component-wise median of curves — the group-level summary shared by the
// normalization fit and the seam offset refit.
func MedianCurve(curves []FrameCurve) FrameCurve {
	return medianCurve(curves)
}

// countAccepted counts the pixels the mask admits.
func countAccepted(w, h int, keep func(x, y int) bool) int {
	n := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if keep(x, y) {
				n++
			}
		}
	}
	return n
}

// curveOf summarises a pre-collected pixel sample as a FrameCurve — the shared core of
// MeasureImage (whole-frame subsample) and MeasureImageMasked (mask-admitted sample).
func curveOf(sample []float32) FrameCurve {
	var fc FrameCurve
	for i, p := range CurveQ {
		fc.Q[i] = imgops.Percentile(sample, p)
	}
	fc.Bg = fc.Q[bgIdx]
	fc.Noise = madToSigma * mad(sample)
	if len(sample) > 0 {
		sat := 0
		for _, v := range sample {
			if v >= SatDetectLevel {
				sat++
			}
		}
		fc.SatFrac = float64(sat) / float64(len(sample))
	}
	return fc
}
