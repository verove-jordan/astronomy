package inspect

// rawstill.go classifies camera-raw stills (iPhone DNG/HEIC, DSLR raws) that carry no FITS header, so
// a folder of phone darks/bias/flats is recognized as calibration instead of being force-labeled as
// lights. Classification is tiered, cheapest first:
//
//  1. filename/folder tokens — a `darks/`, `bias/`/`offset/` or `flats/` folder (or a Dark_/Bias_
//     filename) is authoritative and free (reuses parseFilenameMeta → typeFromToken/typeFromDirs);
//  2. pixel statistics — for a loose, unlabeled mixed pile, develop a downscaled TIFF per frame and
//     run the shared classifyByStats over the batch (so a starless dark is told from a light by the
//     co-exposed evidence, and a bias from a flat by brightness).
//
// EXIF (ISO/exposure/model/dimensions) is read for every frame regardless. Anything left Unknown
// finalizes as a Light — the pre-existing behavior — so a pure-lights folder is unchanged and never
// pays the develop cost (and a non-macOS host with no `sips` degrades to tokens-or-Light, never a
// crash).

import (
	"context"
	"image"
	_ "image/png" // register the PNG decoder for image.Decode
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/rawconv"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

const (
	rawSampleMax   = 512              // downscale-develop target (longest side) for cheap classification stats
	rawStatWorkers = 4                // bounded `sips` workers so a big mixed pile classifies without a CPU storm
	sipsTimeout    = 20 * time.Second // per-frame develop timeout
)

// ClassifyRawStills classifies raw still paths into Frames carrying Type/ISO/exposure/model/dims,
// preserving input order. It is used both by the directory scan (scanFrames) and at process time (the
// milkyway pipeline) to separate lights from dark/flat/bias calibration frames.
func ClassifyRawStills(ctx context.Context, paths []string) []*Frame {
	frames := make([]*Frame, len(paths))
	for i, p := range paths {
		frames[i] = classifyRawTokens(p)
	}
	classifyRawByStats(ctx, frames)
	finalizeRawTypes(frames)
	return frames
}

// classifyRawTokens builds a Frame from EXIF metadata plus filename/folder type tokens. Type stays
// Unknown when the name/folders don't resolve it, deferring to the pixel-stats pass.
func classifyRawTokens(path string) *Frame {
	fr := &Frame{Path: path, Type: Unknown, ClassSource: SourceExtension}
	applyRawMeta(fr, rawmeta.Read(path))
	if t := parseFilenameMeta(path).Type; t != Unknown {
		fr.Type, fr.ClassSource = t, SourceFilename
	}
	return fr
}

// applyRawMeta copies EXIF metadata onto a frame: ISO, exposure, camera model (into Instrument), and
// pixel dimensions.
func applyRawMeta(fr *Frame, m rawmeta.Meta) {
	fr.ISO = m.ISO
	if m.HasExposure {
		fr.ExposureMs = m.ExposureMs
	}
	fr.Instrument = m.CameraModel
	fr.Width, fr.Height = m.Width, m.Height
}

// finalizeRawTypes defaults every still-Unknown frame to a Light and normalizes the filter: lights are
// RGB one-shot-color, calibration frames carry no filter.
func finalizeRawTypes(frames []*Frame) {
	for _, fr := range frames {
		if fr.Type == Unknown {
			fr.Type = Light
		}
		if isCalibration(fr.Type) {
			fr.Filter = ""
		} else {
			fr.Filter = "RGB"
		}
	}
}

// classifyRawByStats resolves the still-Unknown frames by developing a downscaled TIFF for each,
// summarizing its pixel curve, and running the shared classifyByStats over the readable subset (it
// needs the whole batch to find the dark floor and the co-exposed "starry" exposures). Soft-fail: a
// frame that can't be developed (no `sips`, decode error) is left Unknown and finalized as a Light.
func classifyRawByStats(ctx context.Context, frames []*Frame) {
	var idx []int
	for i, fr := range frames {
		if fr.Type == Unknown {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	stats := make([]frameStat, len(idx))
	ok := make([]bool, len(idx))
	sampleRawStats(ctx, frames, idx, stats, ok)

	var batch []frameStat
	var batchIdx []int
	for k := range idx {
		if ok[k] {
			batch = append(batch, stats[k])
			batchIdx = append(batchIdx, idx[k])
		}
	}
	if len(batch) == 0 {
		return
	}
	for k, t := range classifyByStats(batch) {
		frames[batchIdx[k]].Type = t
		frames[batchIdx[k]].ClassSource = SourceHeuristic
	}
}

// sampleRawStats develops a downscaled TIFF for each frame in idx (bounded parallelism) and fills its
// frameStat; ok[k] reports whether the stats for idx[k] were computed.
func sampleRawStats(ctx context.Context, frames []*Frame, idx []int, stats []frameStat, ok []bool) {
	sem := make(chan struct{}, rawStatWorkers)
	var wg sync.WaitGroup
	for k, i := range idx {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(k, i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if s, err := sampleRawStat(ctx, frames[i].Path, frames[i].ExposureMs); err == nil {
				stats[k], ok[k] = s, true
			}
		}(k, i)
	}
	wg.Wait()
}

// sampleRawStat develops a small PNG of path via macOS `sips` and summarizes its luminance curve into
// a frameStat (median/MAD/bright-fraction/peaks), mirroring fits.Stats so classifyByStats can treat a
// developed phone frame like a FITS frame. exposureMs is the frame's EXIF exposure.
func sampleRawStat(ctx context.Context, path string, exposureMs int64) (frameStat, error) {
	dir, err := os.MkdirTemp("", "rawstat")
	if err != nil {
		return frameStat{}, err
	}
	defer os.RemoveAll(dir)
	dst := filepath.Join(dir, "sample.png")
	cctx, cancel := context.WithTimeout(ctx, sipsTimeout)
	defer cancel()
	if err := rawconv.Thumbnail(cctx, path, dst, rawSampleMax); err != nil {
		return frameStat{}, err
	}
	vals, err := lumaSample(dst)
	if err != nil {
		return frameStat{}, err
	}
	return statFromLuma(exposureMs, vals), nil
}

// lumaSample decodes a developed image and returns its per-pixel Rec.601 luminance in [0,1], row-major
// (so countLumaPeaks sees a spatially-ordered run, matching fits.Stats).
func lumaSample(path string) ([]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	vals := make([]float64, 0, b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA() // each 0..65535
			vals = append(vals, (0.299*float64(r)+0.587*float64(g)+0.114*float64(bl))/65535.0)
		}
	}
	return vals, nil
}

// statFromLuma summarizes a luminance sample into a frameStat, mirroring fits.summarize (median, MAD,
// bright-fraction above median+3·MAD, and peak count above median+6·MAD).
func statFromLuma(exposureMs int64, vals []float64) frameStat {
	if len(vals) == 0 {
		return frameStat{exposureMs: exposureMs}
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]

	devs := make([]float64, len(sorted))
	for i, v := range sorted {
		devs[i] = math.Abs(v - median)
	}
	sort.Float64s(devs)
	mad := devs[len(devs)/2]

	bright := 0
	thresh := median + 3*mad
	for _, v := range vals {
		if v > thresh {
			bright++
		}
	}
	return frameStat{
		exposureMs: exposureMs,
		median:     median,
		mad:        mad,
		brightFrac: float64(bright) / float64(len(vals)),
		peaks:      countLumaPeaks(vals, median+6*mad),
		hasStats:   true,
	}
}

// countLumaPeaks counts local maxima above thresh in the spatially-ordered sample — a cheap proxy for
// star/hot-pixel point sources, mirroring fits.Stats (a star field yields many; a dark a few).
func countLumaPeaks(vals []float64, thresh float64) int {
	peaks := 0
	for i := 1; i < len(vals)-1; i++ {
		if vals[i] > thresh && vals[i] > vals[i-1] && vals[i] >= vals[i+1] {
			peaks++
		}
	}
	return peaks
}
