// Package channeldetect infers which filter each light frame belongs to when the capture is
// unlabeled (no FILTER in header/filename/folder). It is pure (stdlib plus the dependency-free
// internal/filters token table): callers supply a per-frame signal Fingerprint, and it returns
// per-frame filter Assignments plus the run grouping.
//
// The method exploits the fact that a motorized filter wheel always turns in a fixed cyclic order
// (e.g. L→R→G→B→Ha and wrap): frames are captured in contiguous same-filter runs, runs advance
// forward through that order, and the broadband/narrowband extremes (brightest = L, faintest = Ha)
// anchor the cycle. R/G/B cannot be told apart by signal alone, so they are resolved purely by their
// position in the forward walk — never by brightness.
package channeldetect

import (
	"math"
	"sort"
)

// Fingerprint is the signal character of one frame, used to group and rank frames by filter.
type Fingerprint struct {
	Background   float64 // robust sky level (median ADU)
	Flux         float64 // bright-signal proxy (P90 − median)
	StarRichness float64 // fraction of bright pixels (broadband ≫ narrowband)
	Noise        float64 // robust noise (MAD)
	ExposureMs   int64   // exposure; a change marks a filter/run boundary
}

// Sample is one light frame to classify, with its acquisition-order key.
type Sample struct {
	Order int64 // DATE-OBS ms (fallback: filename frame index, then mtime)
	Path  string
	FP    Fingerprint
}

// Assignment is the detected filter for one frame.
type Assignment struct {
	Path            string  `json:"path"`
	Filter          string  `json:"filter"`
	RunIndex        int     `json:"run_index"`
	Confidence      float64 `json:"confidence"`
	WheelTransition bool    `json:"wheel_transition,omitempty"`
	TransitionDelta float64 `json:"transition_delta,omitempty"` // relative background deviation
}

// Run is a contiguous group of same-filter frames (one wheel dwell).
type Run struct {
	Index      int      `json:"index"`
	Filter     string   `json:"filter"`
	Paths      []string `json:"-"`
	Count      int      `json:"count"`
	Confidence float64  `json:"confidence"`
	Mean       Fingerprint
}

// Result is the full detection output.
type Result struct {
	Order             []string
	Runs              []Run
	Assignments       []Assignment
	OverallConfidence float64
}

// Options tunes segmentation, assignment and wheel-transition detection.
type Options struct {
	Order               []string // cyclic wheel order, e.g. {"L","R","G","B","Ha"}
	TimeGapFactor       float64  // new run when the inter-frame gap > factor × median gap
	FPBreakSigma        float64  // new run when the signal jump > median + sigma·MAD of jumps
	TransitionLookahead int      // frames after the first used as the run's reference level
	TransitionRelFloor  float64  // min relative background deviation to flag a transition
	TransitionSigma     float64  // background deviation in MADs to flag a transition
	DetectTransitions   bool     // enable conditional first-of-run flagging
}

// DefaultOptions returns robust defaults for block-captured deep-sky sequences.
//
// Order stops at Ha ON PURPOSE, even though the pipeline handles OIII and SII as well: signal
// detection cannot tell one emission line from another — Ha, OIII and SII are all faint AND
// star-poor, which is the only evidence this package has — so adding them as extra cyclic states
// would let the DP scatter a genuine Ha run across three indistinguishable labels. Callers with a
// wider wheel should pass the real slot order in Options.Order.
//
// Naming a PHYSICAL wheel slot is a different question and does use the full canonical set — see
// inspect.defaultSlotLegend.
func DefaultOptions() Options {
	return Options{
		Order:               []string{"L", "R", "G", "B", "Ha"},
		TimeGapFactor:       4,
		FPBreakSigma:        3,
		TransitionLookahead: 4,
		TransitionRelFloor:  0.12,
		TransitionSigma:     3,
		DetectTransitions:   true,
	}
}

// Detect orders the samples by acquisition time, segments them into same-filter runs, assigns a
// filter to each run by walking the fixed cyclic wheel order anchored by signal, and (optionally)
// flags off-brightness first-of-run frames as filter-wheel transitions.
func Detect(samples []Sample, opts Options) Result {
	if len(opts.Order) == 0 {
		opts.Order = DefaultOptions().Order
	}
	if len(samples) == 0 {
		return Result{Order: opts.Order}
	}

	ordered := make([]Sample, len(samples))
	copy(ordered, samples)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })

	runs := segmentRuns(ordered, opts)
	filters, runConf, overall := assignCyclic(ordered, runs, opts.Order)

	res := Result{Order: opts.Order, OverallConfidence: overall}
	res.Runs = make([]Run, len(runs))
	res.Assignments = make([]Assignment, 0, len(ordered))
	for r, idxs := range runs {
		paths := make([]string, len(idxs))
		for k, i := range idxs {
			paths[k] = ordered[i].Path
		}
		res.Runs[r] = Run{
			Index: r, Filter: filters[r], Paths: paths, Count: len(idxs),
			Confidence: runConf[r], Mean: meanFingerprint(ordered, idxs),
		}
		for _, i := range idxs {
			res.Assignments = append(res.Assignments, Assignment{
				Path: ordered[i].Path, Filter: filters[r], RunIndex: r, Confidence: runConf[r],
			})
		}
	}
	if opts.DetectTransitions {
		flagTransitions(ordered, runs, &res, opts)
	}
	return res
}

// level is the brightness scalar used to anchor the cycle: broadband frames sit high, narrowband low.
func level(fp Fingerprint) float64 { return fp.Background + fp.Flux }

func meanFingerprint(s []Sample, idxs []int) Fingerprint {
	var m Fingerprint
	if len(idxs) == 0 {
		return m
	}
	for _, i := range idxs {
		m.Background += s[i].FP.Background
		m.Flux += s[i].FP.Flux
		m.StarRichness += s[i].FP.StarRichness
		m.Noise += s[i].FP.Noise
	}
	n := float64(len(idxs))
	m.Background /= n
	m.Flux /= n
	m.StarRichness /= n
	m.Noise /= n
	m.ExposureMs = s[idxs[0]].FP.ExposureMs
	return m
}

// --- small robust statistics (stdlib only) ---

func medianOf(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	return cp[len(cp)/2]
}

// medianMAD returns the median and the median absolute deviation from it.
func medianMAD(vals []float64) (med, mad float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	med = medianOf(vals)
	devs := make([]float64, len(vals))
	for i, v := range vals {
		devs[i] = math.Abs(v - med)
	}
	return med, medianOf(devs)
}

// minMaxNorm scales a slice to [0,1]; a flat slice maps to all-0.5 (no information).
func minMaxNorm(vals []float64) []float64 {
	out := make([]float64, len(vals))
	if len(vals) == 0 {
		return out
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	span := hi - lo
	for i, v := range vals {
		if span <= 0 {
			out[i] = 0.5
			continue
		}
		out[i] = (v - lo) / span
	}
	return out
}
