package setqa

import (
	"math"
	"sort"
)

// planeFit is the least-squares background plane b(u,v) = a0 + ax·u + ay·v over tile centers,
// with u,v normalized to [-1,1]; sigma is the robust scatter of the kept residuals.
type planeFit struct {
	a0, ax, ay float64
	sigma      float64
}

// fitPlaneRobust fits the INTERIOR tiles only (the border ring is the halo metric's subject — a
// border halo must not drag the plane or inflate its own noise floor), then twice drops tiles
// whose residual exceeds 2.5·MADσ (object tiles) and refits — the plane ends up tracking the sky.
func fitPlaneRobust(tiles []float64) planeFit {
	keep := make([]bool, len(tiles))
	for i := range keep {
		keep[i] = interiorTile(i)
	}
	var fit planeFit
	for iter := 0; iter < 3; iter++ {
		fit = fitPlane(tiles, keep)
		res := make([]float64, 0, len(tiles))
		for i, v := range tiles {
			if keep[i] {
				res = append(res, v-fit.at(i))
			}
		}
		fit.sigma = madSigma(res)
		if iter == 2 {
			break
		}
		cut := 2.5 * math.Max(fit.sigma, 1e-12)
		for i, v := range tiles {
			if keep[i] && math.Abs(v-fit.at(i)) > cut {
				keep[i] = false
			}
		}
	}
	return fit
}

func (f planeFit) at(tile int) float64 {
	u, v := tileUV(tile)
	return f.a0 + f.ax*u + f.ay*v
}

func tileUV(tile int) (u, v float64) {
	r, c := tile/tileCols, tile%tileCols
	return 2*(float64(c)+0.5)/tileCols - 1, 2*(float64(r)+0.5)/tileRows - 1
}

func interiorTile(tile int) bool {
	r, c := tile/tileCols, tile%tileCols
	return c >= borderRing && c < tileCols-borderRing && r >= borderRing && r < tileRows-borderRing
}

// fitPlane solves the 3×3 normal equations by Cramer's rule; a degenerate system (nearly all
// tiles dropped) falls back to the flat plane through the mean.
func fitPlane(tiles []float64, keep []bool) planeFit {
	var n, su, sv, suu, svv, suv, sb, sbu, sbv float64
	for i, b := range tiles {
		if !keep[i] {
			continue
		}
		u, v := tileUV(i)
		n++
		su += u
		sv += v
		suu += u * u
		svv += v * v
		suv += u * v
		sb += b
		sbu += b * u
		sbv += b * v
	}
	det := n*(suu*svv-suv*suv) - su*(su*svv-suv*sv) + sv*(su*suv-suu*sv)
	if n == 0 || math.Abs(det) < 1e-12 {
		mean := 0.0
		if n > 0 {
			mean = sb / n
		}
		return planeFit{a0: mean}
	}
	a0 := (sb*(suu*svv-suv*suv) - su*(sbu*svv-suv*sbv) + sv*(sbu*suv-suu*sbv)) / det
	ax := (n*(sbu*svv-sbv*suv) - sb*(su*svv-suv*sv) + sv*(su*sbv-sbu*sv)) / det
	ay := (n*(suu*sbv-suv*sbu) - su*(su*sbv-sbu*sv) + sb*(su*suv-suu*sv)) / det
	return planeFit{a0: a0, ax: ax, ay: ay}
}

func median64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	buf := append([]float64(nil), vals...)
	sort.Float64s(buf)
	mid := len(buf) / 2
	if len(buf)%2 == 1 {
		return buf[mid]
	}
	return (buf[mid-1] + buf[mid]) / 2
}

func percentile64(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	buf := append([]float64(nil), vals...)
	sort.Float64s(buf)
	if p <= 0 {
		return buf[0]
	}
	if p >= 100 {
		return buf[len(buf)-1]
	}
	rank := p / 100 * float64(len(buf)-1)
	lo := int(math.Floor(rank))
	frac := rank - float64(lo)
	if lo+1 >= len(buf) {
		return buf[len(buf)-1]
	}
	return buf[lo] + frac*(buf[lo+1]-buf[lo])
}

// madSigma estimates a robust standard deviation from residuals (median absolute deviation about
// the median, scaled to Gaussian sigma).
func madSigma(res []float64) float64 {
	if len(res) == 0 {
		return 0
	}
	med := median64(res)
	abs := make([]float64, len(res))
	for i, v := range res {
		abs[i] = math.Abs(v - med)
	}
	return 1.4826 * median64(abs)
}
