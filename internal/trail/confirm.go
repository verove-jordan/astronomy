package trail

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// confirmInliers keeps the seed pixels within ⊥2 px of the peak line, labels them with 4-connectivity
// and merges the non-singleton components, then validates that the merged set is a genuine long, thin,
// well-centred streak (rejecting compact star clusters that merely graze the line). It returns the
// merged pooled inlier indices and whether the confirmation passed.
func confirmInliers(gw, gh int, seeds []int, cosT, sinT, rhoVal float64) ([]int, bool) {
	near := make([]bool, gw*gh)
	for _, idx := range seeds {
		x, y := float64(idx%gw), float64(idx/gw)
		if math.Abs(x*cosT+y*sinT-rhoVal) <= 2.0 {
			near[idx] = true
		}
	}
	labels, n := imgops.Label(near, gw, gh)
	if n == 0 {
		return nil, false
	}
	size := make([]int, n+1)
	for _, l := range labels {
		if l > 0 {
			size[l]++
		}
	}
	var inliers []int
	for i, l := range labels {
		if l > 0 && size[l] >= 2 { // drop isolated specks; merge the collinear pieces
			inliers = append(inliers, i)
		}
	}
	if len(inliers) < 16 || !elongated(inliers, gw, gh, cosT, sinT, rhoVal) {
		return nil, false
	}
	return inliers, true
}

// elongated confirms the merged inliers form a streak: projection extent L ≥ 25% of the min pooled
// dimension, elongation L/(2·RMS⊥) ≥ 8, and mean |⊥| ≤ 3 px.
func elongated(inliers []int, gw, gh int, cosT, sinT, rhoVal float64) bool {
	dirx, diry := -sinT, cosT
	tmin, tmax := math.Inf(1), math.Inf(-1)
	var sumd2, sumabs float64
	for _, i := range inliers {
		x, y := float64(i%gw), float64(i/gw)
		t := x*dirx + y*diry
		tmin, tmax = math.Min(tmin, t), math.Max(tmax, t)
		d := x*cosT + y*sinT - rhoVal
		sumd2 += d * d
		sumabs += math.Abs(d)
	}
	nInl := float64(len(inliers))
	l := tmax - tmin
	rms := math.Sqrt(sumd2 / nInl)
	minDim := float64(gw)
	if gh < gw {
		minDim = float64(gh)
	}
	if l < 0.25*minDim {
		return false
	}
	if rms > 0 && l/(2*rms) < 8 {
		return false
	}
	return sumabs/nInl <= 3
}
