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
}

// Options are the rejection thresholds. Zero value is not useful; use DefaultOptions.
type Options struct {
	RoundnessMin    float64 // reject if roundness < this (elongated stars)
	FWHMSigma       float64 // reject if FWHM > median + k·MADσ (soft frames)
	BackgroundSigma float64 // reject if background > median + k·MADσ (sky glow)
	StarCountFrac   float64 // reject if star count < frac·median (clouds)
	RejectTrails    bool    // reject frames with a detected trail
}

// DefaultOptions returns sensible robust defaults.
func DefaultOptions() Options {
	return Options{
		RoundnessMin:    0.85,
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

// Grade applies the rejection rules to metrics in place. Robust (median + MAD) rules only run
// with enough frames. As a safety net it never rejects every frame — if all are flagged, the
// sharpest (lowest FWHM) is kept.
func Grade(metrics []Metric, opts Options) {
	// Statistics are over successfully-registered frames only (FWHM > 0); unregistered frames
	// are left as the caller marked them and excluded from the medians.
	var fwhms, bgs, stars []float64
	for _, m := range metrics {
		if m.FWHM > 0 {
			fwhms = append(fwhms, m.FWHM)
			bgs = append(bgs, m.Background)
			stars = append(stars, float64(m.StarCount))
		}
	}
	n := len(fwhms)
	if n == 0 {
		return
	}
	fwhmMed, fwhmMAD := medianMAD(fwhms)
	bgMed, bgMAD := medianMAD(bgs)
	starMed := medianOf(stars)

	for i := range metrics {
		m := &metrics[i]
		if m.FWHM <= 0 {
			continue // unregistered: already rejected by the caller
		}
		var reasons []string
		if opts.RejectTrails && m.TrailDetected {
			reasons = append(reasons, fmt.Sprintf("trail detected (score %.2f)", m.TrailScore))
		}
		if m.Roundness > 0 && m.Roundness < opts.RoundnessMin {
			reasons = append(reasons, fmt.Sprintf("elongated stars (roundness %.2f < %.2f)", m.Roundness, opts.RoundnessMin))
		}
		if n >= minFramesForStats {
			// Soft frame: meaningfully worse than median AND a statistical outlier. The relative
			// gate keeps tight sets safe; the MAD term still applies when there is real spread
			// (and degenerates to "> median" when MAD is 0, e.g. 4 identical frames + 1 outlier).
			if fwhmMed > 0 &&
				m.FWHM > fwhmMed*(1+minRelFWHM) && m.FWHM > fwhmMed+opts.FWHMSigma*fwhmMAD {
				reasons = append(reasons, fmt.Sprintf("soft frame (FWHM %.2f vs median %.2f)", m.FWHM, fwhmMed))
			}
			// High sky background: only when the background level is a clear positive outlier.
			if bgMed > 0 && bgMAD > 0 &&
				m.Background > bgMed+opts.BackgroundSigma*bgMAD && m.Background > 1.5*bgMed {
				reasons = append(reasons, "high sky background")
			}
			// Clouds / transparency loss: far fewer stars than typical.
			if starMed >= minStarsForRule && float64(m.StarCount) < opts.StarCountFrac*starMed {
				reasons = append(reasons, fmt.Sprintf("few stars (%d vs median %.0f) — likely clouds", m.StarCount, starMed))
			}
		}
		if len(reasons) > 0 {
			m.Rejected = true
			m.RejectReason = joinReasons(reasons)
		}
	}
	keepAtLeastOne(metrics)
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

// keepAtLeastOne ensures the stack is never empty: if every registered frame was rejected, the
// sharpest one (lowest FWHM) is un-rejected.
func keepAtLeastOne(metrics []Metric) {
	best := -1
	for i := range metrics {
		if metrics[i].FWHM <= 0 {
			continue
		}
		if !metrics[i].Rejected {
			return // at least one registered frame survives
		}
		if best == -1 || metrics[i].FWHM < metrics[best].FWHM {
			best = i
		}
	}
	if best >= 0 {
		metrics[best].Rejected = false
		metrics[best].RejectReason = ""
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
