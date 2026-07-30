package planetary

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync/atomic"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// Surface-alignment tuning. The Moon/planets have no stars, so frames are registered by a per-point warp:
// a coarse downsampled ZNCC (large drifts the centroid alone can misjudge under clouds/limb clipping),
// a fine full-res seeded ZNCC, then — per AP — the local field, applied by a single Catmull-Rom
// resample. warpBlur is small — it denoises the correlation without flattening the peak; a heavy blur
// would broaden the peak and spoil the parabolic sub-pixel fit.
const (
	warpBlur        = 1  // pre-blur radius for the ZNCC measurement (px) — denoise, keep the peak sharp
	surfaceMaxShift = 8  // fine full-res residual search around the coarse seed (px)
	alignWinFrac    = 12 // ZNCC window half-size as a percent of the smaller axis (a feature-rich central patch)
	coarseDown      = 4  // downsample factor of the coarse pre-alignment stage
	coarseMaxShift  = 16 // coarse search in DOWNSAMPLED px — ±64 full-res px of drift coverage
)

// refContext is the reference-frame alignment context shared by warpToSharpest (per-frame lucky
// alignment), coRegisterMasters (cross-channel master alignment) and the double-stack pass: the
// reference centroid, its blurred + downsampled planes for the coarse/fine ZNCC, the global
// window/radius, and the AP-grid geometry.
type refContext struct {
	x, y     float64
	blur     *fits.Image
	small    *fits.Image
	gWin     comet.Point
	gRadius  int
	gridN    int
	cx, cy   []float64
	onDisk   []bool
	apRadius int
}

// newRefContext derives the standard (10×10) alignment context from a reference frame once, so a
// batch of frames/masters is measured against the same precomputed reference planes and geometry.
func newRefContext(ref *fits.Image) refContext {
	return newRefContextN(ref, apGridN, apPatchFrac)
}

// newRefContextN is newRefContext with an explicit AP-grid density and correlation-window size —
// the double-stack pass aligns against the pass-1 master with a much denser grid and smaller
// windows. Featureless APs are vetoed at EVERY density: a window without structure returns a
// degenerate ZNCC whose junk shift would contaminate the outlier-rejection median (half a grid
// of junk pulls the median off the true drift), so such points ride the baseline instead.
func newRefContextN(ref *fits.Image, gridN, patchPct int) refContext {
	rx, ry := brightCentroid(ref)
	blur := blurPlane(ref, warpBlur)
	cx, cy := apCenters(ref.W, ref.H, gridN)
	rc := refContext{
		x: rx, y: ry,
		blur:     blur,
		small:    downPlane(blur, coarseDown),
		gWin:     centerPoint(rx, ry), // window over the bright disk, not the (possibly off-centre) frame
		gRadius:  min(ref.W, ref.H) * alignWinFrac / 100,
		gridN:    gridN,
		cx:       cx,
		cy:       cy,
		onDisk:   apDiskMask(ref, cx, cy),
		apRadius: min(ref.W, ref.H) * patchPct / 100,
	}
	vetoFeaturelessAPs(&rc)
	return rc
}

// alignResult is one alignment pass's output: the aligned FITS paths with, index-aligned, each
// frame's per-AP-cell local sharpness (nil when apAlign is off), its measured displacement grids
// and its index into the pass's INPUT path list — the double-stack pass re-reads the originals
// and seeds from the fields. On the canonical path every frame (reference included) carries a
// corrected field; on the legacy few-frame path the reference stays unresampled with a nil field.
type alignResult struct {
	paths     []string
	refPath   string
	cellSharp [][]float64
	dxFields  [][]float64
	dyFields  [][]float64
	srcIdx    []int
	note      string // run-report note (canonical geometry applied / skipped), "" when silent
	gridNote  string // dense AP-grid provenance ("N×N grid, M usable"), "" when apAlign is off
}

// warpToSharpest reads the FITS frames at paths, registers each onto the sharpest one (highest score),
// and writes the aligned 32-bit FITS into outDir as <prefix>_00001.fits, … (numbered by INPUT position;
// a frame that fails to read leaves a numbering gap — harmless, nothing builds a Siril sequence over
// the dir, only the returned path list is consumed). With apAlign and enough frames the pass is
// CANONICAL (see canonical.go): sweep 1 measures every frame's field (coarse downsampled ZNCC → fine
// seeded parabolic ZNCC → per-AP local field), the median field cancels the reference's own frozen
// seeing warp out of all of them, and sweep 2 resamples every frame — the reference included, by −C —
// onto the distortion-free mean geometry. Below the floor (or without apAlign) the legacy path runs:
// measure-and-warp in one sweep, reference written unresampled (at scale 1). Either way each frame
// is resampled EXACTLY ONCE with a Catmull-Rom bicubic warp of its ORIGINAL pixels — onto a
// scale× output raster when drizzling (fields stay in native units; see drizzle.go). onFrame
// (nil-safe, may be called concurrently) ticks per aligned frame for live progress.
func warpToSharpest(ctx context.Context, paths []string, scores []float64, outDir, prefix string, apAlign bool,
	scale float64, alignPoints int, onFrame func(done, total int)) (alignResult, error) {
	if len(paths) == 0 {
		return alignResult{}, nil
	}
	refIdx := argmax(scores)
	ref, err := fits.ReadImage(paths[refIdx])
	if err != nil {
		return alignResult{}, fmt.Errorf("warp: read reference %s: %w", paths[refIdx], err)
	}
	rc := newRefContext(ref)
	rcD := rc // with apAlign the dense two-level grid measures/scores; otherwise unused
	if apAlign {
		rcD = denseContextFrom(ref, rc, alignPoints)
	}
	scoreCx, scoreCy := scaleCoords(rcD.cx, scale), scaleCoords(rcD.cy, scale)

	canonical := apAlign && len(paths) >= canonicalMin
	dxSlots := make([][]float64, len(paths))
	dySlots := make([][]float64, len(paths))
	if canonical {
		if dxSlots, dySlots, err = measureAllFields(ctx, paths, refIdx, &rc, &rcD); err != nil {
			return alignResult{}, err
		}
		canonicalizeFields(dxSlots, dySlots, refIdx, rcD.onDisk)
	}

	// Slot-indexed results: frame i owns slot i (pre-assigned output name), so workers never contend;
	// read failures leave nil slots, compacted below with every parallel array kept index-aligned.
	outSlots := make([]string, len(paths))
	sharpSlots := make([][]float64, len(paths))
	var done atomic.Int64
	err = forEachFrame(ctx, len(paths), planetaryWorkers(), func(i int) error {
		if canonical && dxSlots[i] == nil {
			return nil // unreadable in sweep 1 — keep it out of the stack entirely
		}
		im, rerr := fits.ReadImage(paths[i])
		if rerr != nil {
			return nil // a corrupt frame drops out; the rest still stack
		}
		aligned := im
		switch {
		case canonical:
			aligned = warpByGridScaled(im, dxSlots[i], dySlots[i], scale)
		case i != refIdx:
			var dxG, dyG []float64
			if apAlign {
				dxG, dyG = measureTwoLevelField(im, &rc, &rcD)
			} else {
				dxG, dyG = measureFrameField(im, &rc, false)
			}
			dxSlots[i], dySlots[i] = dxG, dyG
			aligned = warpByGridScaled(im, dxG, dyG, scale)
		case scale != 1: // legacy-path reference on a drizzled run: plain scale-up, no warp
			aligned = resamplePlane(im, scale)
		}
		outPath := filepath.Join(outDir, fmt.Sprintf("%s_%05d.fits", prefix, i+1))
		if werr := aligned.WriteFITS(outPath); werr != nil {
			return fmt.Errorf("warp: write %s: %w", outPath, werr)
		}
		outSlots[i] = outPath
		if apAlign {
			sharpSlots[i] = apCellSharpness(aligned, scoreCx, scoreCy, rcD.onDisk)
		}
		if onFrame != nil {
			onFrame(int(done.Add(1)), len(paths))
		}
		return nil
	})
	if err != nil {
		return alignResult{}, err
	}

	var res alignResult
	if canonical {
		res.note = fmt.Sprintf("canonical reference geometry (median field over %d frames)", len(paths))
	} else if apAlign && len(paths) > 1 {
		res.note = fmt.Sprintf("canonical geometry skipped (%d kept frames < %d)", len(paths), canonicalMin)
	}
	if apAlign {
		usable := 0
		for _, on := range rcD.onDisk {
			if on {
				usable++
			}
		}
		res.gridNote = fmt.Sprintf("alignment points: %d×%d grid, %d usable", rcD.gridN, rcD.gridN, usable)
	}
	for i, p := range outSlots {
		if p == "" {
			continue
		}
		res.paths = append(res.paths, p)
		res.dxFields = append(res.dxFields, dxSlots[i])
		res.dyFields = append(res.dyFields, dySlots[i])
		res.srcIdx = append(res.srcIdx, i)
		if apAlign {
			res.cellSharp = append(res.cellSharp, sharpSlots[i])
		}
		if i == refIdx {
			res.refPath = p
		}
	}
	return res, nil
}

// globalShift measures im's global drift onto rc's reference: a centroid seed, checked by a coarse
// DOWNSAMPLED ZNCC covering ±64 px (clouds or a clipped limb can bias the centroid), then refined
// by one full-res seeded parabolic ZNCC. tgtBlur is im's warpBlur plane (measured once per frame).
func globalShift(im, tgtBlur *fits.Image, rc *refContext) (gdx, gdy float64) {
	icx, icy := brightCentroid(im)
	seedX, seedY := rc.x-icx, rc.y-icy
	// Coarse stage: verify/correct the centroid seed on 4x-downsampled planes (±coarseMaxShift
	// small-px ≈ ±64 full-res px). A correlation is only trustworthy when its window is at least as
	// large as its search range — on smaller frames the centroid seed + fine stage cover the drift,
	// so the coarse stage is skipped rather than risking a huge mislocked seed.
	fineSeedX, fineSeedY := seedX, seedY
	if smallRadius := rc.gRadius / coarseDown; smallRadius >= coarseMaxShift && rc.small.W < rc.blur.W {
		tgtSmall := downPlane(tgtBlur, coarseDown)
		smallWin := comet.Point{X: rc.gWin.X / coarseDown, Y: rc.gWin.Y / coarseDown}
		cdx, cdy := comet.AlignSeeded(rc.small, tgtSmall, smallWin, smallRadius, coarseMaxShift, 0,
			seedX/coarseDown, seedY/coarseDown)
		fineSeedX, fineSeedY = cdx*coarseDown, cdy*coarseDown
	}
	return comet.AlignSeeded(rc.blur, tgtBlur, rc.gWin, rc.gRadius, surfaceMaxShift, 0, fineSeedX, fineSeedY)
}

// measureFrameField measures im's full displacement field onto rc's reference WITHOUT resampling:
// the global drift is the field's baseline EVERYWHERE, including the dark limb; when apAlign is
// set, on-disk alignment points overwrite it with their absolute local shift before the field is
// smoothed. The two-level estimator (densefield.go) uses this as its coarse stage.
func measureFrameField(im *fits.Image, rc *refContext, apAlign bool) (dxGrid, dyGrid []float64) {
	return coarseField(im, blurPlane(im, warpBlur), rc, apAlign)
}

// coarseField is measureFrameField over a pre-blurred target plane, so the two-level estimator
// measures its blur once and shares it between the coarse and dense stages.
func coarseField(im, tgtBlur *fits.Image, rc *refContext, apAlign bool) (dxGrid, dyGrid []float64) {
	gdx, gdy := globalShift(im, tgtBlur, rc)
	dxGrid, dyGrid = uniformGrid(gdx, gdy, rc.gridN)
	if apAlign {
		measureAPField(rc.blur, tgtBlur, rc.cx, rc.cy, rc.onDisk, rc.apRadius, gdx, gdy, dxGrid, dyGrid)
		rejectAPOutliers(dxGrid, dyGrid, rc.onDisk, gdx, gdy) // a mislocked AP must not bend the field
		smoothGrid(dxGrid)
		smoothGrid(dyGrid)
	}
	return dxGrid, dyGrid
}

// downPlane box-downsamples a 1-plane image by factor f (mean of each f×f block) for the coarse
// alignment stage.
func downPlane(im *fits.Image, f int) *fits.Image {
	w, h := im.W/f, im.H/f
	if w < 8 || h < 8 {
		return im
	}
	out := fits.NewImage(w, h, 1)
	src, dst := im.Pix[0], out.Pix[0]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			for dy := 0; dy < f; dy++ {
				row := (y*f + dy) * im.W
				for dx := 0; dx < f; dx++ {
					s += float64(src[row+x*f+dx])
				}
			}
			dst[y*w+x] = float32(s / float64(f*f))
		}
	}
	return out
}

// brightCentroid is the brightness-weighted center of mass of an image's first plane, weighting each
// pixel by its value above a low-percentile background (≈ sky/limb). For a bright Moon/planet disk on
// dark sky this is a stable, sub-pixel disk centre — biased toward the lit/bright regions but
// CONSISTENTLY across frames, so frame-to-frame differences register the drift correctly.
func brightCentroid(im *fits.Image) (cx, cy float64) {
	p := im.Pix[0]
	w, h := im.W, im.H
	bg := lowPercentile(p, 0.2)
	var sw, sx, sy float64
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			wt := float64(p[row+x]) - bg
			if wt <= 0 {
				continue
			}
			sw += wt
			sx += wt * float64(x)
			sy += wt * float64(y)
		}
	}
	if sw == 0 {
		return float64(w) / 2, float64(h) / 2
	}
	return sx / sw, sy / sw
}

// lowPercentile returns the q-quantile (0..1) of a sampled subset of v (capped for speed).
func lowPercentile(v []float32, q float64) float64 {
	const maxSample = 100000
	step := 1
	if len(v) > maxSample {
		step = len(v) / maxSample
	}
	s := make([]float64, 0, len(v)/step+1)
	for i := 0; i < len(v); i += step {
		s = append(s, float64(v[i]))
	}
	if len(s) == 0 {
		return 0
	}
	sort.Float64s(s)
	idx := int(q * float64(len(s)-1))
	return s[idx]
}

// centerPoint wraps a coordinate as a comet.Point, keeping call sites free of a direct comet import.
func centerPoint(x, y float64) comet.Point { return comet.Point{X: x, Y: y} }

// argmax returns the index of the largest score (0 for an empty slice).
func argmax(scores []float64) int {
	best, bi := -1.0, 0
	for i, s := range scores {
		if i == 0 || s > best {
			best, bi = s, i
		}
	}
	return bi
}
