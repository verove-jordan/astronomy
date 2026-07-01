package planetary

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// Surface-alignment tuning. The Moon/planets have no stars, so frames are registered by a per-point warp:
// a coarse brightness-centroid seed (gross drift) refined by sub-pixel ZNCC on the surface detail, then a
// single Catmull-Rom resample. warpBlur is small — it denoises the correlation without flattening the
// peak; a heavy blur would broaden the peak and spoil the parabolic sub-pixel fit.
const (
	warpBlur        = 1  // pre-blur radius for the ZNCC measurement (px) — denoise, keep the peak sharp
	surfaceMaxShift = 5  // residual search around the centroid seed (px); the centroid does the heavy lifting
	alignWinFrac    = 12 // ZNCC window half-size as a percent of the smaller axis (a feature-rich central patch)
)

// warpToSharpest reads the FITS frames at paths, registers each onto the sharpest one (highest score),
// and writes the aligned 32-bit FITS into outDir as <prefix>_00001.fits, _00002.fits, … (1-based, input
// order). It returns the written paths and the reference frame's written path. Each non-reference frame
// is measured (global centroid + seeded parabolic ZNCC, then — when apAlign — a per-AP local field over
// the lit disk) and resampled EXACTLY ONCE with a Catmull-Rom bicubic warp of its ORIGINAL pixels; the
// reference is written unresampled. Frames that fail to read are skipped (the sequence stays gap-free).
func warpToSharpest(paths []string, scores []float64, outDir, prefix string, apAlign bool) ([]string, string, error) {
	if len(paths) == 0 {
		return nil, "", nil
	}
	refIdx := argmax(scores)
	ref, err := fits.ReadImage(paths[refIdx])
	if err != nil {
		return nil, "", fmt.Errorf("warp: read reference %s: %w", paths[refIdx], err)
	}
	refX, refY := brightCentroid(ref)
	refBlur := blurPlane(ref, warpBlur)
	gWin := centerPoint(refX, refY) // window over the bright disk, not the (possibly off-centre) frame
	gRadius := min(ref.W, ref.H) * alignWinFrac / 100
	cx, cy := apCenters(ref.W, ref.H)
	onDisk := apDiskMask(ref, cx, cy)
	apRadius := min(ref.W, ref.H) * apPatchFrac / 100

	var out []string
	var refPath string
	for _, p := range paths {
		im, rerr := fits.ReadImage(p)
		if rerr != nil {
			continue // a corrupt frame drops out; the rest still stack
		}
		aligned := im
		isRef := p == paths[refIdx]
		if !isRef {
			aligned = warpFrameToRef(im, refBlur, refX, refY, gWin, gRadius, cx, cy, onDisk, apRadius, apAlign)
		}
		outPath := filepath.Join(outDir, fmt.Sprintf("%s_%05d.fits", prefix, len(out)+1))
		if werr := aligned.WriteFITS(outPath); werr != nil {
			return out, refPath, fmt.Errorf("warp: write %s: %w", outPath, werr)
		}
		out = append(out, outPath)
		if isRef {
			refPath = outPath
		}
	}
	return out, refPath, nil
}

// warpFrameToRef measures im's displacement onto the reference and returns a single Catmull-Rom warp of
// im. The global drift (centroid seed refined by one seeded parabolic ZNCC) is the field's baseline
// EVERYWHERE — including the dark limb — and, when apAlign is set, on-disk alignment points overwrite it
// with their absolute local shift before the field is smoothed and applied. One measurement, one resample.
func warpFrameToRef(im, refBlur *fits.Image, refX, refY float64, gWin comet.Point, gRadius int,
	cx, cy []float64, onDisk []bool, apRadius int, apAlign bool) *fits.Image {
	tgtBlur := blurPlane(im, warpBlur)
	icx, icy := brightCentroid(im)
	gdx, gdy := comet.AlignSeeded(refBlur, tgtBlur, gWin, gRadius, surfaceMaxShift, 0, refX-icx, refY-icy)
	dxGrid, dyGrid := uniformGrid(gdx, gdy)
	if apAlign {
		measureAPField(refBlur, tgtBlur, cx, cy, onDisk, apRadius, gdx, gdy, dxGrid, dyGrid)
		smoothGrid(dxGrid)
		smoothGrid(dyGrid)
	}
	return warpByGrid(im, dxGrid, dyGrid)
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
