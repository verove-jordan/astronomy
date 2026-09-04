package planetary

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Double-stack reference (AutoStakkert's "Double Stack Reference"). Pass 1 aligns every kept frame
// onto the canonical geometry derived from the single sharpest frame (two-level dense field +
// median-field correction, see densefield.go / canonical.go). Pass 2 re-registers the ORIGINAL
// frames onto the pass-1 stacked master — a low-noise, distortion-averaged reference — on the
// dense AP grid, each AP seeded by the frame's pass-1 field so the local ZNCC search stays tiny.
// Every original frame is still resampled exactly once (pass-1 warps are discarded; only their
// fields seed).
const (
	apDenseCellPx  = 120 // target pass-2 AP cell size (px) → 29×29 on a 3520 px frame
	apDenseGridMax = 32
	// apAlignPointsGridMax is the explicit align_points per-axis ceiling (48 → 2304 points). At 48
	// a 3520 px min-dim frame has ≈73 px cells — still ≥ the dense pass's 2% (~70 px) ZNCC patch
	// half-size (apDensePatchPct), so every AP window keeps enough support to correlate. The AUTO
	// formula keeps its 32 cap: raising it would silently ~2.25× the AP-measure cost of every
	// large-sensor run, so the explicit knob is the user's consent to that cost.
	apAlignPointsGridMax = 48
	apDensePatchPct      = 2  // dense-AP correlation window half-size, % of the smaller axis
	apDenseMaxShift      = 3  // local search around the pass-1 seed (px)
	doubleStackMin       = 12 // fewer kept frames: a second pass adds noise, not sharpness
	// A dense-grid window on featureless surface (flat maria, dark side) cannot correlate — a
	// degenerate ZNCC would bend the field coherently enough to survive the median outlier gate.
	// Such APs are vetoed up-front (scale-invariant Laplacian variance of the REFERENCE window)
	// and ride the seeded baseline instead — the same structure threshold idea as PSS's AP mesh.
	apDenseMinStructure = 1e-6
)

// denseGridN picks the pass-2 grid density for a frame size (never coarser than pass 1's grid).
func denseGridN(w, h int) int {
	n := min(w, h) / apDenseCellPx
	if n < apGridN {
		n = apGridN
	}
	if n > apDenseGridMax {
		n = apDenseGridMax
	}
	return n
}

// alignPointsGridN maps the align_points knob — a TOTAL grid point count, AutoStakkert-style — to
// the per-axis dense-grid density: clamp(round(√total), apGridN, apAlignPointsGridMax).
func alignPointsGridN(total int) int {
	n := int(math.Round(math.Sqrt(float64(total))))
	if n < apGridN {
		n = apGridN
	}
	if n > apAlignPointsGridMax {
		n = apAlignPointsGridMax
	}
	return n
}

// denseGridNFor resolves the dense grid density: the explicit align_points override when set (>0),
// else the frame-size auto formula (denseGridN — unchanged, still capped at apDenseGridMax).
func denseGridNFor(w, h, alignPoints int) int {
	if alignPoints > 0 {
		return alignPointsGridN(alignPoints)
	}
	return denseGridN(w, h)
}

// SnapAlignPoints normalizes the align_points knob to its effective total: ≤0 → 0 (auto), else N²
// with N = alignPointsGridN(v) — the persisted value always names the real grid (mirrors
// SnapDrizzle). Exported for the param patch and the CLI flags.
func SnapAlignPoints(v int) int {
	if v <= 0 {
		return 0
	}
	n := alignPointsGridN(v)
	return n * n
}

// runDoubleStack executes the pass-2 re-alignment + re-stack for one channel and atomically
// replaces the pass-1 master on success. Soft-fail by contract: any problem keeps the single-pass
// master and returns the reason as a note — a run never fails because of the second pass.
func runDoubleStack(ctx context.Context, keptPaths []string, pass1 alignResult, chDir, alignDir,
	masterPath, filter string, opts Options, prog *runProgress, alignSpan, stackSpan float64,
	onProgress func(siril.Progress)) (string, int) {
	prog.phase(alignSpan)
	report(onProgress, "double-stack aligning "+channelLabel(filter))
	master0, err := fits.ReadImage(masterPath + ".fits")
	if err != nil {
		return "double-stack skipped (kept the single-pass master): " + err.Error(), 0
	}
	srcPaths := make([]string, len(pass1.srcIdx))
	for j, si := range pass1.srcIdx {
		srcPaths[j] = keptPaths[si]
	}
	_ = os.RemoveAll(alignDir) // free the pass-1 aligned frames; pass 2 re-reads the originals
	align2 := filepath.Join(chDir, "aligned2")
	if err := fsutil.EnsureDir(align2); err != nil {
		return "double-stack skipped (kept the single-pass master): " + err.Error(), 0
	}
	// Under drizzle the pass-1 master is scaled but the originals are native: the ZNCC
	// measurement reference must be brought back to the frames' EXACT native raster
	// (comet.AlignSeeded silently returns the seed on a width mismatch).
	scale := SnapDrizzle(opts.DrizzleScale)
	measureRef := master0
	if scale != 1 {
		first, rerr := fits.ReadImage(srcPaths[0])
		if rerr != nil {
			return "double-stack skipped (kept the single-pass master): " + rerr.Error(), 0
		}
		measureRef = resamplePlaneTo(master0, first.W, first.H)
	}
	// Resolve the dense grid once (respecting an align_points override) so the note and the warp
	// can never disagree.
	gridN := denseGridNFor(measureRef.W, measureRef.H, opts.AlignPoints)
	pass2, err := warpToMaster(ctx, measureRef, srcPaths, pass1.dxFields, pass1.dyFields, align2, "d", scale, gridN, prog.tick)
	if err != nil || len(pass2.paths) == 0 {
		reason := "no frames survived the re-alignment"
		if err != nil {
			reason = err.Error()
		}
		return "double-stack skipped (kept the single-pass master): " + reason, 0
	}
	// The pass-2 canvas must land EXACTLY on the pass-1 master's raster: channels soft-fail this
	// pass independently, so any drift here would leave the R/G/B trio at mixed dimensions and
	// break the downstream colour smoothing/combine.
	if first, rerr := fits.ReadImage(pass2.paths[0]); rerr != nil || first.W != master0.W || first.H != master0.H {
		reason := "pass-2 canvas differs from the pass-1 master"
		if rerr != nil {
			reason = rerr.Error()
		} else {
			reason = fmt.Sprintf("%s (%dx%d vs %dx%d)", reason, first.W, first.H, master0.W, master0.H)
		}
		return "double-stack skipped (kept the single-pass master): " + reason, 0
	}
	prog.phase(stackSpan)
	report(onProgress, "double-stacking "+channelLabel(filter))
	tmp := masterPath + "_ds" // stack beside, rename over — a failed pass 2 never corrupts pass 1
	if err := stackAligned(ctx, pass2.paths, pass2.cellSharp, opts, tmp, prog.tick); err != nil {
		return "double-stack skipped (kept the single-pass master): " + err.Error(), 0
	}
	if err := os.Rename(tmp+".fits", masterPath+".fits"); err != nil {
		return "double-stack skipped (kept the single-pass master): " + err.Error(), 0
	}
	_ = os.RemoveAll(align2)
	return fmt.Sprintf("double-stack reference alignment (%d frames re-registered onto the pass-1 master, %d×%d AP grid)",
		len(pass2.paths), gridN, gridN), len(pass2.paths)
}

// warpToMaster re-registers the original kept frames onto the pass-1 master with the dense grid.
// measureRef must be at the frames' NATIVE raster; the warp lands on the scale× output grid. gridN
// is the already-resolved dense per-axis density (auto or an align_points override).
func warpToMaster(ctx context.Context, measureRef *fits.Image, srcPaths []string,
	dxSeeds, dySeeds [][]float64, outDir, prefix string, scale float64, gridN int, onFrame func(done, total int)) (alignResult, error) {
	rc := newRefContextN(measureRef, gridN, apDensePatchPct)
	scoreCx, scoreCy := scaleCoords(rc.cx, scale), scaleCoords(rc.cy, scale)
	outSlots := make([]string, len(srcPaths))
	sharpSlots := make([][]float64, len(srcPaths))
	var done atomic.Int64
	err := forEachFrame(ctx, len(srcPaths), planetaryWorkers(), func(i int) error {
		im, rerr := fits.ReadImage(srcPaths[i])
		if rerr != nil {
			return nil // a corrupt frame drops out; the rest still stack
		}
		dxG, dyG := measureSeededField(im, &rc, dxSeeds[i], dySeeds[i])
		aligned := warpByGridScaled(im, dxG, dyG, scale)
		outPath := filepath.Join(outDir, fmt.Sprintf("%s_%05d.fits", prefix, i+1))
		if werr := aligned.WriteFITS(outPath); werr != nil {
			return fmt.Errorf("double-stack: write %s: %w", outPath, werr)
		}
		outSlots[i] = outPath
		sharpSlots[i] = apCellSharpness(aligned, scoreCx, scoreCy, rc.onDisk)
		if onFrame != nil {
			onFrame(int(done.Add(1)), len(srcPaths))
		}
		return nil
	})
	if err != nil {
		return alignResult{}, err
	}
	var res alignResult
	for i, p := range outSlots {
		if p == "" {
			continue
		}
		res.paths = append(res.paths, p)
		res.cellSharp = append(res.cellSharp, sharpSlots[i])
		res.srcIdx = append(res.srcIdx, i)
	}
	return res, nil
}

// vetoFeaturelessAPs drops on-disk alignment points whose REFERENCE window carries no measurable
// structure (flat maria, near-terminator shadow): they cannot correlate, and ride the seeded
// baseline instead. Runs once per channel on the shared context.
func vetoFeaturelessAPs(rc *refContext) {
	p := rc.blur.Pix[0]
	for k, on := range rc.onDisk {
		if !on {
			continue
		}
		x0 := int(rc.cx[k]) - rc.apRadius
		y0 := int(rc.cy[k]) - rc.apRadius
		if regionLaplacianVariance(p, rc.blur.W, rc.blur.H, x0, y0, 2*rc.apRadius, 2*rc.apRadius) < apDenseMinStructure {
			rc.onDisk[k] = false
		}
	}
}

// measureSeededField measures a frame's field onto the dense reference context. The BASELINE of
// the whole grid is the frame's pass-1 field re-sampled on the dense grid (it already carries the
// global drift + the pass-1 local warp; a frame without a field rides the fresh global shift).
// The shared refineSeededField then refines each structured on-disk AP within ±apDenseMaxShift
// of that baseline; mislocks reset back to the BASELINE (not the global) so vetoed/featureless
// regions keep pass 1's proven correction.
func measureSeededField(im *fits.Image, rc *refContext, seedDx, seedDy []float64) (dxGrid, dyGrid []float64) {
	tgtBlur := blurPlane(im, warpBlur)
	gdx, gdy, _ := globalShift(im, tgtBlur, rc, frameSeed{})
	baseDx, baseDy := uniformGrid(gdx, gdy, rc.gridN)
	if seedDx != nil {
		baseDx, baseDy = resampleFieldTo(seedDx, seedDy, gridSize(seedDx), rc, im.W, im.H)
	}
	return refineSeededField(tgtBlur, rc, baseDx, baseDy)
}

// rejectToBaseline resets mislocked on-disk AP shifts back to their own cell's baseline (the
// pass-1 field), preserving pass 1's correction where the dense measurement can't be trusted.
func rejectToBaseline(dxGrid, dyGrid []float64, onDisk []bool, baseDx, baseDy []float64) {
	var xs, ys []float64
	for k, on := range onDisk {
		if on {
			xs = append(xs, dxGrid[k])
			ys = append(ys, dyGrid[k])
		}
	}
	if len(xs) < 3 {
		return
	}
	mx, my := medianOf(xs), medianOf(ys)
	for k, on := range onDisk {
		if !on {
			continue
		}
		if math.Abs(dxGrid[k]-mx) > apOutlierPx || math.Abs(dyGrid[k]-my) > apOutlierPx {
			dxGrid[k], dyGrid[k] = baseDx[k], baseDy[k]
		}
	}
}
