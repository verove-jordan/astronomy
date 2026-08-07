package stackalg

// Frame-count bounds behind the automatic rejection choice. Verified against Siril 1.4.3 (see the
// live syntax test): percentile logs "percentile clipping low/high", generalized logs
// "GESDT clipping outliers/significance".
const (
	AutoPercentileMax = 7  // ≤7 frames: sigma estimates are too unstable to clip on
	AutoGESDMin       = 50 // ≥50 frames: GESD markedly outperforms winsorized on outlier tails
)

// SigmaMax is the shared upper clamp for the two rejection parameters. The USABLE range is
// per-algorithm (a percentile fraction never exceeds 1) and lives in RejectInfo; this single bound
// is what the knob whitelist clamps to, so the stored value is always exactly what the user asked
// for and the algorithm's own limits are applied at render time (see Resolve).
const SigmaMax = 10.0

// ParamKind labels what a rejection parameter means, so the UI can render the right control and
// unit without a second table.
type ParamKind string

const (
	ParamSigma    ParamKind = "sigma"    // a multiple of the pixel's noise sigma
	ParamFraction ParamKind = "fraction" // a fraction of the samples, 0–1
	ParamAlpha    ParamKind = "alpha"    // a statistical significance level, 0–1
)

// RejectParam describes one of a rejection algorithm's two parameters.
type RejectParam struct {
	Kind    ParamKind `json:"kind"`
	Default float64   `json:"default"`
	Min     float64   `json:"min"`
	Max     float64   `json:"max"`
}

// RejectInfo is one row of the rejection catalogue: which engines implement the algorithm, what
// its two parameters mean, and the frame-count band it is the best choice in.
type RejectInfo struct {
	ID      Reject   `json:"id"`
	Engines []Engine `json:"engines"`
	// SirilToken is the word Siril's `stack … rej <token>` grammar expects (empty when Siril
	// cannot run this algorithm).
	SirilToken string `json:"siril_token,omitempty"`
	// HasParams is false for the algorithms that take no sigma pair (none).
	HasParams bool        `json:"has_params"`
	Low       RejectParam `json:"low,omitempty"`
	High      RejectParam `json:"high,omitempty"`
	// BestFrom/BestTo bound the frame count this algorithm is recommended for (0 = unbounded).
	BestFrom int `json:"best_from,omitempty"`
	BestTo   int `json:"best_to,omitempty"`
}

// CombineInfo is one row of the combination catalogue.
type CombineInfo struct {
	ID      Combine  `json:"id"`
	Engines []Engine `json:"engines"`
	// SirilToken is the word Siril's `stack <seq> <token>` grammar expects.
	SirilToken string `json:"siril_token,omitempty"`
	// Rejects is false for the methods that combine every sample unconditionally (sum/min/max/
	// median); Siril refuses a rejection clause, a weight or a normalization on sum/min/max.
	Rejects bool `json:"rejects"`
	// Normalizes is false for sum/min/max, which Siril accepts no normalization flag for.
	Normalizes bool `json:"normalizes"`
}

var (
	siril  = []Engine{EngineSiril, EngineNative}
	native = []Engine{EngineNative}
)

// combines is the combination catalogue, in the order the UI lists it.
var combines = []CombineInfo{
	{ID: CombineMean, Engines: siril, SirilToken: "rej", Rejects: true, Normalizes: true},
	{ID: CombineMedian, Engines: siril, SirilToken: "med", Normalizes: true},
	{ID: CombineSum, Engines: siril, SirilToken: "sum"},
	{ID: CombineMax, Engines: siril, SirilToken: "max"},
	{ID: CombineMin, Engines: siril, SirilToken: "min"},
	{ID: CombineTrimmedMean, Engines: native, Rejects: true, Normalizes: true},
}

// rejects is the rejection catalogue, in the order the UI lists it.
var rejects = []RejectInfo{
	{ID: RejectNone, Engines: siril, SirilToken: "none"},
	{
		ID: RejectPercentile, Engines: siril, SirilToken: "percentile", HasParams: true,
		Low:    RejectParam{Kind: ParamFraction, Default: 0.2, Min: 0.01, Max: 0.9},
		High:   RejectParam{Kind: ParamFraction, Default: 0.1, Min: 0.01, Max: 0.9},
		BestTo: AutoPercentileMax,
	},
	{
		ID: RejectSigma, Engines: siril, SirilToken: "sigma", HasParams: true,
		Low:      RejectParam{Kind: ParamSigma, Default: 3, Min: 0.5, Max: SigmaMax},
		High:     RejectParam{Kind: ParamSigma, Default: 3, Min: 0.5, Max: SigmaMax},
		BestFrom: 8,
	},
	{
		ID: RejectMedianSigma, Engines: siril, SirilToken: "median", HasParams: true,
		Low:      RejectParam{Kind: ParamSigma, Default: 3, Min: 0.5, Max: SigmaMax},
		High:     RejectParam{Kind: ParamSigma, Default: 3, Min: 0.5, Max: SigmaMax},
		BestFrom: 8,
	},
	{
		ID: RejectWinsorized, Engines: siril, SirilToken: "winsorized", HasParams: true,
		Low:      RejectParam{Kind: ParamSigma, Default: 3, Min: 0.5, Max: SigmaMax},
		High:     RejectParam{Kind: ParamSigma, Default: 3, Min: 0.5, Max: SigmaMax},
		BestFrom: AutoPercentileMax + 1, BestTo: AutoGESDMin - 1,
	},
	{
		ID: RejectLinearFit, Engines: siril, SirilToken: "linear", HasParams: true,
		Low:      RejectParam{Kind: ParamSigma, Default: 5, Min: 0.5, Max: SigmaMax},
		High:     RejectParam{Kind: ParamSigma, Default: 3.5, Min: 0.5, Max: SigmaMax},
		BestFrom: 15,
	},
	{
		ID: RejectGESD, Engines: siril, SirilToken: "generalized", HasParams: true,
		Low:      RejectParam{Kind: ParamFraction, Default: 0.3, Min: 0.01, Max: 0.9},
		High:     RejectParam{Kind: ParamAlpha, Default: 0.05, Min: 0.001, Max: 0.5},
		BestFrom: AutoGESDMin,
	},
	{
		ID: RejectMAD, Engines: siril, SirilToken: "mad", HasParams: true,
		Low:      RejectParam{Kind: ParamSigma, Default: 3, Min: 0.5, Max: SigmaMax},
		High:     RejectParam{Kind: ParamSigma, Default: 3, Min: 0.5, Max: SigmaMax},
		BestFrom: 8,
	},
	{
		ID: RejectRCR, Engines: native, HasParams: true,
		Low:      RejectParam{Kind: ParamAlpha, Default: 0.5, Min: 0.01, Max: 1},
		High:     RejectParam{Kind: ParamAlpha, Default: 0.5, Min: 0.01, Max: 1},
		BestFrom: 10,
	},
	{ID: RejectAdaptiveWeighted, Engines: native, BestFrom: 8},
	{ID: RejectEntropyWeighted, Engines: native, BestFrom: 8},
}

// norms and weights are the remaining enum menus; they carry no parameters of their own.
var (
	norms   = []Norm{NormNone, NormAdd, NormAddScale, NormMul, NormMulScale}
	weights = []Weight{WeightNone, WeightNoise, WeightWFWHM, WeightNbStars, WeightNbStack}
)

// Combines returns the combination catalogue in display order.
func Combines() []CombineInfo { return append([]CombineInfo(nil), combines...) }

// Rejects returns the rejection catalogue in display order.
func Rejects() []RejectInfo { return append([]RejectInfo(nil), rejects...) }

// Norms returns the normalization menu in display order.
func Norms() []Norm { return append([]Norm(nil), norms...) }

// Weights returns the per-frame weighting menu in display order.
func Weights() []Weight { return append([]Weight(nil), weights...) }

// CombineOf looks up a combination method; ok is false for an unknown id.
func CombineOf(c Combine) (CombineInfo, bool) {
	if c == CombineAuto {
		c = CombineMean
	}
	for _, info := range combines {
		if info.ID == c {
			return info, true
		}
	}
	return CombineInfo{}, false
}

// RejectOf looks up a rejection algorithm; ok is false for an unknown id. RejectAuto must be
// resolved (see AutoReject) before lookup.
func RejectOf(r Reject) (RejectInfo, bool) {
	for _, info := range rejects {
		if info.ID == r {
			return info, true
		}
	}
	return RejectInfo{}, false
}

// AutoReject is the count-adaptive choice the engine has always made: percentile clipping for tiny
// stacks (where sigma estimates are meaningless), Winsorized sigma clipping for the common middle,
// and the Generalized Extreme Studentized Deviate test for large stacks — where it removes the
// correlated outliers (walking noise from drifting fixed-pattern residuals, trail remnants) a 3σ
// winsorized clip leaves behind. frames <= 0 (count unknown) keeps the long-proven winsorized default.
func AutoReject(frames int) Reject {
	switch {
	case frames > 0 && frames <= AutoPercentileMax:
		return RejectPercentile
	case frames >= AutoGESDMin:
		return RejectGESD
	default:
		return RejectWinsorized
	}
}

// SupportedBy reports whether an engine implements a rejection algorithm.
func (info RejectInfo) SupportedBy(e Engine) bool { return hasEngine(info.Engines, e) }

// SupportedBy reports whether an engine implements a combination method.
func (info CombineInfo) SupportedBy(e Engine) bool { return hasEngine(info.Engines, e) }

func hasEngine(list []Engine, e Engine) bool {
	for _, x := range list {
		if x == e {
			return true
		}
	}
	return false
}

// IsNorm reports whether s names a supported normalization ("" = auto).
func IsNorm(s Norm) bool { return s == NormAuto || contains(norms, s) }

// IsWeight reports whether s names a supported per-frame weighting ("" = unweighted).
func IsWeight(s Weight) bool { return contains(weights, s) }

func contains[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
