package nightscape

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// enhanceSky runs the optional host-tool enhancements on the LINEAR sky stack, in place, via a FITS
// round-trip: a plate-solve + SPCC colour calibration when an OSC sensor is configured (for natural star
// colour) and a GraXpert chroma denoise. Both are soft-fail — a nil/absent runner, a missing sensor, or
// a bad output leaves the in-memory sky untouched. It does NOT flatten the gradient or neutralise the
// background any more: that is done by the mask-aware removeSkyGradient (foreground-excluded) and the
// per-channel black-clip in autoStretch, neither of which is biased by the dark foreground the way
// GraXpert's whole-frame background model was. Must run on linear data, before the auto-stretch.
func enhanceSky(ctx context.Context, sky *fits.Image, o Options, res *Result) {
	useGrax := o.Graxpert != nil && o.Graxpert.Available(ctx) == nil
	useSPCC := o.Siril != nil && o.ColorCalibration && o.Spcc.OSCSensor != ""
	if !useGrax && !useSPCC {
		return
	}
	dir := o.WorkDir
	if dir == "" {
		dir = o.OutDir
	}
	const base = "sky_linear"
	path := filepath.Join(dir, base+".fits")

	var werr error
	if useGrax {
		werr = writeDithered(sky, path) // tiny noise so GraXpert's MAD normalization can't divide by zero
	} else {
		werr = sky.WriteFITS(path)
	}
	if werr != nil {
		res.note("sky enhance: write FITS: " + werr.Error())
		return
	}
	if useSPCC {
		res.note(colorCalibrateSky(ctx, o, dir, base, sky.W)) // SPCC only; no neutralize fallback
	}
	if useGrax {
		res.note(graxpertInPlace(ctx, o.Graxpert, path, true, o.OnProgress)) // chroma denoise
	}
	out, err := fits.ReadImage(path)
	switch {
	case err != nil:
		res.note("sky enhance: read back FITS: " + err.Error())
	case out.W != sky.W || out.H != sky.H || out.C != sky.C:
		res.note("sky enhance: tool changed image dimensions; kept the original sky")
	case hasNonFinite(out):
		// GraXpert can emit an all-NaN image yet exit 0 (its (x−median)/MAD normalization divides by zero
		// on a degenerate histogram); reject it and keep the original sky.
		res.note("sky enhance: tool produced non-finite pixels; kept the original sky")
	default:
		*sky = *out
	}
}

// writeDithered writes the sky to a FITS with a tiny deterministic dither added (σ ≈ 0.1 % of the P99
// level), without mutating the in-memory sky. The dither breaks the degenerate all-identical histogram
// of the foreground-fill / dark-sky region so GraXpert's (x−median)/MAD normalization cannot divide by
// zero (which otherwise silently yields an all-NaN image). The noise is ~0.1 % of signal and the
// denoise pass removes it.
func writeDithered(sky *fits.Image, path string) error {
	d := sky.Clone()
	sigma := float64(percentile(subsample(d.Pix[0], 200000), 99)) * 0.001
	if sigma < 1e-6 {
		sigma = 1e-6
	}
	var s uint32 = 0x9e3779b9 // deterministic xorshift32 (reproducible runs; no Math.rand)
	for c := 0; c < d.C; c++ {
		p := d.Pix[c]
		for i := range p {
			s ^= s << 13
			s ^= s >> 17
			s ^= s << 5
			p[i] += float32((float64(s)/4294967295.0*2 - 1) * sigma)
		}
	}
	return d.WriteFITS(path)
}

// hasNonFinite reports whether any pixel is NaN or ±Inf (see writeDithered — GraXpert can emit such an
// image while still exiting 0).
func hasNonFinite(im *fits.Image) bool {
	for c := 0; c < im.C; c++ {
		for _, v := range im.Pix[c] {
			if v != v || v > math.MaxFloat32 || v < -math.MaxFloat32 {
				return true
			}
		}
	}
	return false
}

// colorCalibrateSky attempts plate-solve + SPCC on the sky FITS (dir/base.fits) for natural star colour.
// The caller only invokes it when an OSC sensor is configured. RemoveGreen is false on purpose: the
// neutralization fallback is the foreground-biased degree-1 subsky we are deliberately retiring for
// nightscapes (flattening is now the mask-aware removeSkyGradient + per-channel black-clip), so on an
// SPCC miss the sky is simply left as the Go flatten produced it. widthPx derives the plate scale from
// the EXIF focal length.
func colorCalibrateSky(ctx context.Context, o Options, dir, base string, widthPx int) string {
	solve := deriveSolve(o.Solve, o.Focal35mm, widthPx)
	note, err := postprocess.ColorCalibrate(ctx, o.Siril, dir, base, postprocess.ColorCalOptions{
		Enabled: true, RemoveGreen: false, Solve: solve, Spcc: o.Spcc,
	})
	if err != nil {
		return "sky colour calibration: " + err.Error()
	}
	return note
}

// graxpertInPlace runs one GraXpert op (background extraction, or denoise) on a FITS in place: it
// writes a sibling output then renames it over the input on success. Returns a human-readable note on
// any problem (never an error), empty on success. GraXpert can exit 0 after a fatal ONNX error, so
// its runner also inspects the log — a note here means "kept the previous file untouched".
func graxpertInPlace(ctx context.Context, r *graxpert.Runner, path string, denoise bool, onProgress func(siril.Progress)) string {
	label, out := "GraXpert background extraction", strings.TrimSuffix(path, ".fits")+"_gxbg.fits"
	if denoise {
		label, out = "GraXpert denoise", strings.TrimSuffix(path, ".fits")+"_gxdn.fits"
	}
	fwd := func(p graxpert.Progress) {
		if onProgress != nil {
			onProgress(siril.Progress{Line: p.Line, Percent: p.Percent, Sample: p.Sample})
		}
	}
	var err error
	if denoise {
		err = r.Denoise(ctx, path, out, graxpert.DenoiseOptions{}, fwd)
	} else {
		err = r.ExtractBackground(ctx, path, out, graxpert.BackgroundOptions{}, fwd)
	}
	if err != nil {
		return label + " skipped: " + err.Error()
	}
	if _, statErr := os.Stat(out); statErr != nil {
		return label + " skipped: no output produced"
	}
	if err := os.Rename(out, path); err != nil {
		return label + " skipped: " + err.Error()
	}
	return ""
}

// deriveSolve fills the plate scale (FocalMM/PixelUm) from the camera's EXIF 35 mm-equivalent focal
// length and the image width, so a phone field solves at roughly the right scale. The 35 mm
// equivalent fixes the (sensor-independent) horizontal field of view: fov = 2·atan(36/(2·f35)); the
// per-pixel scale is fov/width. We then synthesize a focal+pixel pair giving that scale — Siril only
// uses their ratio. focal35≤0, width≤0, or an already-set focal leaves the scale to Siril.
func deriveSolve(base siril.SolveOptions, focal35 float64, widthPx int) siril.SolveOptions {
	if focal35 <= 0 || widthPx <= 0 || base.FocalMM > 0 {
		return base
	}
	fovRad := 2 * math.Atan(36.0/(2*focal35))
	arcsecPerPx := fovRad * (180 / math.Pi) * 3600 / float64(widthPx)
	if arcsecPerPx <= 0 {
		return base
	}
	const pixelUm = 3.76 // arbitrary; only the focal/pixel ratio sets the scale Siril solves at
	base.PixelUm = pixelUm
	base.FocalMM = 206.265 * pixelUm / arcsecPerPx
	return base
}
