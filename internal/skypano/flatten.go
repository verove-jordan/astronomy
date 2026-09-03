package skypano

// flatten.go removes the sky dome from an assembled canvas.
//
// This is the one place background extraction is allowed to run — see the mode's docs. A panel is 57
// by 72 degrees of sky filled edge to edge with the Milky Way, so flattening a panel against its own
// background subtracts the subject. On the canvas the whole structure is visible at once and the band
// can be told from the glow.
//
// Two choices make that separation safe, and both are deliberate:
//
// The model is a LOW-ORDER POLYNOMIAL, not the grid-and-blur model of nightscape's removeSkyGradient.
// That one samples a background cell every width/16 and smooths — about 8 degrees on this canvas —
// which follows a 20-to-30-degree band comfortably and would eat it. A quadratic over a 120-degree
// canvas cannot produce a narrow ridge no matter how it is fitted: the band survives because the model
// is incapable of representing it.
//
// The band is masked by GALACTIC LATITUDE, which here is computed and not estimated. The canvas knows
// its own frame, so "is this pixel in the band" is a coordinate conversion rather than a threshold on
// brightness — and a threshold on brightness is exactly what would confuse the band with the glow.

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// FlattenOptions tune the background model.
type FlattenOptions struct {
	// Exclude marks pixels that must not be sampled for the background model — the landscape under an
	// arch. See GradeOptions.Exclude for why this cannot be folded into the coverage.
	Exclude []bool

	// Order of the 2D polynomial. 2 is the default and about the highest that is still safely blind
	// to the band; 1 removes a plain tilt; 0 removes only a level.
	Order int
	// MaskLatDeg excludes galactic latitudes below this from the FIT (the model is still evaluated
	// and subtracted everywhere). The band's bright part is roughly 15 degrees wide, and the diffuse
	// galactic light reaches further, so this is set beyond the visible edge of it.
	MaskLatDeg float64
	// TilePx is the sampling tile. Each tile contributes one sample.
	TilePx int
	// Percentile is the per-tile lower envelope, so stars and knots do not pull the model up.
	Percentile float64
	// MinCoverage is the weight a pixel needs before it is sampled, in units of ONE PANEL AT FULL
	// WEIGHT — the thin, noisy outermost pixels of the mosaic are not background measurements.
	// Absolute, for the reason GradeOptions.MinCoverage is.
	MinCoverage float64
	// Strength scales how much of the model is removed, 0..1.
	Strength float64
}

func DefaultFlattenOptions() FlattenOptions {
	return FlattenOptions{Order: 2, MaskLatDeg: 20, TilePx: 96, Percentile: 20, MinCoverage: 0.5, Strength: 1}
}

// Background reports what was fitted, so a run can say why the sky came out the way it did.
type Background struct {
	Order   int
	Tiles   int         // samples that survived the mask and the coverage cut
	Coef    [][]float64 // per channel, in the same term order as polyTerms
	Pedesto []float64   // per channel, the level added back after subtraction
	MinMax  [][2]float64
}

// Flatten subtracts the sky dome from im in place. cov is Render's weight map.
func Flatten(im *fits.Image, cov []float32, c Canvas, o FlattenOptions) (Background, error) {
	if len(cov) != im.W*im.H {
		return Background{}, fmt.Errorf("skypano: coverage is %d pixels, image is %d", len(cov), im.W*im.H)
	}
	if o.Strength <= 0 {
		return Background{}, nil
	}
	if o.TilePx < 8 {
		o.TilePx = 8
	}
	minCov := float32(o.MinCoverage)

	us, vs, vals := sampleTiles(im, cov, c, o, minCov)
	if len(us) == 0 {
		return Background{}, fmt.Errorf("skypano: no background samples outside |b| < %.0f degrees", o.MaskLatDeg)
	}

	bg := Background{Tiles: len(us)}
	// Step the order down rather than fit an under-determined model: an order that the data cannot
	// support produces a confident, wrong surface, and on a canvas that means an invented gradient.
	order := o.Order
	for order > 0 && len(us) < 4*numTerms(order) {
		order--
	}
	bg.Order = order

	for ch := 0; ch < im.C; ch++ {
		coef, ok := fitPoly(us, vs, vals[ch], order)
		if !ok {
			coef = []float64{median(vals[ch])} // a level is always safe
			bg.Order = 0
		}
		bg.Coef = append(bg.Coef, coef)

		model := make([]float64, 0, len(cov))
		lo, hi := math.Inf(1), math.Inf(-1)
		for y := 0; y < im.H; y++ {
			for x := 0; x < im.W; x++ {
				if cov[y*im.W+x] < minCov {
					model = append(model, math.NaN())
					continue
				}
				pu, pv := uv(x, y, im.W, im.H)
				m := evalPoly(coef, pu, pv)
				model = append(model, m)
				lo, hi = math.Min(lo, m), math.Max(hi, m)
			}
		}
		bg.MinMax = append(bg.MinMax, [2]float64{lo, hi})

		// Add back the model's MAXIMUM, so subtracting it can never drive a pixel to zero. The median
		// looked more natural and was wrong: over the half of the canvas where the model sits above
		// its median, the darkest sky clips, and it clips in whichever channel carries the dome — the
		// green one, here, because airglow is green. That is not a level change, it is a colour
		// artefact, and it painted the high-latitude sky maroon. The level this adds back is
		// arbitrary and the grade's black point removes it.
		ped := hi
		bg.Pedesto = append(bg.Pedesto, ped)

		s := float32(o.Strength)
		p := im.Pix[ch]
		for i := range p {
			if math.IsNaN(model[i]) {
				continue
			}
			p[i] -= s * float32(model[i]-ped)
		}
	}
	return bg, nil
}

// sampleTiles reduces each tile to one robust background value, dropping tiles inside the band and
// tiles the mosaic barely covers.
func sampleTiles(im *fits.Image, cov []float32, c Canvas, o FlattenOptions, minCov float32) (us, vs []float64, vals [][]float64) {
	vals = make([][]float64, im.C)
	for ty := 0; ty+o.TilePx <= im.H; ty += o.TilePx {
		for tx := 0; tx+o.TilePx <= im.W; tx += o.TilePx {
			cx, cy := float64(tx+o.TilePx/2), float64(ty+o.TilePx/2)
			if o.MaskLatDeg > 0 {
				v, ok := c.PixToSky(cx, cy)
				if !ok {
					continue
				}
				_, b := vecToLonLat(equatorialToGalactic(v))
				if math.Abs(b) < o.MaskLatDeg {
					continue
				}
			}
			var idx []int
			for y := ty; y < ty+o.TilePx; y += 2 {
				for x := tx; x < tx+o.TilePx; x += 2 {
					i := y*im.W + x
					if len(o.Exclude) == len(cov) && o.Exclude[i] {
						continue
					}
					if cov[i] >= minCov {
						idx = append(idx, i)
					}
				}
			}
			if len(idx) < (o.TilePx*o.TilePx)/16 {
				continue
			}
			u, v := uv(tx+o.TilePx/2, ty+o.TilePx/2, im.W, im.H)
			us, vs = append(us, u), append(vs, v)
			for ch := 0; ch < im.C; ch++ {
				buf := make([]float64, len(idx))
				for k, i := range idx {
					buf[k] = float64(im.Pix[ch][i])
				}
				sort.Float64s(buf)
				vals[ch] = append(vals[ch], buf[int(o.Percentile/100*float64(len(buf)-1))])
			}
		}
	}
	return us, vs, vals
}

// uv maps pixels to [-1,1], which keeps the normal equations well conditioned at order 2 or 3.
func uv(x, y, w, h int) (float64, float64) {
	return (float64(x) - float64(w)/2) / (float64(w) / 2), (float64(y) - float64(h)/2) / (float64(h) / 2)
}

// numTerms is the count of monomials u^i v^j with i+j <= order.
func numTerms(order int) int { return (order + 1) * (order + 2) / 2 }

// polyTerms evaluates the monomial basis at (u,v), in a fixed order the coefficients follow.
func polyTerms(u, v float64, order int) []float64 {
	out := make([]float64, 0, numTerms(order))
	for total := 0; total <= order; total++ {
		for i := total; i >= 0; i-- {
			out = append(out, math.Pow(u, float64(i))*math.Pow(v, float64(total-i)))
		}
	}
	return out
}

func evalPoly(coef []float64, u, v float64) float64 {
	order := 0
	for numTerms(order) < len(coef) {
		order++
	}
	t := polyTerms(u, v, order)
	var s float64
	for i := range coef {
		s += coef[i] * t[i]
	}
	return s
}

// fitPoly solves the least-squares normal equations for the monomial basis.
func fitPoly(us, vs, vals []float64, order int) ([]float64, bool) {
	n := numTerms(order)
	a := make([][]float64, n)
	for i := range a {
		a[i] = make([]float64, n)
	}
	b := make([]float64, n)
	for k := range us {
		t := polyTerms(us[k], vs[k], order)
		for i := 0; i < n; i++ {
			b[i] += t[i] * vals[k]
			for j := 0; j < n; j++ {
				a[i][j] += t[i] * t[j]
			}
		}
	}
	return solveDense(a, b)
}

// solveDense is Gaussian elimination with partial pivoting, for the handful of terms a background
// model has.
func solveDense(a [][]float64, b []float64) ([]float64, bool) {
	n := len(b)
	m := make([][]float64, n)
	for i := range m {
		m[i] = append(append([]float64(nil), a[i]...), b[i])
	}
	for col := 0; col < n; col++ {
		p := col
		for r := col + 1; r < n; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[p][col]) {
				p = r
			}
		}
		if math.Abs(m[p][col]) < 1e-12 {
			return nil, false
		}
		m[col], m[p] = m[p], m[col]
		for r := 0; r < n; r++ {
			if r == col {
				continue
			}
			f := m[r][col] / m[col][col]
			for k := col; k <= n; k++ {
				m[r][k] -= f * m[col][k]
			}
		}
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = m[i][n] / m[i][i]
		if math.IsNaN(out[i]) || math.IsInf(out[i], 0) {
			return nil, false
		}
	}
	return out, true
}

// TypicalCoverage is the median weight over covered pixels — in units of one panel, so it reads as
// how many panels deep the mosaic typically is. A diagnostic, not a threshold: what counts as a real
// pixel is an absolute amount of panel, not a share of however many happened to overlap.
func TypicalCoverage(cov []float32) float32 {
	s := make([]float32, 0, len(cov))
	for _, v := range cov {
		if v > 0 {
			s = append(s, v)
		}
	}
	if len(s) == 0 {
		return 0
	}
	sort.Slice(s, func(a, b int) bool { return s[a] < s[b] })
	return s[len(s)/2]
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}
