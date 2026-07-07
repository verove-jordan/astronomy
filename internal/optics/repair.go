package optics

import (
	"context"
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// repairMaxRel clamps how strongly a residual can be divided out (+/-8%), so a mis-measured residual
// can never brighten/darken a light frame by more than that.
const repairMaxRel = 0.08

// RepairFrames divides out each actionable residual defect from every light frame in place. A defect
// is actionable when its residual is Consistent and |Rel| > residualActionable; among those, only ones
// smaller than defectMaxAreaFrac of the frame are actually repaired (larger ones are flagged, never
// touched). For each repaired pixel p in the full-res bbox: p /= (1 - r*shape01), with r clamped to
// +/-repairMaxRel and the denominator guarded above 0.2. Returns the number of frames rewritten plus a
// note per actionable defect. Honors ctx and soft-fails per frame.
func RepairFrames(ctx context.Context, defects []Defect, residuals []Residual, framePaths []string) (int, []string) {
	actionable := selectActionable(defects, residuals)
	if len(actionable) == 0 {
		return 0, nil
	}
	frameArea := firstFrameArea(framePaths)

	var notes []string
	var toRepair []action
	for _, a := range actionable {
		diam := equivDiameter(a.def.AreaPx)
		oversized := frameArea > 0 && float64(a.def.AreaPx) >= defectMaxAreaFrac*float64(frameArea)
		if oversized {
			notes = append(notes, fmt.Sprintf("defect #%d (Ø%.0fpx at %d,%d) still %+.0f%% after flat correction — too large to repair (>2%% frame); stale flat or dust moved",
				a.idx, diam, a.def.CX, a.def.CY, a.rel*100))
			continue
		}
		notes = append(notes, fmt.Sprintf("defect #%d (Ø%.0fpx at %d,%d) still %+.0f%% after flat correction — stale flat or dust moved",
			a.idx, diam, a.def.CX, a.def.CY, a.rel*100))
		toRepair = append(toRepair, a)
	}
	if len(toRepair) == 0 {
		return 0, notes
	}

	repaired := 0
	for _, fp := range framePaths {
		if err := ctx.Err(); err != nil {
			return repaired, append(notes, "repair cancelled: "+err.Error())
		}
		if err := repairFrame(fp, toRepair); err != nil {
			notes = append(notes, err.Error())
			continue
		}
		repaired++
	}
	return repaired, notes
}

// action pairs a defect with its measured residual and original index.
type action struct {
	idx int
	def Defect
	rel float64
}

// selectActionable keeps defects whose residual is consistent and past the actionable threshold.
func selectActionable(defects []Defect, residuals []Residual) []action {
	var out []action
	for i := range defects {
		if i >= len(residuals) {
			break
		}
		r := residuals[i]
		if r.Consistent && math.Abs(r.Rel) > residualActionable {
			out = append(out, action{idx: i, def: defects[i], rel: r.Rel})
		}
	}
	return out
}

// repairFrame reads a light frame, divides out each repairable defect across all channels, and writes
// the pixel data back with OverwriteData (header preserved).
func repairFrame(path string, toRepair []action) error {
	im, err := fits.ReadImage(path)
	if err != nil {
		return fmt.Errorf("optics: read frame %s: %w", path, err)
	}
	for i := range toRepair {
		applyRepair(im, &toRepair[i].def, toRepair[i].rel)
	}
	if err := im.OverwriteData(path); err != nil {
		return fmt.Errorf("optics: write frame %s: %w", path, err)
	}
	return nil
}

// applyRepair divides the defect's residual out of every channel over its full-res bbox.
func applyRepair(im *fits.Image, d *Defect, rel float64) {
	sw, sh := shapeDims(d)
	if sw == 0 {
		return
	}
	fw, fh := d.X1-d.X0+1, d.Y1-d.Y0+1
	r := clampF(rel, -repairMaxRel, repairMaxRel)
	for ry := 0; ry < fh; ry++ {
		fy := d.Y0 + ry
		if fy < 0 || fy >= im.H {
			continue
		}
		for rx := 0; rx < fw; rx++ {
			fx := d.X0 + rx
			if fx < 0 || fx >= im.W {
				continue
			}
			s := clampF(float64(sampleShape(d, sw, sh, rx, ry, fw, fh)), 0, 1)
			denom := 1 - r*s
			if denom <= 0.2 {
				continue
			}
			idx := fy*im.W + fx
			for c := 0; c < im.C; c++ {
				im.Pix[c][idx] = float32(float64(im.Pix[c][idx]) / denom)
			}
		}
	}
}

// firstFrameArea returns W*H of the first readable frame (0 if none), used to size the "too large to
// repair" guard without loading every frame.
func firstFrameArea(framePaths []string) int {
	for _, fp := range framePaths {
		if f, err := fits.Open(fp); err == nil {
			if w, h := f.Dimensions(); w > 0 && h > 0 {
				return w * h
			}
		}
	}
	return 0
}
