// Package transient removes per-pixel cross-frame transients — satellite/aircraft trails, cosmic rays,
// hot pixels — from a set of REGISTERED frames before they are stacked. At each aligned sky pixel a
// transient is present in only one (or a few) of the frames, so it is a strong POSITIVE outlier against
// the per-pixel median. Such pixels are replaced by the median; every consistent pixel (real stars and
// nebulosity, which match across the aligned frames) is left untouched — so a trail is removed with no
// global SNR cost, unlike tightening the whole stack's sigma rejection.
//
// This is the right tool for a SLOW satellite that lands in many subs as a short streak at a marching
// position: it is not "one bad frame" to reject, but a transient that, per sky pixel, only ever appears
// once and so is cleanly separable across the aligned stack.
package transient

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// MinFrames is the smallest frame count at which the per-pixel median + MAD is robust enough to mask
// transients without risking real signal (a lone outlier among <5 samples is not cleanly separable).
// Below it MaskCrossFrame is a no-op.
const MinFrames = 5

// MaskCrossFrame replaces, in place, every pixel that is a strong POSITIVE outlier across the frames
// (value > per-pixel median + k·MADσ) with that pixel's median across the frames, and returns the number
// of pixel samples cleaned. The frames must share dimensions/channels and be REGISTERED (aligned) so a
// given sky position is the same pixel in every frame. With fewer than MinFrames frames, or k ≤ 0, it
// does nothing. Only the high side is clipped: faint real signal (consistent across frames → tiny
// per-pixel spread) is never flagged, and bright stars self-protect because their own frame-to-frame
// jitter inflates the per-pixel MAD.
func MaskCrossFrame(frames []*fits.Image, k float64) (int, error) {
	n := len(frames)
	if n < MinFrames || k <= 0 {
		return 0, nil
	}
	w, h, c := frames[0].W, frames[0].H, frames[0].C
	for i, f := range frames {
		if f.W != w || f.H != h || f.C != c {
			return 0, fmt.Errorf("frame %d is %dx%dx%d, want %dx%dx%d", i, f.W, f.H, f.C, w, h, c)
		}
	}

	col := make([]float64, n)
	dev := make([]float64, n)
	masked := 0
	for ch := 0; ch < c; ch++ {
		planes := make([][]float32, n)
		for i := range frames {
			planes[i] = frames[i].Pix[ch]
		}
		for p := range planes[0] {
			for i := range planes {
				col[i] = float64(planes[i][p])
			}
			med := median(col)
			for i := range col {
				dev[i] = math.Abs(col[i] - med)
			}
			mad := 1.4826 * median(dev)
			if mad <= 0 {
				continue
			}
			thr := med + k*mad
			for i := range planes {
				if float64(planes[i][p]) > thr {
					planes[i][p] = float32(med)
					masked++
				}
			}
		}
	}
	return masked, nil
}

// median sorts the scratch slice in place (mutating it) and returns its median.
func median(v []float64) float64 {
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return 0.5 * (v[n/2-1] + v[n/2])
}
