package transient

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/trail"
)

// medianMADPlanes builds, per channel, the per-pixel median and MAD-σ (1.4826·MAD) across all frames.
// These are the "clean sky" reference the line-aware and blanket masks paint transients back to.
func medianMADPlanes(frames []*fits.Image, w, h, c int) (med, sig [][]float32) {
	med = make([][]float32, c)
	sig = make([][]float32, c)
	n := len(frames)
	col := make([]float64, n)
	dev := make([]float64, n)
	for ch := 0; ch < c; ch++ {
		mp := make([]float32, w*h)
		sp := make([]float32, w*h)
		planes := make([][]float32, n)
		for i := range frames {
			planes[i] = frames[i].Pix[ch]
		}
		for p := range mp {
			for i := range planes {
				col[i] = float64(planes[i][p])
			}
			m := median(col)
			for i := range col {
				dev[i] = math.Abs(col[i] - m)
			}
			mp[p] = float32(m)
			sp[p] = float32(1.4826 * median(dev))
		}
		med[ch], sig[ch] = mp, sp
	}
	return med, sig
}

// maxMedianPlane collapses the per-channel median planes to a single detection plane (per-pixel max),
// so a geostationary trail is caught whichever channel it is brightest in.
func maxMedianPlane(med [][]float32, w, h int) []float32 {
	if len(med) == 1 {
		return med[0]
	}
	out := make([]float32, w*h)
	for i := range out {
		m := med[0][i]
		for ch := 1; ch < len(med); ch++ {
			if v := med[ch][i]; v > m {
				m = v
			}
		}
		out[i] = m
	}
	return out
}

// detectMedianTrails finds straight streaks present in the cross-frame median itself — a geostationary
// object or a fixed reflection that sits on the same sky pixels in a majority of frames, so the median
// is contaminated there and those swaths must be repaired from local background, not the median.
func detectMedianTrails(med [][]float32, w, h int) []trail.Segment {
	return trail.DetectSegments(maxMedianPlane(med, w, h), w, h, trail.RawParams(0))
}

// residualPlane returns the per-pixel residual of a frame over the cross-frame median, collapsed
// across channels by taking the most-positive channel. It is kept SIGNED (not clipped at 0) so the
// sky noise floor survives — a plane clipped to 0 has a zero MAD, which the detector refuses to act on
// — while a satellite present in only this frame still stands out far above that floor.
func residualPlane(f *fits.Image, med [][]float32, w, h, c int) []float32 {
	out := make([]float32, w*h)
	for p := range out {
		best := f.Pix[0][p] - med[0][p]
		for ch := 1; ch < c; ch++ {
			if d := f.Pix[ch][p] - med[ch][p]; d > best {
				best = d
			}
		}
		out[p] = best
	}
	return out
}
