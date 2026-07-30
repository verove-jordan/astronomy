package mosaic

import "math"

// seamSampleBudget caps the total number of overlap probes behind the SeamRMS metric.
const seamSampleBudget = 20_000

// ChannelAssembly reports one channel's assembly for run.json.
type ChannelAssembly struct {
	Panels      []PanelPlacement `json:"panels"`
	SeamRMS     float64          `json:"seam_rms"`
	CoveredFrac float64          `json:"covered_frac"`
}

// PanelPlacement is one panel's contribution: the canvas-space bbox actually written (X1/Y1
// exclusive) and the photometric correction applied.
type PanelPlacement struct {
	Label  string  `json:"label"`
	X0     int     `json:"x0"`
	Y0     int     `json:"y0"`
	X1     int     `json:"x1"`
	Y1     int     `json:"y1"`
	Gain   float64 `json:"gain"`
	Offset float64 `json:"offset"`
}

// Options tune the blend. The zero value takes every default; EdgeErodePx < 0 disables erosion
// explicitly (0 means "default").
type Options struct {
	FeatherFrac float64 // default 0.6
	OverlapFrac float64 // expected capture overlap, default 0.2
	EdgeErodePx int     // default 8
	Workers     int     // ≤0 → 1
}

// withDefaults resolves the zero-value conventions documented on Options.
func (o Options) withDefaults() Options {
	if o.FeatherFrac <= 0 {
		o.FeatherFrac = 0.6
	}
	if o.OverlapFrac <= 0 {
		o.OverlapFrac = 0.2
	}
	if o.EdgeErodePx == 0 {
		o.EdgeErodePx = 8
	} else if o.EdgeErodePx < 0 {
		o.EdgeErodePx = 0
	}
	if o.Workers <= 0 {
		o.Workers = 1
	}
	return o
}

// seamRMS re-samples the pairwise overlap regions after correction and reports a robust RMS of
// corrected_A − corrected_B: per pair, the differences are 5σ-MAD-clipped (stars and PSF mismatch
// must not masquerade as a sky seam) and the RMS is taken about ZERO, so a residual systematic
// offset — the visible seam — counts in full. Pairs combine weighted by their kept sample counts.
func seamRMS(panels []PanelImage, canvas CanvasSpec, gains, offsets []float64) float64 {
	cands := candidatePairs(panels, canvas)
	if len(cands) == 0 {
		return 0
	}
	per := seamSampleBudget / len(cands)
	if per < 100 {
		per = 100
	}
	var sumSq, sumN float64
	for _, c := range cands {
		va, vb := overlapSamples(panels[c.a], panels[c.b], canvas, per)
		if len(va) < 50 {
			continue
		}
		diffs := make([]float64, len(va))
		for k := range va {
			ca := float64(va[k])*gains[c.a] + offsets[c.a]
			cb := float64(vb[k])*gains[c.b] + offsets[c.b]
			diffs[k] = ca - cb
		}
		med := medianF64(diffs)
		lim := 5 * madSigmaF64(diffs, med)
		var ss float64
		n := 0
		for _, d := range diffs {
			if lim > 0 && math.Abs(d-med) > lim {
				continue
			}
			ss += d * d
			n++
		}
		if n == 0 {
			continue
		}
		sumSq += ss
		sumN += float64(n)
	}
	if sumN == 0 {
		return 0
	}
	return math.Sqrt(sumSq / sumN)
}
