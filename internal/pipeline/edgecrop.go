package pipeline

// edgecrop.go trims the ragged edge a registered stack always carries, before anything models the
// sky. It is the single-session counterpart of the coverage crop next door: that one knows the
// per-frame homographies (grouped runs only) and cuts to the field every channel covers; this one
// measures the finished stack's own edge and needs nothing but the pixels.
//
// The bug it exists for is worth stating, because the artefact is invisible and the damage is not.
// A 104-frame M31 stack that drifted 135 px came out with a sky flat to 0.06% — and a quarter of
// the final picture blown to white. Measured on the linear master, along the drift edge:
//
//	x = 0..32    a wedge of pixels no frame ever covered (exactly zero, 71% of the column at x=0)
//	x = 20..180  the sky sits ABOVE the interior level, peaking at x≈50
//	x > 200      the interior, flat to ±2σ
//
// The peak is +0.2% of the sky. In an image whose noise after 89 frames is 1e-5, +0.2% is **+46σ**:
// nothing to the eye, an enormous feature to anything fitting a surface. The per-channel GraXpert
// pass rode straight over it (its model is too smooth to represent a 50 px ridge, and it left the
// sky beautifully flat), and then the polynomial subsky and the combined GraXpert+RBF pass fitted
// the ridge instead of the sky, tilted the whole frame, and the stretch clipped the result.
//
// So the rule is: the ragged edge never reaches a background model. A line is border when either
// mark is on it, and the two cover different parts of the edge — the dead wedge stops at x≈50 and
// the skirt runs to x≈190:
//
//   - DEAD: a line still carrying pixels no frame covered.
//   - SKIRT: a line whose background sits off the fitted TREND by more than a few sigma AND whose
//     own noise is well above the interior's.
//
// That second clause is what makes the trim safe to leave on, and it is measured, not assumed. The
// skirt is where FEWER FRAMES CONTRIBUTED, so it is noisier in exact proportion — on the master
// above, 8 to 10x the interior noise out to x=100 and still 1.3 to 1.8x at x=160, against 1.1x deep
// inside. A real object touching the frame edge — a nebula filling the field — lifts the background
// there just as much but carries the SAME noise as the rest of the stack, so it fails this clause
// and is kept. Without it the level test alone would happily eat the edge of NGC 7000.
//
// The crop is applied to the colour-combine inputs, never to the persisted channel masters: a
// refine or a supervised re-entry re-reads the masters, and they must stay the frames the run
// recorded.

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	// edgeDeadLevel: a pixel below this fraction of the interior sky received no data at all.
	edgeDeadLevel = 0.05
	// edgeDeadFrac: a line with more than this share of dead pixels is still inside the border.
	// Deliberately small — one dead pixel in a thousand is still the wedge, not the sky.
	edgeDeadFrac = 0.002
	// edgeSigma: how far a line's background may sit from the interior trend before it reads as
	// skirt. 5σ of the line-to-line scatter, so the interior itself never trips it.
	edgeSigma = 5.0
	// edgeNoiseRatio: how much noisier than the interior a line must be to corroborate the level
	// test. Measured on the failing master: 8-10x through the wedge, 1.3-1.8x across the skirt,
	// 1.1x deep inside — so this sits just above the interior's own wobble.
	edgeNoiseRatio = 1.3
	// edgeStableLines: consecutive interior-like lines needed to call the border over. The skirt is
	// not monotonic (it dips and rises again between x=100 and x=150 on the measured stack), so a
	// single clean line is not evidence that it has ended.
	edgeStableLines = 32
	// edgeMaxTrimFrac: never cut more than this off one side. A measurement that wants more than an
	// eighth of the frame has misread something; the run says so and keeps the field.
	edgeMaxTrimFrac = 0.12
	// edgeLineSample: subsampling ACROSS each line. A background percentile does not need every
	// pixel, and this keeps a 24 Mpx master under a second.
	edgeLineSample = 4
	// edgeLinePct: the per-line background statistic. The MEDIAN, deliberately: a low percentile
	// looks safer but under-reads this artefact badly, because the skirt both lifts the level and
	// widens the distribution, so its low tail stays near the interior's. Measured on the failing
	// master, the skirt at x=100 reads +16.6σ at p50 and +1.0σ at p20 — p20 would have stopped the
	// trim at 71 px and left most of the damage in. The noise clause, not a timid percentile, is
	// what keeps an object safe.
	edgeLinePct = 0.50
	// edgeMinProfile: a profile shorter than this cannot support a trend fit plus a stable run.
	edgeMinProfile = 4 * edgeStableLines
)

// edgeRect is a half-open pixel rectangle [X0,X1) × [Y0,Y1).
type edgeRect struct{ X0, Y0, X1, Y1 int }

func (r edgeRect) w() int    { return r.X1 - r.X0 }
func (r edgeRect) h() int    { return r.Y1 - r.Y0 }
func (r edgeRect) area() int { return r.w() * r.h() }

// intersect narrows r to the part it shares with o.
func (r edgeRect) intersect(o edgeRect) edgeRect {
	return edgeRect{max(r.X0, o.X0), max(r.Y0, o.Y0), min(r.X1, o.X1), min(r.Y1, o.Y1)}
}

// measureEdgeTrim returns the largest rectangle of im free of the stack's ragged edge, and whether
// the measurement hit the per-side cap (which means it is a floor, not the answer).
//
// Columns and rows are measured alternately and TWICE: the uncovered region is a wedge, not a
// border, so trimming the columns changes what the row profile sees and vice versa.
func measureEdgeTrim(im *fits.Image) (edgeRect, bool) {
	r := edgeRect{0, 0, im.W, im.H}
	if im.W < edgeMinProfile || im.H < edgeMinProfile {
		return r, false
	}
	dead := float32(edgeDeadLevel * interiorSky(im))
	maxX := int(edgeMaxTrimFrac * float64(im.W))
	maxY := int(edgeMaxTrimFrac * float64(im.H))
	capped := false
	for pass := 0; pass < 2; pass++ {
		for _, columns := range []bool{true, false} {
			st := lineProfiles(im, r, columns, dead)
			trend, scale, ok := interiorTrend(st.Bg)
			if !ok {
				continue
			}
			// The cap is a budget per SIDE of the original frame, not per pass: the second pass
			// re-measures the already-narrowed rect and must not be able to spend the cap again.
			budget := [2]int{maxX - r.X0, maxX - (im.W - r.X1)}
			if !columns {
				budget = [2]int{maxY - r.Y0, maxY - (im.H - r.Y1)}
			}
			lo, hi, c := trimEnds(st, trend, scale, budget)
			capped = capped || c
			if columns {
				r.X0, r.X1 = r.X0+lo, r.X1-hi
			} else {
				r.Y0, r.Y1 = r.Y0+lo, r.Y1-hi
			}
		}
	}
	return r, capped
}

// interiorSky is the median of a coarse sample of the middle of the frame — the level everything
// else is expressed as a fraction of.
func interiorSky(im *fits.Image) float64 {
	x0, x1 := im.W/4, 3*im.W/4
	y0, y1 := im.H/4, 3*im.H/4
	var buf []float32
	for y := y0; y < y1; y += 16 {
		for x := x0; x < x1; x += 16 {
			buf = append(buf, meanChannels(im, y*im.W+x))
		}
	}
	return percentileOf(buf, 0.5)
}

// lineStats is what one scan of the image reduces each line to.
type lineStats struct {
	// Bg is the line's background level, Dead the share of its pixels that no frame covered, and
	// Noise its own pixel-to-pixel scatter — the depth proxy that tells the stack's shallow edge
	// apart from an object sitting on it.
	Bg, Dead, Noise []float64
}

// lineProfiles reduces r to one lineStats per line — per column when columns is true, else per row.
// Dead pixels are excluded from the level and noise estimates (a column that is 71% zeros has no
// meaningful median) because Dead already reports them.
func lineProfiles(im *fits.Image, r edgeRect, columns bool, dead float32) lineStats {
	n, across := r.w(), r.h()
	if !columns {
		n, across = r.h(), r.w()
	}
	st := lineStats{Bg: make([]float64, n), Dead: make([]float64, n), Noise: make([]float64, n)}
	buf := make([]float32, 0, across/edgeLineSample+1)
	diff := make([]float32, 0, across/edgeLineSample+1)
	for i := 0; i < n; i++ {
		buf = buf[:0]
		deadN, total := 0, 0
		for k := 0; k < across; k += edgeLineSample {
			x, y := r.X0+i, r.Y0+k
			if !columns {
				x, y = r.X0+k, r.Y0+i
			}
			total++
			if v := meanChannels(im, y*im.W+x); v <= dead {
				deadN++
			} else {
				buf = append(buf, v)
			}
		}
		st.Dead[i] = float64(deadN) / float64(max(1, total))
		st.Noise[i] = lineNoise(buf, diff)
		st.Bg[i] = percentileOf(buf, edgeLinePct)
	}
	return st
}

// lineNoise estimates a line's pixel-to-pixel noise from the MAD of its successive differences —
// the standard structure-blind estimator, so a gradient or an object crossing the line does not
// inflate it. scratch is reused across lines. NaN when the line is too short to measure.
func lineNoise(v []float32, scratch []float32) float64 {
	if len(v) < 16 {
		return math.NaN()
	}
	d := scratch[:0]
	for i := 1; i < len(v); i++ {
		d = append(d, float32(math.Abs(float64(v[i]-v[i-1]))))
	}
	return percentileOf(d, 0.5) * 1.4826 / math.Sqrt2
}

// interiorNoise is the reference the per-line noise is compared against: the median over the middle
// of the profile, where the stack is at full depth.
func interiorNoise(noise []float64) float64 {
	n := len(noise)
	v := make([]float64, 0, n)
	for i := n / 5; i < 4*n/5; i++ {
		if !math.IsNaN(noise[i]) {
			v = append(v, noise[i])
		}
	}
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	return v[len(v)/2]
}

// meanChannels averages the planes at a pixel index — vignetting and coverage are colour-blind, and
// averaging keeps a single hot channel from deciding where the border is.
func meanChannels(im *fits.Image, i int) float32 {
	if len(im.Pix) == 0 {
		return 0
	}
	var s float32
	for c := range im.Pix {
		s += im.Pix[c][i]
	}
	return s / float32(len(im.Pix))
}

// percentileOf sorts v IN PLACE (callers pass scratch) and returns the p-quantile; NaN when empty.
func percentileOf(v []float32, p float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
	return float64(v[int(p*float64(len(v)-1))])
}

// interiorTrend fits a quadratic to a line profile and returns the fit plus a robust scale of its
// residuals. This is what lets a genuine sky gradient through: the fit follows it, so only a
// departure from it — the stack's own step at the frame edge — is measured. ok is false when the
// profile carries too few usable lines to fit.
//
// Two properties of the fit are load-bearing, and both were learned the hard way:
//
// It is sigma-CLIPPED. A plain pass fits the edge skirt along with the sky, which is exactly the
// feature being looked for; clipping drops the skirt after one pass and leaves the sky's own shape.
//
// It spans the WHOLE profile rather than a trusted middle, so the trend is never EXTRAPOLATED to
// the edges. Fitting the middle 60% and evaluating at the ends looks safer and is not: a galaxy
// lifting the middle of the profile by three parts in a million tilts the quadratic enough that its
// extrapolation misses the corner by twelve sigma, and a clean frame gets trimmed from all four
// sides. Clipping is what makes spanning the whole profile safe.
func interiorTrend(bg []float64) (trend func(int) float64, scale float64, ok bool) {
	n := len(bg)
	u := func(i int) float64 { return (float64(i) - float64(n)/2) / (float64(n) / 2) }
	keep := make([]bool, n)
	for i := range keep {
		keep[i] = !math.IsNaN(bg[i])
	}
	for pass := 0; ; pass++ {
		c, fitted, solved := fitQuadratic(bg, keep, 0, n, u)
		if !solved || fitted < 3*edgeStableLines {
			return nil, 0, false
		}
		trend = func(i int) float64 { return c[0] + c[1]*u(i) + c[2]*u(i)*u(i) }
		scale = robustScale(bg, keep, 0, n, trend, c[0])
		if pass >= 2 || !clipOutliers(bg, keep, 0, n, trend, scale) {
			return trend, scale, scale > 0
		}
	}
}

// fitQuadratic solves the least-squares quadratic over the kept lines of [lo,hi).
func fitQuadratic(bg []float64, keep []bool, lo, hi int, u func(int) float64) ([3]float64, int, bool) {
	var a [3][4]float64
	fitted := 0
	for i := lo; i < hi; i++ {
		if !keep[i] {
			continue
		}
		fitted++
		t := [3]float64{1, u(i), u(i) * u(i)}
		for p := 0; p < 3; p++ {
			for q := 0; q < 3; q++ {
				a[p][q] += t[p] * t[q]
			}
			a[p][3] += t[p] * bg[i]
		}
	}
	c, solved := solve3(a)
	return c, fitted, solved
}

// robustScale is 1.4826·MAD of the kept residuals, floored in units of the level itself: a synthetic
// or heavily denoised frame can have a literally zero MAD, and without a floor every line off the
// trend by a rounding error would read as border.
func robustScale(bg []float64, keep []bool, lo, hi int, trend func(int) float64, level float64) float64 {
	res := make([]float64, 0, hi-lo)
	for i := lo; i < hi; i++ {
		if keep[i] {
			res = append(res, math.Abs(bg[i]-trend(i)))
		}
	}
	if len(res) == 0 {
		return 0
	}
	sort.Float64s(res)
	scale := 1.4826 * res[len(res)/2]
	if floor := 1e-6 * math.Abs(level); scale < floor {
		scale = floor
	}
	return scale
}

// clipOutliers drops lines further than 2.5 scales from the trend, reporting whether any were
// dropped (so the caller stops as soon as the fit is stable).
func clipOutliers(bg []float64, keep []bool, lo, hi int, trend func(int) float64, scale float64) bool {
	dropped := false
	for i := lo; i < hi; i++ {
		if keep[i] && math.Abs(bg[i]-trend(i)) > 2.5*scale {
			keep[i], dropped = false, true
		}
	}
	return dropped
}

// solve3 is Gaussian elimination with partial pivoting on a 3×4 augmented matrix.
func solve3(a [3][4]float64) ([3]float64, bool) {
	for col := 0; col < 3; col++ {
		p := col
		for r := col + 1; r < 3; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[p][col]) {
				p = r
			}
		}
		if math.Abs(a[p][col]) < 1e-12 {
			return [3]float64{}, false
		}
		a[col], a[p] = a[p], a[col]
		for r := 0; r < 3; r++ {
			if r == col {
				continue
			}
			f := a[r][col] / a[col][col]
			for k := col; k < 4; k++ {
				a[r][k] -= f * a[col][k]
			}
		}
	}
	var out [3]float64
	for i := 0; i < 3; i++ {
		out[i] = a[i][3] / a[i][i]
		if math.IsNaN(out[i]) || math.IsInf(out[i], 0) {
			return out, false
		}
	}
	return out, true
}

// trimEnds walks in from both ends of a profile and returns how many lines each end must give up,
// plus whether either hit its remaining budget.
func trimEnds(st lineStats, trend func(int) float64, scale float64, budget [2]int) (lo, hi int, capped bool) {
	ref := interiorNoise(st.Noise)
	border := func(i int) bool {
		if st.Dead[i] > edgeDeadFrac || math.IsNaN(st.Bg[i]) {
			return true
		}
		if math.Abs(st.Bg[i]-trend(i)) <= edgeSigma*scale {
			return false
		}
		// Off-level is not enough: an object touching the frame edge is off-level too. Only the
		// stack's own shallow edge is off-level AND noisier than the field it belongs to.
		return ref <= 0 || math.IsNaN(st.Noise[i]) || st.Noise[i] > edgeNoiseRatio*ref
	}
	n := len(st.Bg)
	lo = walkIn(n, border, func(i int) int { return i })
	hi = walkIn(n, border, func(i int) int { return n - 1 - i })
	for end, want := range [2]int{lo, hi} {
		if have := max(0, budget[end]); want > have {
			capped = true
			if end == 0 {
				lo = have
			} else {
				hi = have
			}
		}
	}
	if lo+hi >= n { // pathological: keep the field and let the cap flag say so
		return 0, 0, true
	}
	return lo, hi, capped
}

// walkIn counts the leading border lines in the order at(0), at(1)… — the count stops at the start
// of the first run of edgeStableLines interior lines, so a lone clean line inside the skirt does
// not end the walk.
func walkIn(n int, border func(int) bool, at func(int) int) int {
	trim, good := 0, 0
	for i := 0; i < n; i++ {
		if border(at(i)) {
			good, trim = 0, i+1
			continue
		}
		if good++; good >= edgeStableLines {
			break
		}
	}
	return trim
}

// applyEdgeCrop crops every colour-combine input to the field the stack covers cleanly, returning
// the channels map pointing at the cropped copies trim_<tag>.fits — the same contract as
// applyCoverageCrop, under a DIFFERENT name so that running after it never reads and writes one
// file. Channels are cut to their COMMON rectangle so the combine still sees one geometry. Inert
// when the preset disables it or nothing needs cutting; a failure warns and combines uncropped.
func applyEdgeCrop(opts Options, res *Result, channels map[string]string, outDir string) map[string]string {
	if opts.Preset == nil || !opts.Preset.EdgeCrop || len(channels) == 0 {
		return channels
	}
	common, full, capped := edgeRect{}, edgeRect{}, false
	for _, base := range channels {
		im, err := fits.ReadImage(filepath.Join(outDir, base+".fits"))
		if err != nil {
			warnLive(opts, res, "edge trim skipped: "+err.Error())
			return channels
		}
		r, c := measureEdgeTrim(im)
		capped = capped || c
		if full.area() == 0 {
			common, full = r, edgeRect{0, 0, im.W, im.H}
		} else {
			common = common.intersect(r)
		}
	}
	if common.w() <= 0 || common.h() <= 0 {
		warnLive(opts, res, "edge trim skipped: the channels share no clean field")
		return channels
	}
	if capped {
		warnLive(opts, res, fmt.Sprintf(
			"the stack's ragged edge reaches past %.0f%% of the frame — trimming that much and no more; "+
				"the sky model may still see a step", edgeMaxTrimFrac*100))
	}
	if common == full {
		return channels
	}
	out := make(map[string]string, len(channels))
	for f, base := range channels {
		dst := "trim_" + filterTag(f)
		if err := cropFITS(filepath.Join(outDir, base+".fits"), filepath.Join(outDir, dst+".fits"),
			common.X0, common.Y0, common.X1, common.Y1); err != nil {
			warnLive(opts, res, fmt.Sprintf("edge trim failed on %s (%v) — combining uncropped", f, err))
			return channels
		}
		out[f] = dst
	}
	opts.report(Progress{Line: fmt.Sprintf(
		"✂ trimmed the stacking edge: %d×%d → %d×%d px (left %d, right %d, top %d, bottom %d)",
		full.w(), full.h(), common.w(), common.h(),
		common.X0, full.X1-common.X1, common.Y0, full.Y1-common.Y1)})
	res.EdgeCrop = &CombineCrop{X: common.X0, Y: common.Y0, W: common.w(), H: common.h(),
		Frac: float64(common.area()) / float64(max(1, full.area())), Applied: true}
	return out
}
