package grade

import (
	"fmt"
	"sort"
)

// Metric is a graded sub-frame: its registration metrics, trail status, and the keep/reject
// decision with a human-readable reason.
type Metric struct {
	Index         int     `json:"index"` // 1-based position in the Siril sequence
	Path          string  `json:"path"`
	FWHM          float64 `json:"fwhm"`
	WFWHM         float64 `json:"wfwhm"`
	Roundness     float64 `json:"roundness"`
	StarCount     int     `json:"star_count"`
	Background    float64 `json:"background"`
	Quality       float64 `json:"quality"`
	TrailDetected bool    `json:"trail_detected"`
	TrailScore    float64 `json:"trail_score"`
	Rejected      bool    `json:"rejected"`
	RejectReason  string  `json:"reject_reason,omitempty"`
	// ShiftX/ShiftY are the frame's registration translation vs the reference (pixels) — the
	// capture-time pointing offsets the dither/drift diagnostic reads.
	ShiftX float64 `json:"shift_x,omitempty"`
	ShiftY float64 `json:"shift_y,omitempty"`
}

// Options are the rejection thresholds. Zero value is not useful; use DefaultOptions.
type Options struct {
	RoundnessFloor  float64 // always reject if roundness < this (clearly trailed/elongated)
	RoundnessSigma  float64 // also reject if roundness < median − k·MADσ (worse than the session)
	FWHMSigma       float64 // reject if FWHM > median + k·MADσ (soft frames)
	BackgroundSigma float64 // reject if background > median + k·MADσ (sky glow)
	StarCountFrac   float64 // reject if star count < frac·median (clouds)
	RejectTrails    bool    // reject frames with a detected trail
}

// DefaultOptions returns sensible robust defaults. Roundness is judged relative to the session
// (real subs are commonly ~0.8 round, so a fixed high cutoff would reject everything); only
// clearly-trailed frames (below the floor) or per-session outliers are dropped.
func DefaultOptions() Options {
	return Options{
		RoundnessFloor:  0.55,
		RoundnessSigma:  2.5,
		FWHMSigma:       2.5,
		BackgroundSigma: 3.0,
		StarCountFrac:   0.5,
		RejectTrails:    true,
	}
}

const (
	// minFramesForStats is the smallest sample where MAD-based outlier rejection is meaningful.
	minFramesForStats = 4
	// minRelFWHM requires a "soft" frame to be at least this fraction worse than the median,
	// so trivially-different frames in a very tight set are not rejected.
	minRelFWHM = 0.10
	// minStarsForRule avoids the cloud rule firing on frames that simply have few stars.
	minStarsForRule = 8.0
)

// Span delimits one calibration group's frames inside the merged metric order ([Start,End)) — the
// population the RELATIVE rules are scoped to when several capture nights merge.
type Span struct{ Start, End int }

// populationStats are the robust statistics of one population, feeding the relative rules.
type populationStats struct {
	n                  int
	fwhmMed, fwhmMAD   float64
	bgMed, bgMAD       float64
	roundMed, roundMAD float64
	starMed            float64
}

// statsOf measures a population over its successfully-registered frames only (FWHM > 0);
// unregistered frames are left as the caller marked them and excluded from the medians.
func statsOf(metrics []Metric) populationStats {
	var fwhms, bgs, rounds, stars []float64
	for _, m := range metrics {
		if m.FWHM > 0 {
			fwhms = append(fwhms, m.FWHM)
			bgs = append(bgs, m.Background)
			rounds = append(rounds, m.Roundness)
			stars = append(stars, float64(m.StarCount))
		}
	}
	st := populationStats{n: len(fwhms)}
	if st.n == 0 {
		return st
	}
	st.fwhmMed, st.fwhmMAD = medianMAD(fwhms)
	st.bgMed, st.bgMAD = medianMAD(bgs)
	st.roundMed, st.roundMAD = medianMAD(rounds)
	st.starMed = medianOf(stars)
	return st
}

// Grade applies the rejection rules to metrics in place as ONE population. Robust (median + MAD)
// rules only run with enough frames. As a safety net it never leaves fewer than stackMinimum
// registered frames (Siril's stack floor) — the best flagged frames are restored, keeping their
// reason as provenance.
func Grade(metrics []Metric, opts Options) {
	GradeGrouped(metrics, nil, opts)
}

// GradeGrouped is Grade with the RELATIVE rules scoped per span — one span per capture night in a
// multi-night merge — so each night's frames are judged against their OWN median seeing/sky/
// roundness/star count. Graded as one population, the sharpest night's median evicts every other
// night wholesale (task #354: G stacked 20/56 — the "few stars"/"elongated vs session" bars were
// set by the one best night). ABSOLUTE rules (trail detection, the roundness floor) apply per
// frame regardless, and the stack-minimum restore stays GLOBAL (Siril's floor is a whole-stack
// constraint). nil or empty spans mean one population — identical to Grade.
func GradeGrouped(metrics []Metric, spans []Span, opts Options) {
	if len(spans) == 0 {
		spans = []Span{{Start: 0, End: len(metrics)}}
	}
	for _, sp := range spans {
		lo := max(0, sp.Start)
		hi := min(len(metrics), sp.End)
		if lo < hi {
			gradeSpan(metrics[lo:hi], opts)
		}
	}
	keepAtLeast(metrics, stackMinimum)
}

// gradeSpan applies the rejection rules within one population.
func gradeSpan(metrics []Metric, opts Options) {
	st := statsOf(metrics)
	if st.n == 0 {
		return
	}
	for i := range metrics {
		rejectFrame(&metrics[i], st, opts)
	}
}

// rejectFrame applies the per-frame rules against its population's statistics, marking the metric
// in place.
func rejectFrame(m *Metric, st populationStats, opts Options) {
	if m.FWHM <= 0 {
		return // unregistered: already rejected by the caller
	}
	var reasons []string
	if opts.RejectTrails && m.TrailDetected {
		reasons = append(reasons, fmt.Sprintf("trail detected (score %.2f)", m.TrailScore))
	}
	if m.Roundness > 0 {
		if m.Roundness < opts.RoundnessFloor {
			reasons = append(reasons, fmt.Sprintf("trailed/elongated stars (roundness %.2f < %.2f)", m.Roundness, opts.RoundnessFloor))
		} else if st.n >= minFramesForStats && st.roundMAD > 0 && m.Roundness < st.roundMed-opts.RoundnessSigma*st.roundMAD {
			reasons = append(reasons, fmt.Sprintf("elongated vs session (roundness %.2f < median %.2f)", m.Roundness, st.roundMed))
		}
	}
	if st.n >= minFramesForStats {
		// Soft frame: meaningfully worse than median AND a statistical outlier. The relative
		// gate keeps tight sets safe; the MAD term still applies when there is real spread
		// (and degenerates to "> median" when MAD is 0, e.g. 4 identical frames + 1 outlier).
		if st.fwhmMed > 0 &&
			m.FWHM > st.fwhmMed*(1+minRelFWHM) && m.FWHM > st.fwhmMed+opts.FWHMSigma*st.fwhmMAD {
			reasons = append(reasons, fmt.Sprintf("soft frame (FWHM %.2f vs median %.2f)", m.FWHM, st.fwhmMed))
		}
		// High sky background: only when the background level is a clear positive outlier.
		if st.bgMed > 0 && st.bgMAD > 0 &&
			m.Background > st.bgMed+opts.BackgroundSigma*st.bgMAD && m.Background > 1.5*st.bgMed {
			reasons = append(reasons, "high sky background")
		}
		// Clouds / transparency loss: far fewer stars than typical.
		if st.starMed >= minStarsForRule && float64(m.StarCount) < opts.StarCountFrac*st.starMed {
			reasons = append(reasons, fmt.Sprintf("few stars (%d vs median %.0f) — likely clouds", m.StarCount, st.starMed))
		}
	}
	if len(reasons) > 0 {
		m.Rejected = true
		m.RejectReason = joinReasons(reasons)
	}
}

// Kept returns the metrics that survived grading.
func Kept(metrics []Metric) []Metric {
	var out []Metric
	for _, m := range metrics {
		if !m.Rejected {
			out = append(out, m)
		}
	}
	return out
}

// RejectedIndices returns the 1-based sequence indices to unselect before stacking.
func RejectedIndices(metrics []Metric) []int {
	var out []int
	for _, m := range metrics {
		if m.Rejected {
			out = append(out, m.Index)
		}
	}
	return out
}

// stackMinimum is the fewest frames Siril's `stack -filter-incl` accepts: filtering below two
// images fails the whole script, so grading must never leave a lone survivor it could have
// avoided.
const stackMinimum = 2

// KeptStackMinimumPrefix marks a metric's RejectReason when grading restored the frame to honor
// the stack minimum; the original reason follows it. The pipeline parses it to warn live.
const KeptStackMinimumPrefix = "kept (stack minimum) — was: "

// keepAtLeast ensures at least n registered frames survive grading (capped by how many frames
// registered at all). While short, the sharpest (lowest FWHM) rejected registered frame is
// restored; its original reason is kept as provenance so the report still explains the flag.
func keepAtLeast(metrics []Metric, n int) {
	registered, survivors := 0, 0
	for i := range metrics {
		if metrics[i].FWHM <= 0 {
			continue // unregistered: can never be stacked
		}
		registered++
		if !metrics[i].Rejected {
			survivors++
		}
	}
	if n > registered {
		n = registered
	}
	for survivors < n {
		best := -1
		for i := range metrics {
			m := metrics[i]
			if m.FWHM <= 0 || !m.Rejected {
				continue
			}
			if best == -1 || m.FWHM < metrics[best].FWHM {
				best = i
			}
		}
		if best == -1 {
			return
		}
		metrics[best].Rejected = false
		metrics[best].RejectReason = KeptStackMinimumPrefix + metrics[best].RejectReason
		survivors++
	}
}

func medianOf(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	return cp[len(cp)/2]
}

func joinReasons(r []string) string {
	out := r[0]
	for _, s := range r[1:] {
		out += "; " + s
	}
	return out
}
