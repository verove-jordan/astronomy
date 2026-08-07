package stacknative

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// normSampleDim is the longest side of the decimated read used to measure each frame's level and
// spread. Normalization coefficients are global statistics — measuring them on a ~512 px thumbnail
// costs a fraction of a full read and moves the answer by far less than the noise.
const normSampleDim = 512

// normCoef maps one frame onto the reference frame's photometric scale: x' = scale·x + offset.
type normCoef struct {
	scale  float64
	offset float64
}

func (c normCoef) apply(x float64) float64 { return c.scale*x + c.offset }

// identityCoef leaves a frame untouched.
func identityCoef() normCoef { return normCoef{scale: 1} }

// normalizationFor measures every frame's location and spread and derives the coefficient that puts
// it on the FIRST frame's footing — the reference, as Siril uses the sequence reference.
//
//   - none:     frames are combined as they are (correct for calibration masters, where the
//     pedestal IS the signal)
//   - add:      level the background by an offset only
//   - addscale: level the background AND the spread — the deep-sky default, so subs shot through
//     different transparency stack together fairly
//   - mul:      scale by a ratio (the physically correct choice for flats)
//   - mulscale: multiplicative levelling with a spread correction too
func normalizationFor(files []*fits.File, mode stackalg.Norm, fast bool) ([]normCoef, error) {
	out := make([]normCoef, len(files))
	for i := range out {
		out[i] = identityCoef()
	}
	if mode == stackalg.NormNone {
		return out, nil
	}
	locs := make([]float64, len(files))
	scales := make([]float64, len(files))
	for i, f := range files {
		loc, scale, err := frameStats(f, fast)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i+1, err)
		}
		locs[i], scales[i] = loc, scale
	}
	refLoc, refScale := locs[0], scales[0]
	for i := range files {
		out[i] = coefFor(mode, locs[i], scales[i], refLoc, refScale)
	}
	return out, nil
}

// coefFor turns one frame's statistics into its mapping onto the reference.
func coefFor(mode stackalg.Norm, loc, scale, refLoc, refScale float64) normCoef {
	switch mode {
	case stackalg.NormAdd:
		return normCoef{scale: 1, offset: refLoc - loc}
	case stackalg.NormMul:
		if loc == 0 {
			return identityCoef()
		}
		return normCoef{scale: refLoc / loc}
	case stackalg.NormMulScale:
		if loc == 0 || scale == 0 {
			return identityCoef()
		}
		return normCoef{scale: (refLoc / loc) * (refScale / scale)}
	default: // NormAddScale and the auto/empty value — the deep-sky default
		if scale == 0 {
			return normCoef{scale: 1, offset: refLoc - loc}
		}
		k := refScale / scale
		return normCoef{scale: k, offset: refLoc - k*loc}
	}
}

// frameStats measures a frame's level (median) and spread on a decimated read. fast swaps the
// robust MAD for the plain standard deviation — quicker, very slightly less robust, which is exactly
// what Siril's -fastnorm trades.
func frameStats(f *fits.File, fast bool) (loc, scale float64, err error) {
	grid, w, h, err := f.ReadDownsampled(normSampleDim, fits.Mean)
	if err != nil {
		return 0, 0, err
	}
	if w*h == 0 || len(grid) == 0 {
		return 0, 0, fmt.Errorf("empty frame")
	}
	s := newScratch(len(grid))
	loc = median(grid, s)
	if fast {
		var ss float64
		for _, x := range grid {
			d := x - loc
			ss += d * d
		}
		return loc, math.Sqrt(ss / float64(len(grid))), nil
	}
	return loc, mad(grid, loc, s), nil
}

// The header-card helpers. WriteFITSWith takes pre-padded 80-character cards, so each value is
// rendered in FITS fixed format here rather than by the caller.
func intCard(key string, v int64, comment string) string {
	return padCard(fmt.Sprintf("%-8s= %20d / %s", key, v, comment))
}

func floatCard(key string, v float64, comment string) string {
	return padCard(fmt.Sprintf("%-8s= %20s / %s", key, strconv.FormatFloat(v, 'G', -1, 64), comment))
}

func strCard(key, v string) string {
	if len(v) > 68 {
		v = v[:68]
	}
	return padCard(fmt.Sprintf("%-8s= '%s'", key, v))
}

func padCard(s string) string {
	if len(s) > 80 {
		return s[:80]
	}
	return s + strings.Repeat(" ", 80-len(s))
}

// noiseWeights derives a per-frame weight from each frame's measured background spread — the
// quietest frames count most, which is what Siril's -weight=noise does. It reuses the same decimated
// statistics the normalization already needs, so it costs no extra read.
func noiseWeights(files []*fits.File, fast bool) ([]float64, error) {
	w := make([]float64, len(files))
	var best float64
	for i, f := range files {
		_, scale, err := frameStats(f, fast)
		if err != nil {
			return nil, err
		}
		if scale <= 0 {
			scale = math.SmallestNonzeroFloat64
		}
		w[i] = 1 / (scale * scale) // inverse variance: the statistically optimal weighting
		if w[i] > best {
			best = w[i]
		}
	}
	if best <= 0 {
		return nil, fmt.Errorf("no frame produced a usable noise estimate")
	}
	for i := range w {
		w[i] /= best // normalized to the cleanest frame, so the numbers stay readable in logs
	}
	return w, nil
}
