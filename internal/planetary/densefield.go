package planetary

import (
	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// Two-level pass-1 field. The 10×10 coarse grid is robust (big windows, ±apMaxShift search)
// but its ~465 px cells leave sub-cell seeing warp uncorrected at 16 MP — frames then average
// with their fine details ±1–2 px apart and the master smears texture any single sharp frame
// resolves. The dense level reuses the double-stack machinery on the same frame: the coarse
// field seeds a ~apDenseCellPx grid whose structured APs refine within ±apDenseMaxShift;
// featureless cells stay vetoed and ride the coarse baseline. The double-stack pass still
// re-measures against the low-noise master afterwards; this level only shrinks the residual
// warp every frame carries INTO pass 1's average (and into the canonical median field).

// denseContextFrom derives the dense alignment context from an already-built coarse context,
// sharing its blurred/downsampled reference planes and global-lock geometry; only the AP grid
// and its disk/veto masks are rebuilt at dense density (vetoed like every context). alignPoints
// overrides the auto grid density (0 = auto).
func denseContextFrom(ref *fits.Image, rc10 refContext, alignPoints int) refContext {
	rcD := rc10
	rcD.gridN = denseGridNFor(ref.W, ref.H, alignPoints)
	rcD.cx, rcD.cy = apCenters(ref.W, ref.H, rcD.gridN)
	rcD.onDisk = apDiskMask(ref, rcD.cx, rcD.cy)
	rcD.apRadius = min(ref.W, ref.H) * apDensePatchPct / 100
	vetoFeaturelessAPs(&rcD)
	return rcD
}

// measureTwoLevelField measures one frame's displacement field onto the reference with the
// two-level estimator: global lock → coarse 10×10 AP field (outlier-rejected + smoothed,
// exactly the legacy pass-1 field) → dense refinement seeded by the coarse field. Returns the
// DENSE grid.
func measureTwoLevelField(im *fits.Image, rc10, rcD *refContext) (dxGrid, dyGrid []float64) {
	tgtBlur := blurPlane(im, warpBlur)
	coarseDx, coarseDy := coarseField(im, tgtBlur, rc10, true)
	baseDx, baseDy := resampleFieldTo(coarseDx, coarseDy, rc10.gridN, rcD, im.W, im.H)
	return refineSeededField(tgtBlur, rcD, baseDx, baseDy)
}

// resampleFieldTo bilinearly samples an n×n field at another context's AP centres — the seed
// baseline of a dense refinement.
func resampleFieldTo(dx, dy []float64, n int, rc *refContext, w, h int) (baseDx, baseDy []float64) {
	baseDx = make([]float64, len(rc.cx))
	baseDy = make([]float64, len(rc.cy))
	for k := range rc.cx {
		gu := rc.cx[k]*float64(n)/float64(w) - 0.5
		gv := rc.cy[k]*float64(n)/float64(h) - 0.5
		baseDx[k] = sampleGrid(dx, gu, gv, n)
		baseDy[k] = sampleGrid(dy, gu, gv, n)
	}
	return baseDx, baseDy
}

// refineSeededField refines each structured on-disk AP within ±apDenseMaxShift of its baseline
// value, resets mislocks back to the baseline (rejectToBaseline) and smooths the result — the
// dense refinement shared by the double-stack pass and the two-level pass-1 field.
func refineSeededField(tgtBlur *fits.Image, rc *refContext, baseDx, baseDy []float64) (dxGrid, dyGrid []float64) {
	dxGrid = append([]float64(nil), baseDx...)
	dyGrid = append([]float64(nil), baseDy...)
	for k := range rc.cx {
		if !rc.onDisk[k] {
			continue
		}
		dxGrid[k], dyGrid[k] = comet.AlignSeeded(rc.blur, tgtBlur,
			comet.Point{X: rc.cx[k], Y: rc.cy[k]}, rc.apRadius, apDenseMaxShift, 0, baseDx[k], baseDy[k])
	}
	rejectToBaseline(dxGrid, dyGrid, rc.onDisk, baseDx, baseDy)
	smoothGrid(dxGrid)
	smoothGrid(dyGrid)
	return dxGrid, dyGrid
}
