// Package nightscape ports the proven Milky-Way nightscape recipe (star-aligned sky stack + single
// clean foreground, masked composite, linear colour grade) into Go. It drives Siril for registration
// and does all per-pixel maths here, so the pipeline stays native Go (no Python).
//
// The numeric helpers the recipe relies on live in the shared internal/imgops package; the thin
// wrappers below keep the recipe's existing call sites unchanged while avoiding a second copy of the
// implementations.
package nightscape

import "github.com/verove-jordan/astronomy/internal/imgops"

// percentile returns the linear-interpolated p-th percentile (0..100) of vals (numpy default).
func percentile(vals []float32, p float64) float64 { return imgops.Percentile(vals, p) }

// gaussianBlur approximates a Gaussian filter using three O(N) box passes (edge-clamped).
func gaussianBlur(src []float32, w, h int, sigma float64) []float32 {
	return imgops.GaussianBlur(src, w, h, sigma)
}

// medianFilter applies a size×size median filter (reflect boundary).
func medianFilter(src []float32, w, h, size int) []float32 {
	return imgops.MedianFilter(src, w, h, size)
}

// binaryDilation grows the true region by `iterations` steps of 4-connectivity.
func binaryDilation(mask []bool, w, h, iterations int) []bool {
	return imgops.BinaryDilation(mask, w, h, iterations)
}

// label assigns a connected-component id (1..n) to each true pixel with 4-connectivity.
func label(mask []bool, w, h int) (labels []int, n int) { return imgops.Label(mask, w, h) }
