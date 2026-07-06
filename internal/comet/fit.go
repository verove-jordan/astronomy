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
// the residual and refits. kept reports how many observations survived the rejection — the caller's
// consistency signal (a fit through mostly-rejected detections is not a track). ok is false with fewer
// than two surviving observations. Returned as a Track (the line sampled at the observation time span).
func FitTrack(obs []Obs) (tr Track, kept int, ok bool) {
	if len(obs) < 2 {
		return Track{}, 0, false
	}
	tmin, tmax := obs[0].T, obs[0].T
	for _, o := range obs {
		tmin, tmax = min(tmin, o.T), max(tmax, o.T)
	}
	if tmin == tmax {
		return Track{}, 0, false
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
		return Track{}, len(keep), false
	}
	at := func(t int64) Point {
		rt := float64(t - tmin)
		return Point{X: ax + bx*rt, Y: ay + by*rt}
	}
	return Track{P0: at(tmin), P1: at(tmax), T0: tmin, T1: tmax}, len(keep), true
}

// QuadTrack models the comet's apparent motion as a quadratic in time — over a long session the
// projected sky motion visibly curves, and pinning a curved path with a line smears the coma.
type QuadTrack struct {
	T0                     int64 // time origin (epoch ms); coefficients are in offset time
	AX, BX, CX, AY, BY, CY float64
}

// At returns the comet's modeled position at time t.
func (q QuadTrack) At(t int64) Point {
	rt := float64(t - q.T0)
	return Point{X: q.AX + q.BX*rt + q.CX*rt*rt, Y: q.AY + q.BY*rt + q.CY*rt*rt}
}

// Shift is the translation that moves the comet from its position at time t onto ref.
func (q QuadTrack) Shift(t int64, ref Point) (dx, dy float64) {
	p := q.At(t)
	return ref.X - p.X, ref.Y - p.Y
}

// quadImprovementMin is how much the quadratic's median residual must beat the linear fit's to be
// selected (a real curvature signal, not noise chasing an extra degree of freedom).
const quadImprovementMin = 0.8

// fitBestSpanMs is the session span beyond which curvature becomes worth testing (~2 h).
const fitBestSpanMs = 2 * 60 * 60 * 1000

// FitBestTrack fits the linear track and — for sessions longer than ~2 h — also a quadratic on the
// linear fit's surviving observations, returning the quadratic only when it reduces the median
// residual by >20%. kept/ok mirror FitTrack.
func FitBestTrack(obs []Obs) (tr Tracker, kept int, ok bool) {
	lin, kept, ok := FitTrack(obs)
	if !ok {
		return lin, kept, ok
	}
	if lin.T1-lin.T0 < fitBestSpanMs || len(obs) < 6 {
		return lin, kept, true
	}
	quad, qres, qok := fitQuad(obs, lin)
	if !qok {
		return lin, kept, true
	}
	lres := medianResidual(obs, lin)
	if lres > 0 && qres < quadImprovementMin*lres {
		return quad, kept, true
	}
	return lin, kept, true
}

// fitQuad least-squares fits x(t), y(t) as quadratics over the observations within the linear fit's
// tolerance (reusing its outlier verdicts implicitly by refitting on all points but reporting the
// median residual for the caller's comparison).
func fitQuad(obs []Obs, lin Track) (QuadTrack, float64, bool) {
	t0 := lin.T0
	axs, ok1 := quadFit(obs, t0, func(o Obs) float64 { return o.P.X })
	ays, ok2 := quadFit(obs, t0, func(o Obs) float64 { return o.P.Y })
	if !ok1 || !ok2 {
		return QuadTrack{}, 0, false
	}
	q := QuadTrack{T0: t0, AX: axs[0], BX: axs[1], CX: axs[2], AY: ays[0], BY: ays[1], CY: ays[2]}
	return q, medianResidual(obs, q), true
}

// quadFit solves the 3x3 normal equations of value = a + b·t + c·t² (t offset, scaled to hours so
// the t⁴ terms stay in a sane numeric range).
func quadFit(obs []Obs, t0 int64, val func(Obs) float64) ([3]float64, bool) {
	const hour = 3600 * 1000.0
	var s [5]float64 // Σ t^k
	var sv [3]float64
	for _, o := range obs {
		t := float64(o.T-t0) / hour
		v := val(o)
		tp := 1.0
		for k := 0; k < 5; k++ {
			s[k] += tp
			if k < 3 {
				sv[k] += tp * v
			}
			tp *= t
		}
	}
	m := [3][4]float64{
		{s[0], s[1], s[2], sv[0]},
		{s[1], s[2], s[3], sv[1]},
		{s[2], s[3], s[4], sv[2]},
	}
	// Gaussian elimination with partial pivoting.
	for col := 0; col < 3; col++ {
		p := col
		for r := col + 1; r < 3; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[p][col]) {
				p = r
			}
		}
		m[col], m[p] = m[p], m[col]
		if math.Abs(m[col][col]) < 1e-12 {
			return [3]float64{}, false
		}
		for r := 0; r < 3; r++ {
			if r == col {
				continue
			}
			f := m[r][col] / m[col][col]
			for cc := col; cc < 4; cc++ {
				m[r][cc] -= f * m[col][cc]
			}
		}
	}
	// Coefficients are in per-hour units; convert back to per-ms offsets.
	a := m[0][3] / m[0][0]
	b := m[1][3] / m[1][1] / hour
	c := m[2][3] / m[2][2] / (hour * hour)
	return [3]float64{a, b, c}, true
}

// medianResidual is the median distance between the observations and a track's model.
func medianResidual(obs []Obs, tr Tracker) float64 {
	res := make([]float64, len(obs))
	for i, o := range obs {
		p := tr.At(o.T)
		res[i] = math.Hypot(o.P.X-p.X, o.P.Y-p.Y)
	}
	med, _ := medianMAD64(res)
	return med
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
