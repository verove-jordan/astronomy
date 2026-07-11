package planetary

import (
	"fmt"
	"path/filepath"
	"sort"

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
// alignment) and coRegisterMasters (cross-channel master alignment): the reference centroid, its blurred
// + downsampled planes for the coarse/fine ZNCC, the global window/radius, and the AP-grid geometry.
type refContext struct {
	x, y     float64
	blur     *fits.Image
	small    *fits.Image
	gWin     comet.Point
	gRadius  int
	cx, cy   []float64
	onDisk   []bool
	apRadius int
}

// newRefContext derives the alignment context from a reference frame once, so a batch of frames/masters
// is measured against the same precomputed reference planes and AP geometry.
func newRefContext(ref *fits.Image) refContext {
	rx, ry := brightCentroid(ref)
	blur := blurPlane(ref, warpBlur)
	cx, cy := apCenters(ref.W, ref.H)
	return refContext{
		x: rx, y: ry,
		blur:     blur,
		small:    downPlane(blur, coarseDown),
		gWin:     centerPoint(rx, ry), // window over the bright disk, not the (possibly off-centre) frame
		gRadius:  min(ref.W, ref.H) * alignWinFrac / 100,
		cx:       cx,
		cy:       cy,
		onDisk:   apDiskMask(ref, cx, cy),
		apRadius: min(ref.W, ref.H) * apPatchFrac / 100,
	}
}

// warpToSharpest reads the FITS frames at paths, registers each onto the sharpest one (highest score),
// and writes the aligned 32-bit FITS into outDir as <prefix>_00001.fits, _00002.fits, … (1-based, input
// order). It returns the written paths, the reference frame's written path, and each aligned frame's
// per-AP-cell local sharpness (for the multi-point stacking weights; nil when apAlign is off). Each
// non-reference frame is measured (coarse downsampled ZNCC → fine seeded parabolic ZNCC, then — when
// apAlign — a per-AP local field over the lit disk) and resampled EXACTLY ONCE with a Catmull-Rom
// bicubic warp of its ORIGINAL pixels; the reference is written unresampled. Frames that fail to read
// are skipped (the sequence stays gap-free).
func warpToSharpest(paths []string, scores []float64, outDir, prefix string, apAlign bool) ([]string, string, [][]float64, error) {
	if len(paths) == 0 {
		return nil, "", nil, nil
	}
	refIdx := argmax(scores)
	ref, err := fits.ReadImage(paths[refIdx])
	if err != nil {
		return nil, "", nil, fmt.Errorf("warp: read reference %s: %w", paths[refIdx], err)
	}
	rc := newRefContext(ref)

	var out []string
	var refPath string
	var cellSharp [][]float64
	for _, p := range paths {
		im, rerr := fits.ReadImage(p)
		if rerr != nil {
			continue // a corrupt frame drops out; the rest still stack
		}
		aligned := im
		isRef := p == paths[refIdx]
		if !isRef {
			aligned = warpFrameToRef(im, rc.blur, rc.small, rc.x, rc.y, rc.gWin, rc.gRadius, rc.cx, rc.cy, rc.onDisk, rc.apRadius, apAlign)
		}
		outPath := filepath.Join(outDir, fmt.Sprintf("%s_%05d.fits", prefix, len(out)+1))
		if werr := aligned.WriteFITS(outPath); werr != nil {
			return out, refPath, cellSharp, fmt.Errorf("warp: write %s: %w", outPath, werr)
		}
		out = append(out, outPath)
		if apAlign {
			cellSharp = append(cellSharp, apCellSharpness(aligned, rc.cx, rc.cy, rc.onDisk))
		}
		if isRef {
			refPath = outPath
		}
	}
	return out, refPath, cellSharp, nil
}

// warpFrameToRef measures im's displacement onto the reference and returns a single Catmull-Rom warp of
// im. The global drift — a centroid seed, checked by a coarse DOWNSAMPLED ZNCC covering ±64 px (clouds
// or a clipped limb can bias the centroid), then refined by one full-res seeded parabolic ZNCC — is the
// field's baseline EVERYWHERE, including the dark limb; when apAlign is set, on-disk alignment points
// overwrite it with their absolute local shift before the field is smoothed and applied. One
// measurement, one resample.
func warpFrameToRef(im, refBlur, refSmall *fits.Image, refX, refY float64, gWin comet.Point, gRadius int,
	cx, cy []float64, onDisk []bool, apRadius int, apAlign bool) *fits.Image {
	tgtBlur := blurPlane(im, warpBlur)
	icx, icy := brightCentroid(im)
	seedX, seedY := refX-icx, refY-icy
	// Coarse stage: verify/correct the centroid seed on 4x-downsampled planes (±coarseMaxShift
	// small-px ≈ ±64 full-res px). A correlation is only trustworthy when its window is at least as
	// large as its search range — on smaller frames the centroid seed + fine stage cover the drift,
	// so the coarse stage is skipped rather than risking a huge mislocked seed.
	fineSeedX, fineSeedY := seedX, seedY
	if smallRadius := gRadius / coarseDown; smallRadius >= coarseMaxShift && refSmall.W < refBlur.W {
		tgtSmall := downPlane(tgtBlur, coarseDown)
		smallWin := comet.Point{X: gWin.X / coarseDown, Y: gWin.Y / coarseDown}
		cdx, cdy := comet.AlignSeeded(refSmall, tgtSmall, smallWin, smallRadius, coarseMaxShift, 0,
			seedX/coarseDown, seedY/coarseDown)
		fineSeedX, fineSeedY = cdx*coarseDown, cdy*coarseDown
	}
	gdx, gdy := comet.AlignSeeded(refBlur, tgtBlur, gWin, gRadius, surfaceMaxShift, 0, fineSeedX, fineSeedY)
	dxGrid, dyGrid := uniformGrid(gdx, gdy)
	if apAlign {
		measureAPField(refBlur, tgtBlur, cx, cy, onDisk, apRadius, gdx, gdy, dxGrid, dyGrid)
		rejectAPOutliers(dxGrid, dyGrid, onDisk, gdx, gdy) // a mislocked AP must not bend the field
		smoothGrid(dxGrid)
		smoothGrid(dyGrid)
	}
	return warpByGrid(im, dxGrid, dyGrid)
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
