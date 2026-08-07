package scene3d

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

const (
	// minClusterStars is how many members with a measured parallax a distance must rest on. Below
	// this the "mode" is just whichever two field stars happened to land near each other.
	minClusterStars = 12
	// minClusterFraction is the share of the in-footprint stars that must agree on the distance. A
	// real cluster dominates its own ellipse; if only a tenth of the stars there agree, what was found
	// is a chance alignment in the field, not a cluster.
	minClusterFraction = 0.25
	// clusterCoreDex is the half-width (in decades of distance) around the mode used to measure the
	// spread before membership is decided. 0.1 dex is ±26%, comfortably wider than a real cluster's
	// depth and than Gaia's parallax error at a few kpc, without reaching into the field.
	clusterCoreDex = 0.1
	// minClusterSigmaDex floors the membership window so a cluster whose members happen to agree to
	// four decimal places does not end up rejecting all but a handful of them.
	minClusterSigmaDex = 0.02
)

// clusterTypes are the label types whose distance can be measured from the frame itself.
var clusterTypes = map[string]bool{"open_cluster": true, "globular": true, "cluster": true}

// clusterFit is one object's distance as measured from its own member stars.
type clusterFit struct {
	distPc   float64
	sigmaDex float64
	members  int   // stars inside the footprint that agree on the distance
	sampled  int   // stars inside the footprint that had a measured distance at all
	memberOf []int // indices into the point slice, for flagging the records
}

// measureCluster derives a cluster's distance from the stars the frame actually resolved inside its
// footprint. This is a measurement rather than a lookup, and it is free: the parallaxes are already
// loaded for every identified star, and the footprint is already projected into final-image pixels.
//
// It matters because it is the only distance in the whole scene that comes from THIS image. A
// catalogued value places the object where a reference says it is; this places it where the picture
// says it is, and a disagreement between the two is worth seeing rather than hiding — so both end up
// in the manifest.
func measureCluster(l annotate.Label, points []annotate.Point) (clusterFit, bool) {
	if l.Extent == nil {
		return clusterFit{}, false
	}
	var logs []float64
	var idx []int
	for i, p := range points {
		if p.Star == nil || p.Star.DistPc <= 0 {
			continue
		}
		if !insideExtent(l, float64(p.X), float64(p.Y)) {
			continue
		}
		logs = append(logs, math.Log10(p.Star.DistPc))
		idx = append(idx, i)
	}
	if len(logs) < minClusterStars {
		return clusterFit{}, false
	}

	mode := halfSampleMode(append([]float64(nil), logs...))

	// Spread from the core alone: the field stars in the same footprint span decades and would
	// otherwise inflate sigma until everything counted as a member.
	var core []float64
	for _, v := range logs {
		if math.Abs(v-mode) <= clusterCoreDex {
			core = append(core, v)
		}
	}
	if len(core) < minClusterStars {
		return clusterFit{}, false
	}
	sigma := math.Max(minClusterSigmaDex, stdDev(core, mode))

	fit := clusterFit{distPc: math.Pow(10, mode), sigmaDex: sigma, sampled: len(logs)}
	for i, v := range logs {
		if math.Abs(v-mode) <= 2*sigma {
			fit.members++
			fit.memberOf = append(fit.memberOf, idx[i])
		}
	}
	if fit.members < minClusterStars || float64(fit.members) < minClusterFraction*float64(fit.sampled) {
		return clusterFit{}, false
	}
	return fit, true
}

// insideExtent reports whether a final-image pixel falls inside a label's projected ellipse.
func insideExtent(l annotate.Label, x, y float64) bool {
	e := l.Extent
	if e == nil || e.RXpx <= 0 || e.RYpx <= 0 {
		return false
	}
	dx, dy := x-l.X, y-l.Y
	sin, cos := math.Sincos(e.AngleRad)
	// Rotate into the ellipse's own frame, where the test is the plain unit-circle one.
	u := (dx*cos + dy*sin) / e.RXpx
	v := (-dx*sin + dy*cos) / e.RYpx
	return u*u+v*v <= 1
}

// halfSampleMode is the Bickel–Frühwirth mode estimator: repeatedly keep the densest half of the
// sample (the shortest interval containing half the points) until two are left. It finds the peak of
// a distribution sitting on a broad background without any bin width to choose — which is exactly
// the shape here, a narrow cluster on a field spanning decades. Destroys the input's order.
func halfSampleMode(x []float64) float64 {
	sort.Float64s(x)
	for len(x) > 2 {
		h := (len(x) + 1) / 2
		best, bestWidth := 0, math.Inf(1)
		for i := 0; i+h <= len(x); i++ {
			if w := x[i+h-1] - x[i]; w < bestWidth {
				best, bestWidth = i, w
			}
		}
		x = x[best : best+h]
	}
	if len(x) == 1 {
		return x[0]
	}
	return (x[0] + x[1]) / 2
}

// stdDev is the spread of xs about a fixed centre (the mode, not the mean — the distribution is
// deliberately not assumed symmetric).
func stdDev(xs []float64, center float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var ss float64
	for _, v := range xs {
		d := v - center
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)))
}
