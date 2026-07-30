package pipeline

import (
	"fmt"
	"math"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// A mosaic master carries zero margins (regions no session ever covered). Background models
// (GraXpert, subsky planes) and the stretch must never see those zeros — they would drag the sky
// model toward black and step at the data edge. The fill replaces never-covered pixels with a
// NORMALIZED-CONVOLUTION extrapolation of the covered sky (blur(v·cov)/blur(cov) — the nightscape
// drift-edge recipe) plus matched Gaussian grain, so the margins read as plain sky until the
// final crop (MosaicFill "crop", default) discards them — or stay in the export ("fill").
const (
	// mosaicFillFloorPct is the dark-sky floor percentile of the covered pixels.
	mosaicFillFloorPct = 15
	// mosaicFillSigmaFrac sets the extrapolation blur radius as a fraction of the long side.
	mosaicFillSigmaFrac = 1.0 / 6.0
)

// FillRecord is one channel's mosaic-fill outcome (run.json provenance).
type FillRecord struct {
	FilledFrac float64 `json:"filled_frac"`
	NoiseSigma float64 `json:"noise_sigma,omitempty"`
	MaskPNG    string  `json:"mask_png,omitempty"`
	Applied    bool    `json:"applied"`
}

// coveredPixelMask upsamples the grid's any-coverage cells to per-pixel, with the UNCOVERED region
// dilated by one cell so partially-black edge cells never survive as black fringes.
func coveredPixelMask(g *coverageGrid, w, h int) []bool {
	cells := make([]bool, len(g.Counts))
	for i, c := range g.Counts {
		cells[i] = c > 0
	}
	uncovered := make([]bool, len(cells))
	for i, c := range cells {
		uncovered[i] = !c
	}
	uncovered = imgops.BinaryDilation(uncovered, g.W, g.H, 1)
	out := make([]bool, w*h)
	for y := 0; y < h; y++ {
		gy := min(y/g.Scale, g.H-1)
		for x := 0; x < w; x++ {
			out[y*w+x] = !uncovered[gy*g.W+min(x/g.Scale, g.W-1)]
		}
	}
	return out
}

// fillNoCoverage replaces im's uncovered pixels in place with extrapolated sky + matched grain.
// Deterministic under a fixed seed. Returns the filled fraction and the grain sigma used.
func fillNoCoverage(im *fits.Image, covered []bool, seed uint64) (float64, float64) {
	if len(covered) != im.W*im.H {
		return 0, 0
	}
	uncovered := 0
	for _, c := range covered {
		if !c {
			uncovered++
		}
	}
	if uncovered == 0 {
		return 0, 0
	}
	sigmaPx := mosaicFillSigmaFrac * float64(max(im.W, im.H))
	rng := &boxMuller{s: seed | 1}
	var noiseSigma float64
	for c := range im.Pix {
		noiseSigma = fillPlane(im.Pix[c], im.W, im.H, covered, sigmaPx, rng)
	}
	return float64(uncovered) / float64(len(covered)), noiseSigma
}

// fillPlane fills one plane's uncovered pixels: normalized-convolution extrapolation with a
// dark-sky floor, plus Gaussian grain matched to the covered sky's MAD sigma.
func fillPlane(p []float32, w, h int, covered []bool, sigmaPx float64, rng *boxMuller) float64 {
	vals := make([]float32, len(p))
	cov := make([]float32, len(p))
	var sample []float32
	for i, c := range covered {
		if c {
			vals[i] = p[i]
			cov[i] = 1
			if i%97 == 0 {
				sample = append(sample, p[i])
			}
		}
	}
	blurV := imgops.GaussianBlur(vals, w, h, sigmaPx)
	blurC := imgops.GaussianBlur(cov, w, h, sigmaPx)
	floor := imgops.Percentile(sample, mosaicFillFloorPct)
	sigma := 1.4826 * madOf(sample)
	for i, c := range covered {
		if c {
			continue
		}
		v := floor
		if blurC[i] > 1e-6 {
			if ext := float64(blurV[i] / blurC[i]); ext > floor {
				v = ext
			}
		}
		p[i] = float32(v + sigma*rng.next())
	}
	return sigma
}

// madOf is the median absolute deviation of the sample.
func madOf(sample []float32) float64 {
	if len(sample) == 0 {
		return 0
	}
	med := imgops.Percentile(sample, 50)
	dev := make([]float32, len(sample))
	for i, v := range sample {
		dev[i] = float32(math.Abs(float64(v) - med))
	}
	return imgops.Percentile(dev, 50)
}

// boxMuller is a tiny deterministic Gaussian source (no math/rand — reproducible run to run).
type boxMuller struct {
	s     uint64
	spare float64
	has   bool
}

func (b *boxMuller) uniform() float64 {
	b.s = b.s*6364136223846793005 + 1442695040888963407
	return (float64(b.s>>11) + 1) / float64(1<<53)
}

func (b *boxMuller) next() float64 {
	if b.has {
		b.has = false
		return b.spare
	}
	u, v := b.uniform(), b.uniform()
	r := math.Sqrt(-2 * math.Log(u))
	b.spare = r * math.Sin(2*math.Pi*v)
	b.has = true
	return r * math.Cos(2*math.Pi*v)
}

// fillMosaicMargins fills the never-covered margins of a mosaic channel master in place (before
// any background model sees them) and records the outcome. Soft-fail: errors leave the master
// untouched with a note.
func fillMosaicMargins(opts Options, ch *ChannelResult, outDir, filter string) {
	if ch.coverage == nil || ch.OutputPath == "" {
		return
	}
	im, err := fits.ReadImage(ch.OutputPath)
	if err != nil {
		ch.Selection.Notes = append(ch.Selection.Notes, "mosaic fill skipped: "+err.Error())
		return
	}
	covered := coveredPixelMask(ch.coverage, im.W, im.H)
	frac, sigma := fillNoCoverage(im, covered, fillSeed(filter, im.W, im.H))
	if frac == 0 {
		return
	}
	if err := im.OverwriteData(ch.OutputPath); err != nil {
		ch.Selection.Notes = append(ch.Selection.Notes, "mosaic fill not persisted: "+err.Error())
		return
	}
	rec := &FillRecord{FilledFrac: frac, NoiseSigma: sigma, Applied: true}
	maskPath := filepath.Join(outDir, "fill_"+filterTag(filter)+".png")
	if err := writeCoverageMaskPNG(ch.coverage, maskPath); err == nil {
		rec.MaskPNG = maskPath
	}
	ch.MosaicFill = rec
	opts.report(Progress{Line: fmt.Sprintf("· mosaic %s: %.0f%% of the union filled with extrapolated sky (grain σ %.4g)",
		filter, frac*100, sigma)})
}

// fillSeed derives a stable per-channel seed so reruns reproduce the same grain.
func fillSeed(filter string, w, h int) uint64 {
	s := uint64(1469598103934665603)
	for _, r := range filter {
		s = (s ^ uint64(r)) * 1099511628211
	}
	return s ^ uint64(w)<<32 ^ uint64(h)
}
