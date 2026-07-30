package capture

import (
	"fmt"
	"sort"

	"github.com/verove-jordan/astronomy/internal/filters"
)

// Building a calibration set: the guided path from "I shot some lights" to a matched library of
// darks, flats and bias frames.
//
// Calibration only cancels what it MATCHES. A dark subtracts thermal signal and hot pixels, which
// depend on exposure, gain, offset, binning and sensor temperature — change any of them and the
// dark is worse than useless, because it removes a pattern the lights do not contain. A flat divides
// out dust shadows and vignetting, which live in the optical train, so it needs one per filter, at
// the same focus and rotation. Bias captures the read pedestal and needs only the shortest possible
// exposure at the lights' gain and offset.
//
// Getting that matching right by hand is where most calibration libraries go wrong, so this builds
// the plan from the lights themselves rather than asking the user to retype the settings.

// CalibrationKind selects which frame types to shoot.
type CalibrationKind string

const (
	CalibDark     CalibrationKind = "dark"
	CalibFlat     CalibrationKind = "flat"
	CalibBias     CalibrationKind = "bias"
	CalibDarkFlat CalibrationKind = "darkflat"
)

// LightSettings describe the exposures the calibration must match. Normally read from the session
// that just finished, so nothing is retyped.
type LightSettings struct {
	ExposureUs int64    `json:"exposure_us"`
	Gain       int64    `json:"gain"`
	Offset     int64    `json:"offset"`
	Bin        int      `json:"bin"`
	Filters    []string `json:"filters"`
	// TempMilliC is the sensor set-point the lights were taken at. Carried for the UI to warn on;
	// the sequencer cannot enforce it because cooling is a camera control, not a per-frame one.
	TempMilliC int  `json:"temp_milli_c"`
	HasTemp    bool `json:"has_temp"`
}

// CalibrationRequest asks for a plan.
type CalibrationRequest struct {
	Lights LightSettings     `json:"lights"`
	Kinds  []CalibrationKind `json:"kinds"`
	// Counts per kind; zero falls back to the recommended count for that type.
	DarkCount     int `json:"dark_count"`
	FlatCount     int `json:"flat_count"`
	BiasCount     int `json:"bias_count"`
	DarkFlatCount int `json:"dark_flat_count"`
	// FlatExposureUs comes from the flat auto-exposure measurement. Zero means "not measured yet",
	// which is a refusal rather than a guess: flat exposure depends on the light panel and the sky.
	FlatExposureUs int64 `json:"flat_exposure_us"`
}

// Recommended frame counts. These are not arbitrary: master frames average down read noise as √N,
// so the gain from more frames flattens quickly. Around 30 the master's own noise is a fifth of a
// single frame's and no longer limits the stack — past that the extra time is better spent on
// lights. Bias frames are free (they take no time), so more of them costs nothing.
const (
	recommendedDarks     = 30
	recommendedFlats     = 30
	recommendedBias      = 50
	recommendedDarkFlats = 30
	// biasExposureUs is the shortest exposure worth asking for. The sensor's true floor is 32 µs,
	// but asking for exactly the minimum makes some drivers round in surprising ways; 32 µs is what
	// the ASI1600 reports and what it honours.
	biasExposureUs = 32
)

// CalibrationPlan is the answer: an ordered sequence plus the reasoning, so the UI can explain
// itself rather than presenting an opaque list of exposures.
type CalibrationPlan struct {
	Sequence     Sequence `json:"sequence"`
	TotalFrames  int      `json:"total_frames"`
	EstimatedSec float64  `json:"estimated_seconds"`
	Notes        []string `json:"notes"`
	Warnings     []string `json:"warnings,omitempty"`
}

// BuildCalibrationPlan turns light settings into an ordered calibration sequence.
func BuildCalibrationPlan(req CalibrationRequest) (CalibrationPlan, error) {
	if len(req.Kinds) == 0 {
		return CalibrationPlan{}, fmt.Errorf("choose at least one kind of calibration frame")
	}
	l := req.Lights
	if l.Bin <= 0 {
		l.Bin = 1
	}

	var plan CalibrationPlan
	want := map[CalibrationKind]bool{}
	for _, k := range req.Kinds {
		want[k] = true
	}

	// Order matters, and not only for tidiness. Bias and darks need the shutter closed or the scope
	// capped; flats need a light source. Shooting the dark frames first means the user covers the
	// telescope once, at the start, instead of being asked to uncover and recover it.
	if want[CalibBias] {
		n := pick(req.BiasCount, recommendedBias)
		plan.Sequence.Steps = append(plan.Sequence.Steps, Step{
			Type: string(CalibBias), Count: n, ExposureUs: biasExposureUs,
			Gain: l.Gain, Offset: l.Offset, Bin: l.Bin,
		})
		plan.Notes = append(plan.Notes,
			"Bias: cap the telescope. Shortest possible exposure, same gain and offset as the lights.")
	}

	if want[CalibDark] {
		if l.ExposureUs <= 0 {
			return CalibrationPlan{}, fmt.Errorf(
				"darks must match the lights' exposure, but no light exposure was given")
		}
		n := pick(req.DarkCount, recommendedDarks)
		plan.Sequence.Steps = append(plan.Sequence.Steps, Step{
			Type: string(CalibDark), Count: n, ExposureUs: l.ExposureUs,
			Gain: l.Gain, Offset: l.Offset, Bin: l.Bin,
		})
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"Darks: keep the telescope capped. Same exposure (%s), gain and temperature as the lights.",
			formatExposure(l.ExposureUs)))
	}

	if want[CalibFlat] {
		if req.FlatExposureUs <= 0 {
			return CalibrationPlan{}, fmt.Errorf(
				"measure the flat exposure first — it depends on your light panel and cannot be guessed")
		}
		n := pick(req.FlatCount, recommendedFlats)
		for _, f := range sortedFilters(l.Filters) {
			plan.Sequence.Steps = append(plan.Sequence.Steps, Step{
				Type: string(CalibFlat), Filter: f, Count: n, ExposureUs: req.FlatExposureUs,
				Gain: l.Gain, Offset: l.Offset, Bin: l.Bin,
			})
		}
		plan.Notes = append(plan.Notes,
			"Flats: uncap and cover the aperture with an evenly lit panel. One set per filter, "+
				"with the focus and camera rotation UNCHANGED since the lights — a flat only matches "+
				"the dust shadows it was taken with.")
	}

	if want[CalibDarkFlat] {
		if req.FlatExposureUs <= 0 {
			return CalibrationPlan{}, fmt.Errorf("dark flats need the flat exposure to match")
		}
		n := pick(req.DarkFlatCount, recommendedDarkFlats)
		plan.Sequence.Steps = append(plan.Sequence.Steps, Step{
			Type: string(CalibDarkFlat), Count: n, ExposureUs: req.FlatExposureUs,
			Gain: l.Gain, Offset: l.Offset, Bin: l.Bin,
		})
		plan.Notes = append(plan.Notes,
			"Dark flats: cap the telescope again. Same exposure as the flats — they remove the flats' "+
				"own read pedestal, and replace bias when the flat exposure is long.")
	}

	if len(plan.Sequence.Steps) == 0 {
		return CalibrationPlan{}, fmt.Errorf("nothing to shoot")
	}
	plan.Sequence.Name = "Calibration"
	for _, s := range plan.Sequence.Steps {
		plan.TotalFrames += s.Count
		plan.EstimatedSec += float64(s.Count) * (float64(s.ExposureUs)/1e6 + downloadOverheadSec)
	}
	plan.Warnings = calibrationWarnings(l, want)
	return plan, nil
}

// downloadOverheadSec is the per-frame cost beyond the exposure itself — readout, transfer and
// writing. Roughly right for a USB3 ASI1600 at full frame; it only feeds the time estimate.
const downloadOverheadSec = 1.5

// calibrationWarnings names the mistakes that silently ruin a calibration library.
func calibrationWarnings(l LightSettings, want map[CalibrationKind]bool) []string {
	var out []string
	if want[CalibDark] && !l.HasTemp {
		out = append(out, "the lights have no recorded sensor temperature, so these darks cannot be "+
			"temperature-matched — cool the camera to the same set-point you used for the lights")
	}
	if want[CalibFlat] && len(l.Filters) == 0 {
		out = append(out, "no filters were given, so a single unfiltered flat set will be shot — "+
			"if you used a filter wheel, each filter needs its own flats")
	}
	if want[CalibBias] && want[CalibDarkFlat] {
		out = append(out, "bias and dark flats do the same job; shooting both is harmless but only "+
			"one of them will be used")
	}
	return out
}

func pick(given, fallback int) int {
	if given > 0 {
		return given
	}
	return fallback
}

// sortedFilters de-duplicates and orders the filter list so a plan is deterministic. An empty list
// yields one unfiltered set rather than nothing — a camera without a wheel still needs flats.
//
// Ordering is canonical (L,R,G,B,Ha,OIII,SII), not alphabetical: a flat plan is read against the
// wheel, and "B, G, Ha, L, OIII, R, SII" makes the imager hunt for each row.
func sortedFilters(want []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(want))
	for _, f := range want {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		return []string{""}
	}
	sort.Slice(out, func(i, j int) bool { return filters.Less(out[i], out[j]) })
	return out
}

// formatExposure renders a duration the way an imager says it.
func formatExposure(us int64) string {
	switch {
	case us >= 1_000_000:
		return fmt.Sprintf("%g s", float64(us)/1e6)
	case us >= 1000:
		return fmt.Sprintf("%g ms", float64(us)/1e3)
	default:
		return fmt.Sprintf("%d µs", us)
	}
}
