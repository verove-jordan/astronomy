package optics

import (
	"fmt"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// AnalyzeFlat loads a master flat, detects its defects, and grades it. rawFlatPaths (optional) are the
// raw sub-flats used only to sample saturation; pass nil to skip that check. It soft-fails: on a read
// error it returns a zero FlatQC (Status "") plus the error.
func AnalyzeFlat(masterPath string, rawFlatPaths []string) (FlatQC, []Defect, error) {
	plane, w, h, scale, _, err := LoadFlatPlane(masterPath)
	if err != nil {
		return FlatQC{}, nil, err
	}
	defects, _ := DetectDefects(plane, w, h, scale)

	qc := masterQC(plane, w, h)
	qc.SaturFrac = rawSaturFrac(rawFlatPaths, &qc)

	totalPx := float64(w) * scale * float64(h) * scale
	setStatus(&qc, defectScore(defects, totalPx), maxDepth(defects))
	return qc, defects, nil
}

// masterQC computes the exposure/vignetting/uniformity metrics from the [0,1] detection plane.
func masterQC(plane []float32, w, h int) FlatQC {
	globalMed := imgops.Percentile(imgops.Subsample(plane, 200000), 50)
	grid := tileGrid(plane, w, h)
	tmin, tmax := tileExtremes(grid, globalMed)
	return FlatQC{
		Level:         globalMed,
		VignetteDepth: vignetteDepth(grid),
		TileMin:       tmin,
		TileMax:       tmax,
		DeadFrac:      deadFraction(plane, globalMed),
	}
}

// setStatus applies the QC thresholds to fill Status and append a human note per trigger. `score` is
// the integrated defect burden and `deepest` the deepest single defect.
func setStatus(qc *FlatQC, score, deepest float64) {
	bad := false
	if qc.SaturFrac > saturBad {
		bad = true
		qc.Notes = append(qc.Notes, fmt.Sprintf("saturated: %.1f%% of raw-flat pixels clipped (limit %.0f%%)", qc.SaturFrac*100, saturBad*100))
	}
	if qc.Level < levelBadLo {
		bad = true
		qc.Notes = append(qc.Notes, fmt.Sprintf("too dim: median level %.1f%% of full scale (min %.0f%%)", qc.Level*100, levelBadLo*100))
	}
	if qc.Level > levelBadHi {
		bad = true
		qc.Notes = append(qc.Notes, fmt.Sprintf("too bright: median level %.1f%% of full scale (max %.0f%%)", qc.Level*100, levelBadHi*100))
	}
	if bad {
		qc.Status = "bad"
		return
	}

	warn := false
	warnf := func(cond bool, msg string, args ...any) {
		if cond {
			warn = true
			qc.Notes = append(qc.Notes, fmt.Sprintf(msg, args...))
		}
	}
	warnf(qc.Level < levelWarnLo, "dim: median level %.1f%% of full scale", qc.Level*100)
	warnf(qc.Level > levelWarnHi, "bright: median level %.1f%% of full scale", qc.Level*100)
	warnf(qc.VignetteDepth > vignetteWarn, "heavy vignetting: %.0f%% corner falloff", qc.VignetteDepth*100)
	warnf(score > defectScoreWarn, "defect burden %.2e above %.0e", score, defectScoreWarn)
	warnf(deepest > deepDefectWarn, "deep defect: %.1f%% dip", deepest*100)

	if warn {
		qc.Status = "warn"
	} else {
		qc.Status = "ok"
	}
}
