package comet

import (
	"math"
	"sort"
)

// Obs is a comet centroid detected in one frame at time T (epoch ms).
type Obs struct {
	T int64
	P Point
}

// FitTrack fits a robust linear motion track to many per-frame comet centroids — far more accurate than
// two single-frame anchors, because the diffuse coma cannot be centroided precisely in one short sub. It
// least-squares fits x(t) and y(t), then iteratively drops outlier frames (bad detections) beyond 4·MAD of
// the residual and refits. ok is false with fewer than two surviving observations. Returned as a Track
// (the line sampled at the observation time span) so callers use it like any other track.
func FitTrack(obs []Obs) (Track, bool) {
	if len(obs) < 2 {
		return Track{}, false
	}
	tmin, tmax := obs[0].T, obs[0].T
	for _, o := range obs {
		tmin, tmax = min(tmin, o.T), max(tmax, o.T)
	}
	if tmin == tmax {
		return Track{}, false
	}

	keep := make([]int, len(obs))
	for i := range keep {
		keep[i] = i
	}
	var ax, bx, ay, by float64
	for iter := 0; iter < 4 && len(keep) >= 2; iter++ {
		ax, bx = linFit(obs, keep, tmin, func(o Obs) float64 { return o.P.X })
		ay, by = linFit(obs, keep, tmin, func(o Obs) float64 { return o.P.Y })
		res := make([]float64, len(keep))
		for k, i := range keep {
			rt := float64(obs[i].T - tmin)
			res[k] = math.Hypot(obs[i].P.X-(ax+bx*rt), obs[i].P.Y-(ay+by*rt))
		}
		med, mad := medianMAD64(res)
		if mad <= 0 {
			break
		}
		next := make([]int, 0, len(keep))
		for k, i := range keep {
			if res[k] <= med+4*mad {
				next = append(next, i)
			}
		}
		if len(next) == len(keep) || len(next) < 2 {
			break
		}
		keep = next
	}
	if len(keep) < 2 {
		return Track{}, false
	}
	at := func(t int64) Point {
		rt := float64(t - tmin)
		return Point{X: ax + bx*rt, Y: ay + by*rt}
	}
	return Track{P0: at(tmin), P1: at(tmax), T0: tmin, T1: tmax}, true
}

// linFit returns (a,b) of value = a + b·(t−tmin) by ordinary least squares over the selected indices.
// Times are offset by tmin so the normal-equation terms stay numerically small (epoch-ms² would lose
// precision).
func linFit(obs []Obs, idx []int, tmin int64, val func(Obs) float64) (a, b float64) {
	var n, st, sv, stt, stv float64
	for _, i := range idx {
		t := float64(obs[i].T - tmin)
		v := val(obs[i])
		n++
		st += t
		sv += v
		stt += t * t
		stv += t * v
	}
	denom := n*stt - st*st
	if denom == 0 {
		return sv / n, 0
	}
	b = (n*stv - st*sv) / denom
	a = (sv - b*st) / n
	return a, b
}

func medianMAD64(v []float64) (med, mad float64) {
	if len(v) == 0 {
		return 0, 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	med = s[len(s)/2]
	for i := range s {
		s[i] = math.Abs(s[i] - med)
	}
	sort.Float64s(s)
	return med, s[len(s)/2]
}
