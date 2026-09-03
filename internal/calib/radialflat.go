package calib

// radialflat.go reduces a master flat to the part of it that is trustworthy: the smooth radial
// falloff of the lens.
//
// A phone flat is easy to shoot and hard to shoot WELL. Measured on a real twenty-one frame set from
// an iPhone 16 Pro, two things are in it:
//
//   - A clean vignette. Brightness falls to about 48% at the corners — a bit over a stop — smoothly,
//     monotonically, with zero gradient at the centre as a real vignette must have. It is identical
//     across a 53-degree spread of camera roll, so it belongs to the lens and not to the room. Apple's
//     ProRAW plainly does not pre-correct it.
//   - A reflection of the phone's own camera bump, in the middle of the frame. It renders as a rounded
//     square with the three lens circles inside it, and it is worth 7% peak to peak.
//
// Rolling the phone between frames is the right idea for separating them and did not work here: 13 of
// the 21 frames sat within 4 degrees of the same roll, and a median is decided by the bulk, so the
// reflection was in the majority at the same place and survived. Removing it needs rolls spread
// evenly around the circle, or the phone tilted so the reflection leaves the frame altogether.
//
// So the flat is used for the half of it that is sound. The vignette is fitted where the data is
// CLEAN — outside the reflection — and extrapolated inward under the constraint that a vignette is an
// even, smooth function of radius. What is discarded with the residual is dust, which on this camera
// there is none of worth correcting.
//
// A radial model has one more property that matters here, and it is not a small one: expressed in
// NORMALISED radius it does not know how big the image is. The flats are 8064x6048 and the lights are
// 4032x3024, which would otherwise have to be binned — and a master of the wrong size is silently
// dropped by the matcher rather than used.

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// RadialVignette is a lens falloff as an even polynomial in normalised radius, scaled so the centre
// is exactly 1.
type RadialVignette struct {
	// Coef are the coefficients of 1, r^2, r^4, r^6, already divided through by the value at r = 0.
	Coef [4]float64
	// FitFrom is the smallest radius whose data was used. Everything inside it is extrapolation.
	FitFrom float64
	// RMS is how well the polynomial matched the measured profile over the fitted region — the
	// honest measure of whether a vignette this simple describes this lens.
	RMS float64
}

// At evaluates the vignette at a normalised radius (0 at the centre, 1 at the corner).
func (v RadialVignette) At(r float64) float64 {
	r2 := r * r
	return v.Coef[0] + r2*(v.Coef[1]+r2*(v.Coef[2]+r2*v.Coef[3]))
}

// Image materialises the vignette as a master flat of the given size, normalised to a mean of 1 the
// way the calibration path expects a flat to arrive.
func (v RadialVignette) Image(w, h, channels int) *fits.Image {
	im := fits.NewImage(w, h, channels)
	cx, cy := float64(w)/2, float64(h)/2
	maxR := math.Hypot(cx, cy)
	var sum float64
	vals := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) / maxR
			p := v.At(r)
			if p < 1e-3 {
				p = 1e-3 // a flat is divided by; never hand the divider a zero
			}
			vals[y*w+x] = p
			sum += p
		}
	}
	mean := sum / float64(len(vals))
	for c := 0; c < channels; c++ {
		for i, p := range vals {
			im.Pix[c][i] = float32(p / mean)
		}
	}
	return im
}

// FitRadialVignette fits an even polynomial to a measured radial profile.
//
// Bins whose mean radius falls inside fitFrom are IGNORED — that is the whole point, since the middle of a
// phone flat carries a reflection of the phone itself and fitting through it would bake that
// reflection into the model as a bogus central brightening.
//
// The fit is constrained to even powers, which is what makes extrapolating inward legitimate: an even
// polynomial has zero gradient at r = 0 automatically, so it cannot invent a peak or a dimple there.
func FitRadialVignette(profile *RadialProfile, fitFrom float64) (RadialVignette, error) {
	if profile == nil || len(profile.Level) < 8 {
		return RadialVignette{}, fmt.Errorf("calib: a radial profile needs at least 8 bins")
	}
	var rs, ys []float64
	for i, y := range profile.Level {
		r := profile.MeanR[i]
		if r <= 0 || r < fitFrom || y <= 0 {
			continue
		}
		rs = append(rs, r)
		ys = append(ys, y)
	}
	if len(rs) < 4 {
		return RadialVignette{}, fmt.Errorf("calib: only %d bins outside r=%.2f, too few to fit", len(rs), fitFrom)
	}
	// Normal equations for y = c0 + c1 r^2 + c2 r^4 + c3 r^6.
	var a [4][5]float64
	for k, r := range rs {
		t := [4]float64{1, r * r, r * r * r * r, math.Pow(r, 6)}
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				a[i][j] += t[i] * t[j]
			}
			a[i][4] += t[i] * ys[k]
		}
	}
	c, ok := solve4(a)
	if !ok {
		return RadialVignette{}, fmt.Errorf("calib: the radial fit is singular")
	}
	if c[0] <= 0 {
		return RadialVignette{}, fmt.Errorf("calib: the radial fit extrapolates to %.3f at the centre", c[0])
	}
	v := RadialVignette{FitFrom: fitFrom}
	for i := range v.Coef {
		v.Coef[i] = c[i] / c[0] // scale so the centre is exactly 1
	}
	var ss float64
	for k, r := range rs {
		d := v.At(r) - ys[k]/c[0]
		ss += d * d
	}
	v.RMS = math.Sqrt(ss / float64(len(rs)))
	return v, nil
}

// solve4 is Gaussian elimination with partial pivoting on a 4x5 augmented matrix.
func solve4(a [4][5]float64) ([4]float64, bool) {
	for col := 0; col < 4; col++ {
		p := col
		for r := col + 1; r < 4; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[p][col]) {
				p = r
			}
		}
		if math.Abs(a[p][col]) < 1e-14 {
			return [4]float64{}, false
		}
		a[col], a[p] = a[p], a[col]
		for r := 0; r < 4; r++ {
			if r == col {
				continue
			}
			f := a[r][col] / a[col][col]
			for k := col; k < 5; k++ {
				a[r][k] -= f * a[col][k]
			}
		}
	}
	var out [4]float64
	for i := 0; i < 4; i++ {
		out[i] = a[i][4] / a[i][i]
	}
	return out, true
}

// RadialProfile is an azimuthally-averaged measurement of a plane.
type RadialProfile struct {
	// Level is the mean value in each bin, and MeanR the bin's area-weighted MEAN radius — which is
	// not its centre. In a rectangular frame the outer bins are truncated annuli, reached only near
	// the corners, so their mean radius falls short of the bin centre by enough to bend a fit (0.012
	// in the last bin of a 4:3 frame). Carrying the real radius costs nothing and removes the bias.
	Level, MeanR []float64
}

// RadialProfileOf measures the azimuthally-averaged profile of a plane, in bins of normalised radius.
func RadialProfileOf(v []float64, w, h, bins int) *RadialProfile {
	if bins < 1 || len(v) != w*h {
		return nil
	}
	cx, cy := float64(w)/2, float64(h)/2
	maxR := math.Hypot(cx, cy)
	sum := make([]float64, bins)
	rsum := make([]float64, bins)
	cnt := make([]float64, bins)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) / maxR
			b := int(r * float64(bins))
			if b >= bins {
				b = bins - 1
			}
			sum[b] += v[y*w+x]
			rsum[b] += r
			cnt[b]++
		}
	}
	out := &RadialProfile{Level: make([]float64, bins), MeanR: make([]float64, bins)}
	for i := range out.Level {
		if cnt[i] > 0 {
			out.Level[i] = sum[i] / cnt[i]
			out.MeanR[i] = rsum[i] / cnt[i]
		}
	}
	return out
}
