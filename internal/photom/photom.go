// Package photom performs photometric curve-matching normalization for heterogeneous astro frames.
//
// When light frames of one target come from different sessions — different sky transparency, exposure,
// gain, or optical train — their background and signal levels differ, which biases a naive stack. This
// package measures each frame's tonal distribution as a 14-point percentile "curve", fits a robust
// affine transform (Theil–Sen scale + median offset) mapping a group's curve onto a reference group's
// curve, and rewrites the group's pixels in place so every group shares the reference's photometric
// scale. Star-saturation percentiles are excluded from the fit, and a metadata prior (exposure/gain)
// flags — but never overrides — a measurement that disagrees with the headers.
//
// Normalization is soft-fail: any per-file or per-group error is recorded as a note and the frames are
// left untouched, so a run never fails because of it.
package photom

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// CurveQ are the 14 probe percentiles summarising a frame's tonal distribution, from the sky
// background up to the star-saturation shoulder. Indices 12 and 13 (P99, P99.5) are saturation
// sensitive and excluded from the affine fit (see fit.go).
var CurveQ = []float64{5, 10, 20, 30, 40, 50, 60, 70, 80, 90, 95, 97.5, 99, 99.5}

const (
	// bgIdx points at CurveQ == P40, used as the frame's background reference level.
	bgIdx = 3
	// sampleLimit caps the number of pixels fed to the percentile/MAD estimators for speed.
	sampleLimit = 200_000
	// madToSigma converts a median-absolute-deviation into a Gaussian-equivalent sigma.
	madToSigma = 1.4826
)

// FrameCurve summarises one frame: 14 probe percentiles plus a background level and a noise estimate.
type FrameCurve struct {
	Q     [14]float64 `json:"q"`
	Bg    float64     `json:"bg"`    // Q at P40 (index 3)
	Noise float64     `json:"noise"` // MAD-derived sigma of the subsample
}

// MeasureImage summarises an image as a FrameCurve. Mono images use plane 0; RGB images (C>=3) use the
// per-pixel mean across channels so a single curve describes the frame.
func MeasureImage(im *fits.Image) FrameCurve {
	sample := imgops.Subsample(sampleSlice(im), sampleLimit)
	var fc FrameCurve
	for i, p := range CurveQ {
		fc.Q[i] = imgops.Percentile(sample, p)
	}
	fc.Bg = fc.Q[bgIdx]
	fc.Noise = madToSigma * mad(sample)
	return fc
}

// MeasureFile reads a FITS file and summarises it as a FrameCurve.
func MeasureFile(path string) (FrameCurve, error) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return FrameCurve{}, fmt.Errorf("measure %s: %w", path, err)
	}
	return MeasureImage(im), nil
}

// sampleSlice returns the pixel slice to summarise: plane 0 for mono, else the per-pixel mean across
// the first (up to three) channels. It never mutates the image and returns nil for an empty image.
func sampleSlice(im *fits.Image) []float32 {
	if im == nil || len(im.Pix) == 0 {
		return nil
	}
	if im.C == 1 {
		return im.Pix[0]
	}
	nc := im.C
	if nc > 3 {
		nc = 3
	}
	n := im.W * im.H
	out := make([]float32, n)
	for c := 0; c < nc && c < len(im.Pix); c++ {
		src := im.Pix[c]
		for i := 0; i < n && i < len(src); i++ {
			out[i] += src[i]
		}
	}
	inv := float32(1) / float32(nc)
	for i := range out {
		out[i] *= inv
	}
	return out
}

// mad returns the median absolute deviation of vals: median(|x - median(x)|). It allocates its own
// buffers and never mutates the input.
func mad(vals []float32) float64 {
	if len(vals) == 0 {
		return 0
	}
	med := imgops.Percentile(vals, 50)
	dev := make([]float32, len(vals))
	for i, v := range vals {
		dev[i] = float32(math.Abs(float64(v) - med))
	}
	return imgops.Percentile(dev, 50)
}
