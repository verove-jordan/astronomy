// Package stackalg is the canonical catalogue of the frame-combination and pixel-rejection
// algorithms AstroStack can run, plus the option set every stacking call site reads.
//
// It is the single source of truth behind three surfaces that must never drift apart: the Siril
// command the engine emits (internal/siril), the tunable-knob whitelist the API validates and
// clamps (internal/pipeline), and the algorithm menu the UI renders (GET /api/mode-params). The
// package is engine-free — it imports only the standard library — so mode, siril, pipeline, calib
// and preset can all depend on it without an import cycle.
//
// The zero value of every enum means "auto": an Options{} left untouched resolves to exactly the
// behaviour the engine had before these knobs existed (count-adaptive rejection, additive-with-
// scaling normalization), so an un-configured run is byte-identical to a legacy one.
package stackalg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Engine is who performs the pixel combination.
type Engine string

const (
	// EngineAuto picks Siril unless the chosen algorithm is native-only. The zero value ("") means
	// the same thing — it is what an un-configured preset carries.
	EngineAuto Engine = "auto"
	// EngineSiril drives Siril's own `stack` command — the proven, fast path.
	EngineSiril Engine = "siril"
	// EngineNative combines in Go (internal/stacknative) over the frames Siril has already
	// registered. It exists for the algorithms Siril does not implement.
	EngineNative Engine = "native"
)

// Combine is how the surviving samples of a pixel are reduced to one value.
type Combine string

const (
	CombineAuto        Combine = ""             // = CombineMean
	CombineMean        Combine = "mean"         // average, optionally with rejection
	CombineMedian      Combine = "median"       // robust, ~20% less SNR than the mean
	CombineSum         Combine = "sum"          // pure addition, no rejection, no normalization
	CombineMin         Combine = "min"          // darkest sample per pixel
	CombineMax         Combine = "max"          // brightest sample per pixel (star trails, meteors)
	CombineTrimmedMean Combine = "trimmed_mean" // mean after dropping a fraction at each end (native)
)

// Reject is the per-pixel outlier test applied before the samples are averaged.
type Reject string

const (
	RejectAuto        Reject = ""             // count-adaptive: percentile / winsorized / GESD
	RejectNone        Reject = "none"         // keep every sample
	RejectPercentile  Reject = "percentile"   // drop a fraction at each end — for tiny stacks
	RejectSigma       Reject = "sigma"        // classic iterative kappa-sigma clipping
	RejectMedianSigma Reject = "median_sigma" // sigma clipping around the median
	RejectWinsorized  Reject = "winsorized"   // Huber winsorization — the general-purpose default
	RejectLinearFit   Reject = "linear_fit"   // robust line fit per pixel — for moving gradients
	RejectGESD        Reject = "gesd"         // generalized extreme studentized deviate — big stacks
	RejectMAD         Reject = "mad"          // k times the median absolute deviation

	// Native-only algorithms — Siril has no equivalent.
	RejectRCR              Reject = "rcr"               // Robust Chauvenet Rejection
	RejectAdaptiveWeighted Reject = "adaptive_weighted" // DSS auto-adaptive weighted average
	RejectEntropyWeighted  Reject = "entropy_weighted"  // DSS entropy-weighted average
)

// Norm is how frames are brought onto a common photometric footing before combination.
type Norm string

const (
	NormAuto     Norm = ""         // the call site's own default (addscale for lights)
	NormNone     Norm = "none"     // Siril -nonorm
	NormAdd      Norm = "add"      // additive (offset only)
	NormAddScale Norm = "addscale" // additive with scaling — the deep-sky default
	NormMul      Norm = "mul"      // multiplicative — the correct choice for flats
	NormMulScale Norm = "mulscale" // multiplicative with scaling
)

// Weight is the per-frame weighting applied inside an average.
type Weight string

const (
	WeightNone    Weight = ""        // unweighted
	WeightNoise   Weight = "noise"   // favour the frames with the lowest background noise
	WeightWFWHM   Weight = "wfwhm"   // favour the sharpest frames (star-count-weighted FWHM)
	WeightNbStars Weight = "nbstars" // favour the frames that detected the most stars
	WeightNbStack Weight = "nbstack" // favour already-stacked inputs by their sub-count
)

// Options is one stack's full configuration. Every field's zero value means "leave it to the
// call site's own default", so a partially-filled Options overlays cleanly onto a base (see With).
type Options struct {
	Engine  Engine  `json:"engine,omitempty"`
	Combine Combine `json:"combine,omitempty"`
	Reject  Reject  `json:"reject,omitempty"`
	// Low and High are the rejection algorithm's two parameters. Their meaning and units depend
	// on Reject (sigma multipliers, kept fractions, or GESD outliers/significance) — see
	// Catalogue. 0 means "the algorithm's own default".
	Low  float64 `json:"low,omitempty"`
	High float64 `json:"high,omitempty"`
	// TrimFrac is the fraction discarded at EACH end by CombineTrimmedMean (native only).
	TrimFrac float64 `json:"trim_frac,omitempty"`

	Norm     Norm `json:"norm,omitempty"`
	FastNorm bool `json:"fast_norm,omitempty"` // faster location/scale estimators than IKSS
	// OutputNorm rescales the result into [0,1] (mean and median stacking only).
	OutputNorm bool   `json:"output_norm,omitempty"`
	Weight     Weight `json:"weight,omitempty"`

	// RejMaps additionally writes low/high rejection maps — a diagnostic image showing where
	// pixels were rejected. Costs an extra output file per stack.
	RejMaps bool `json:"rej_maps,omitempty"`
	// Feather blends each frame's borders over this many pixels, hiding the hard dithered edges
	// of a drifting sequence. 0 = off.
	Feather int `json:"feather,omitempty"`

	// LocalNorm fits a per-frame polynomial background/transparency surface before rejection
	// (PixInsight LocalNormalization / APP LNC). Native engine only.
	LocalNorm bool `json:"local_norm,omitempty"`
	// LocalNormDegree is that surface's polynomial degree (1–4). 0 = the engine default.
	LocalNormDegree int `json:"local_norm_degree,omitempty"`
}

// MasterOptions bundles the calibration-master stacks, one per frame type. Each has its own
// physics, which is why they are separate recipes rather than one shared setting — and why the
// normalization is fixed per type rather than exposed: bias and dark stack UN-normalized (their
// pedestal IS the signal, so levelling it would erase what is being measured), while a flat stacks
// multiplicatively (only its relative shape matters).
//
// The frame counts differ by an order of magnitude too — a 200-frame bias pool and a 5-flat set want
// opposite algorithms — so each type resolves its own count-adaptive default.
type MasterOptions struct {
	// Bias is the offset/bias master (the two words mean the same frame).
	Bias Options `json:"bias"`
	Dark Options `json:"dark"`
	Flat Options `json:"flat"`
	// DarkFlat is the flat's OWN dark: it calibrates the flats, so it stacks like a dark.
	DarkFlat Options `json:"dark_flat"`
}

// DefaultLights is the light-frame stack the engine has always run: a count-adaptively rejected
// mean, additively normalized with scaling, rescaled to [0,1].
func DefaultLights() Options {
	return Options{Combine: CombineMean, Norm: NormAddScale, OutputNorm: true}
}

// DefaultComet is the comet-aligned stack's ASYMMETRIC winsorization: the coma is consistent
// frame to frame (a tight high clip never touches it) while the star trails marching through are
// bright one-or-two-frame HIGH outliers, so sigma-high is tight (1.8) where the symmetric 3/3
// left residual streaks. Sigma-low stays loose (4) so the faint tail's noisy low samples survive.
func DefaultComet() Options {
	return Options{
		Combine: CombineMean, Reject: RejectWinsorized, Low: 4, High: 1.8,
		Norm: NormAddScale, OutputNorm: true,
	}
}

// DefaultMasters is the calibration-master recipe: count-adaptive rejection everywhere, no
// normalization for bias/dark, multiplicative for flats.
func DefaultMasters() MasterOptions {
	return MasterOptions{
		Bias:     Options{Combine: CombineMean, Norm: NormNone},
		Dark:     Options{Combine: CombineMean, Norm: NormNone},
		DarkFlat: Options{Combine: CombineMean, Norm: NormNone},
		Flat:     Options{Combine: CombineMean, Norm: NormMul},
	}
}

// Fingerprint is a short, stable tag for a resolved recipe — empty when the options ARE the given
// baseline. Calibration masters are cached in a shared library keyed by camera settings, so a master
// built with a non-default recipe must carry the tag in its filename: otherwise it would overwrite
// (or be silently reused in place of) the default-options master every other run depends on.
func (o Options) Fingerprint(base Options) string {
	if o == base {
		return ""
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%g|%g|%g|%s|%t|%s|%t|%d|%t|%d",
		o.Engine, o.Combine, o.Reject, o.Low, o.High, o.TrimFrac, o.Norm, o.FastNorm,
		o.Weight, o.RejMaps, o.Feather, o.LocalNorm, o.LocalNormDegree)))
	return hex.EncodeToString(h[:3])
}

// DefaultChannelIntegration is the cross-CHANNEL integration used by the combined-mono side
// output. Rejection is deliberately off: the "frames" are different filters, not repeated samples
// of one signal, so an outlier test clips a channel's real morphology (an Ha-bright core) as if it
// were noise.
func DefaultChannelIntegration() Options {
	return Options{Combine: CombineMean, Reject: RejectNone, Norm: NormAddScale, OutputNorm: true}
}

// Configured reports whether o asks for anything other than the engine's historical behaviour —
// i.e. whether this stack still renders byte-identically to a pre-knob run. Used to decide when a
// calibration master must be built under a variant name instead of the shared library one.
func (o Options) Configured(base Options) bool { return o != base }
