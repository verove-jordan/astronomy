package optics

import "github.com/verove-jordan/astronomy/internal/fits"

// saturLimit is the fraction of full scale at/above which a pixel counts as saturated.
const saturLimit = 0.98

// rawSaturFrac samples up to three raw flats (first/middle/last) and returns the worst estimated
// saturated fraction. With no raw flats it returns 0 and records a note. Read errors on individual
// frames are skipped (soft-fail).
func rawSaturFrac(paths []string, qc *FlatQC) float64 {
	if len(paths) == 0 {
		qc.Notes = append(qc.Notes, "no raw flats to check saturation")
		return 0
	}
	worst := 0.0
	for _, p := range pickThree(paths) {
		f, err := fits.Open(p)
		if err != nil {
			continue
		}
		st, err := f.Stats(200000)
		if err != nil {
			continue
		}
		if frac := saturFrac(st, saturLimit*fullScaleOf(f)); frac > worst {
			worst = frac
		}
	}
	return worst
}

// pickThree returns the first, middle and last elements of paths (deduplicated), i.e. up to three
// evenly-spread samples.
func pickThree(paths []string) []string {
	switch len(paths) {
	case 0:
		return nil
	case 1:
		return paths[:1]
	case 2:
		return paths
	}
	idx := []int{0, len(paths) / 2, len(paths) - 1}
	out := make([]string, 0, 3)
	seen := map[int]bool{}
	for _, i := range idx {
		if !seen[i] {
			seen[i] = true
			out = append(out, paths[i])
		}
	}
	return out
}

// saturFrac estimates the fraction of pixels at/above threshold T from a Stats summary. Stats gives no
// direct saturation count, so we reconstruct a piecewise-linear CDF from the order statistics
// (Min@0, Median@0.5, P90@0.9, Max@1.0) and return 1 - CDF(T). A constant frame collapses the anchors,
// which the Min/Max guards handle (all-saturated -> 1, none -> 0).
func saturFrac(st fits.Stats, T float64) float64 {
	if T <= st.Min {
		return 1
	}
	if T > st.Max {
		return 0
	}
	segs := [][4]float64{
		{st.Min, 0, st.Median, 0.5},
		{st.Median, 0.5, st.P90, 0.9},
		{st.P90, 0.9, st.Max, 1.0},
	}
	for _, s := range segs {
		v0, f0, v1, f1 := s[0], s[1], s[2], s[3]
		if v1 > v0 && T >= v0 && T <= v1 {
			return 1 - (f0 + (f1-f0)*(T-v0)/(v1-v0))
		}
	}
	// T lands only in zero-width (collapsed) upper segments: treat as near-saturated.
	return 0.1
}

// fullScaleOf returns the full-scale pixel value implied by the frame's BITPIX: normalized float
// frames span [0,1]; integer frames span their unsigned container. Assumes ADU fills the container
// (true for the ASI 1600 captures, which are left-shifted to 16-bit).
func fullScaleOf(f *fits.File) float64 {
	bitpix, _ := f.Header.Int("BITPIX")
	switch bitpix {
	case 8:
		return 255
	case 16:
		return 65535
	case 32:
		return 4294967295
	case -32, -64:
		return 1.0
	}
	return 65535
}
