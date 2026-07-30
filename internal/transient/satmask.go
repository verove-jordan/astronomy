// Saturated-core repair for multi-night merges (task #354 follow-up: "galaxy centres must never
// burn"). M81-class cores hard-saturate the sensor on high-gain nights; after CORRECT photometric
// normalization each night's plateau lands at its own `scale·ceiling + offset` — BELOW the true
// flux and flat — and stack rejection cannot remove a coherent level-matched plateau. Measured on
// the real five-night data: 81 of 131 L frames were clipped, so the PLAIN cross-frame median is the
// plateau; the repair therefore replaces saturated pixels from the SUB-CEILING median — the
// per-pixel median over only the frames that still see the true value — and leaves a pixel
// untouched when fewer than minCleanSamples frames do (a universally-clipped core has no true
// value to restore; the finish's headroom cap handles it as before).
package transient

import (
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	// satCeilMargin scales a frame's post-normalization saturation ceiling into the detection
	// threshold: flat-field division, background flattening and registration resampling all wobble
	// the plateau below the exact mapped ceiling. A generous margin is SAFE by construction — a
	// falsely-flagged bright-but-clean pixel finds its peers flagged too, fails minCleanSamples,
	// and is left untouched.
	satCeilMargin = 0.90
	// minCleanSamples is the fewest sub-ceiling, non-zero samples a pixel needs before the clean
	// median is trusted as its replacement.
	minCleanSamples = 5
)

// satThreshold is a frame's saturation-detection threshold; 0 (disabled) when the ceiling is
// unknown or nonsensical.
func satThreshold(ceil float32) float32 {
	if ceil <= 0 {
		return 0
	}
	return ceil * satCeilMargin
}

// cleanMedianPlanes computes, per channel, a SPARSE per-pixel median over the frames whose own
// value sits below their own threshold — evaluated only at pixels where at least one frame is
// saturated. Zero-fill pixels (outside a rotated frame's footprint) never count as clean samples.
// Pixels with fewer than minCleanSamples clean samples are absent from the result.
func cleanMedianPlanes(frames []*fits.Image, satCeil []float32, w, h, c int) []map[int]float32 {
	planes := make([]map[int]float32, c)
	for ch := 0; ch < c; ch++ {
		planes[ch] = map[int]float32{}
	}
	saturated := make([]bool, w*h)
	for ch := 0; ch < c; ch++ {
		clear(saturated)
		for fi, f := range frames {
			thr := satThreshold(ceilAt(satCeil, fi))
			if thr <= 0 {
				continue
			}
			for p, v := range f.Pix[ch] {
				if v >= thr {
					saturated[p] = true
				}
			}
		}
		clean := make([]float64, 0, len(frames))
		for p, sat := range saturated {
			if !sat {
				continue
			}
			clean = clean[:0]
			for fi, f := range frames {
				thr := satThreshold(ceilAt(satCeil, fi))
				if thr <= 0 {
					continue
				}
				if v := f.Pix[ch][p]; v > 0 && v < thr {
					clean = append(clean, float64(v))
				}
			}
			if len(clean) < minCleanSamples {
				continue
			}
			sort.Float64s(clean)
			planes[ch][p] = float32(clean[len(clean)/2])
		}
	}
	return planes
}

// repairSaturated replaces the frame's at/above-threshold pixels with the clean median where one
// exists, returning the replaced count. A nil cleanMed or unknown ceiling is a no-op.
func repairSaturated(f *fits.Image, ceil float32, cleanMed []map[int]float32) int {
	thr := satThreshold(ceil)
	if thr <= 0 || len(cleanMed) == 0 {
		return 0
	}
	repaired := 0
	for ch := 0; ch < f.C && ch < len(cleanMed); ch++ {
		for p, v := range f.Pix[ch] {
			if v < thr {
				continue
			}
			if m, ok := cleanMed[ch][p]; ok {
				f.Pix[ch][p] = m
				repaired++
			}
		}
	}
	return repaired
}

// repairSaturatedAll runs the whole in-memory repair: clean medians from all frames, then each
// frame repaired in place. Returns per-frame replaced counts (nil when the pass is disabled).
func repairSaturatedAll(frames []*fits.Image, satCeil []float32, w, h, c int) []int {
	if len(satCeil) == 0 {
		return nil
	}
	cleanMed := cleanMedianPlanes(frames, satCeil, w, h, c)
	counts := make([]int, len(frames))
	for fi, f := range frames {
		counts[fi] = repairSaturated(f, ceilAt(satCeil, fi), cleanMed)
	}
	return counts
}

// ceilAt is a bounds-safe satCeil lookup (0 = disabled).
func ceilAt(satCeil []float32, fi int) float32 {
	if fi < 0 || fi >= len(satCeil) {
		return 0
	}
	return satCeil[fi]
}
