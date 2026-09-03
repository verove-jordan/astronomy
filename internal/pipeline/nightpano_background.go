package pipeline

// nightpano_background.go removes the sky's light-pollution dome from a panorama canvas with
// GraXpert, before the landscape is put back under it.
//
// The order is the point. The polynomial this replaces ran AFTER the foreground was composited: it
// was careful not to SAMPLE the landscape (FlattenOptions.Exclude), but a background model is
// evaluated and subtracted everywhere, so the ground still had a surface fitted to the sky taken off
// it — a shift of whatever the dome happened to be worth at those pixels, applied to pixels that
// were never part of the dome. Running the whole background step while the canvas is still pure sky
// means the landscape is composited into an already-flat sky and is then left exactly as it was.
//
// Two things stop GraXpert being pointed straight at the canvas:
//
//   - A canvas is largely nothing, and GraXpert has no coverage mask. See skypano.FillHoles.
//   - GraXpert normalises by (x−median)/MAD, so a region of perfectly identical pixels — which is
//     exactly what a smooth fill is — divides by zero and yields an all-NaN image while still
//     exiting 0. A tiny deterministic dither breaks the degeneracy. nightscape learned this the same
//     way; see writeDithered there.
//
// What comes back is not used directly. We take the DIFFERENCE between what GraXpert was given and
// what it returned — its background model — and subtract that from the untouched canvas. GraXpert's
// Subtraction correction returns input − model + constant, so the dither cancels out of that
// difference exactly, and the canvas never has the fill or the dither baked into it.

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/skypano"
)

// panoBgMinCoverage is what counts as a real pixel for the background pass — the same absolute cut,
// in units of one panel at full weight, that Flatten and Grade use.
const panoBgMinCoverage = 0.5

// graxpertCanvasBackground subtracts GraXpert's background model from a panorama canvas in place.
// It reports whether it applied; false means the caller should fall back to the polynomial, and a
// human-readable reason has already been warned. It never returns an error: a panorama that kept its
// light-pollution dome is still a panorama.
func graxpertCanvasBackground(ctx context.Context, opts Options, res *Result, img *fits.Image,
	cov []float32, dir, label string) bool {

	if opts.Graxpert == nil {
		return false
	}
	if err := opts.Graxpert.Healthy(ctx); err != nil {
		warnLive(opts, res, fmt.Sprintf("nightpano: %s background — GraXpert unavailable (%v), using the polynomial", label, err))
		return false
	}
	if len(cov) != img.W*img.H || img.C == 0 {
		return false
	}

	mask := make([]float32, len(cov))
	var covered []int
	for i, v := range cov {
		if v >= panoBgMinCoverage {
			mask[i], covered = 1, append(covered, i)
		}
	}
	if len(covered) < 1024 {
		warnLive(opts, res, fmt.Sprintf("nightpano: %s background — too little covered canvas to model", label))
		return false
	}

	// Map the canvas into the range GraXpert's model expects. The percentiles are taken over covered
	// pixels only: over the whole array they would mostly be measuring the empty part of it.
	lo, hi := canvasPct(img, covered, 1), canvasPct(img, covered, 99.99)
	if !(hi > lo) {
		warnLive(opts, res, fmt.Sprintf("nightpano: %s background — degenerate canvas range", label))
		return false
	}
	const inLo, inHi = 0.05, 0.90
	scale := (inHi - inLo) / (hi - lo)
	offset := inLo - lo*scale

	prep := img.Clone()
	for c := 0; c < prep.C; c++ {
		p := prep.Pix[c]
		for i := range p {
			v := float64(p[i])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = lo
			}
			p[i] = float32(math.Min(math.Max(v*scale+offset, 0), 1))
		}
	}
	skypano.FillHoles(prep, mask)
	ditherInPlace(prep, (inHi-inLo)*0.001)

	// Staged, handed over, and removed. These are a full float32 copy of the canvas EACH — half a
	// gigabyte apiece on an arch — and leaving them behind filled the disk and killed a run three
	// canvases later. They are of no use once the model has been read back.
	inPath := filepath.Join(dir, "gxbg_"+label+"_in.fits")
	outPath := filepath.Join(dir, "gxbg_"+label+"_out.fits")
	defer func() {
		_ = os.Remove(inPath)
		_ = os.Remove(outPath)
	}()
	if err := prep.WriteFITS(inPath); err != nil {
		warnLive(opts, res, fmt.Sprintf("nightpano: %s background — could not stage the canvas (%v)", label, err))
		return false
	}
	fwd := func(p graxpert.Progress) { opts.report(Progress{Line: p.Line, Sample: p.Sample}) }
	if err := opts.Graxpert.ExtractBackground(ctx, inPath, outPath, graxpert.BackgroundOptions{}, fwd); err != nil {
		warnLive(opts, res, fmt.Sprintf("nightpano: %s background — GraXpert failed (%v), using the polynomial", label, err))
		return false
	}
	got, err := fits.ReadImage(outPath)
	if err != nil {
		warnLive(opts, res, fmt.Sprintf("nightpano: %s background — GraXpert produced no readable output (%v)", label, err))
		return false
	}
	if got.W != prep.W || got.H != prep.H || got.C != prep.C {
		warnLive(opts, res, fmt.Sprintf("nightpano: %s background — GraXpert changed the canvas size, ignoring it", label))
		return false
	}

	// The model is what GraXpert took out. Smooth it before it is applied: it is a background surface
	// by construction, so anything left in it at pixel scale is not background — it is whatever did
	// not cancel (the dither, GraXpert's own rounding). The residual that removes is reported, because
	// a large one would mean the difference is not a pure subtraction and this reasoning is wrong.
	model := make([][]float32, got.C)
	for c := 0; c < got.C; c++ {
		m := make([]float32, len(got.Pix[c]))
		for i := range m {
			m[i] = prep.Pix[c][i] - got.Pix[c][i]
		}
		if !allFinite(m) {
			warnLive(opts, res, fmt.Sprintf("nightpano: %s background — GraXpert returned non-finite pixels, ignoring it", label))
			return false
		}
		model[c] = m
	}
	radius := max(img.W, img.H) / 256
	var rough float64
	for c := range model {
		smooth := boxBlurPlane(model[c], img.W, img.H, radius)
		rough = math.Max(rough, planeRMSDiff(model[c], smooth))
		model[c] = smooth
	}

	var lvl [3]float64
	for c := 0; c < img.C && c < len(model); c++ {
		p, m := img.Pix[c], model[c]
		var sum float64
		for _, i := range covered {
			sum += float64(m[i])
		}
		lvl[min(c, 2)] = sum / float64(len(covered)) / scale
		for i := range p {
			p[i] -= m[i] / float32(scale)
		}
	}
	opts.report(Progress{Line: fmt.Sprintf(
		"%s background: GraXpert removed a dome of %.5g/%.5g/%.5g (R/G/B mean over the sky); model roughness %.2g",
		label, lvl[0], lvl[1], lvl[2], rough/scale)})
	return true
}

// ditherInPlace adds a tiny deterministic noise so GraXpert's (x−median)/MAD normalisation cannot
// divide by zero on the perfectly flat region the hole fill creates. Deterministic so two runs of
// the same data still agree; it cancels out of the model difference regardless.
func ditherInPlace(im *fits.Image, sigma float64) {
	var s uint32 = 0x9e3779b9 // xorshift32
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		for i := range p {
			s ^= s << 13
			s ^= s >> 17
			s ^= s << 5
			p[i] += float32((float64(s)/4294967295.0*2 - 1) * sigma)
		}
	}
}

// canvasPct is a percentile over the given pixels of every channel pooled.
func canvasPct(im *fits.Image, idx []int, pct float64) float64 {
	buf := make([]float32, 0, len(idx)*im.C)
	for c := 0; c < im.C; c++ {
		for _, i := range idx {
			if v := im.Pix[c][i]; !math.IsNaN(float64(v)) {
				buf = append(buf, v)
			}
		}
	}
	if len(buf) == 0 {
		return 0
	}
	sort.Slice(buf, func(a, b int) bool { return buf[a] < buf[b] })
	return float64(buf[int(math.Min(math.Max(pct, 0), 100)/100*float64(len(buf)-1))])
}

func allFinite(p []float32) bool {
	for _, v := range p {
		if v != v || v > math.MaxFloat32 || v < -math.MaxFloat32 {
			return false
		}
	}
	return true
}

// boxBlurPlane is a two-pass separable box blur with running sums — O(n) in the number of pixels
// rather than in the radius, which matters on a canvas this size.
func boxBlurPlane(p []float32, w, h, radius int) []float32 {
	if radius < 1 {
		out := make([]float32, len(p))
		copy(out, p)
		return out
	}
	tmp := make([]float32, len(p))
	out := make([]float32, len(p))
	for y := 0; y < h; y++ {
		row := p[y*w : (y+1)*w]
		var sum float64
		for x := 0; x <= radius && x < w; x++ {
			sum += float64(row[x])
		}
		n := math.Min(float64(radius+1), float64(w))
		for x := 0; x < w; x++ {
			tmp[y*w+x] = float32(sum / n)
			if add := x + radius + 1; add < w {
				sum += float64(row[add])
				n++
			}
			if drop := x - radius; drop >= 0 {
				sum -= float64(row[drop])
				n--
			}
		}
	}
	for x := 0; x < w; x++ {
		var sum float64
		for y := 0; y <= radius && y < h; y++ {
			sum += float64(tmp[y*w+x])
		}
		n := math.Min(float64(radius+1), float64(h))
		for y := 0; y < h; y++ {
			out[y*w+x] = float32(sum / n)
			if add := y + radius + 1; add < h {
				sum += float64(tmp[add*w+x])
				n++
			}
			if drop := y - radius; drop >= 0 {
				sum -= float64(tmp[drop*w+x])
				n--
			}
		}
	}
	return out
}

func planeRMSDiff(a, b []float32) float64 {
	if len(a) == 0 {
		return 0
	}
	var s float64
	for i := range a {
		d := float64(a[i] - b[i])
		s += d * d
	}
	return math.Sqrt(s / float64(len(a)))
}
