package inspect

import (
	"math"
	"strings"
	"time"
)

// classifyImageType maps a FITS IMAGETYP value to a FrameType. Capture programs vary
// ("Light Frame", "LIGHT", "Dark", "Bias Frame", "FlatField", "FlatDark", ...).
func classifyImageType(imagetyp string) FrameType {
	s := strings.ToLower(imagetyp)
	hasFlat := strings.Contains(s, "flat")
	hasDark := strings.Contains(s, "dark")
	switch {
	case hasFlat && hasDark:
		return DarkFlat
	case hasFlat:
		return Flat
	case strings.Contains(s, "bias"), strings.Contains(s, "offset"), strings.Contains(s, "zero"):
		return Bias
	case hasDark:
		return Dark
	case strings.Contains(s, "light"), strings.Contains(s, "object"), strings.Contains(s, "science"):
		return Light
	default:
		return Unknown
	}
}

// normalizeFilter trims a filter name and abbreviates the common broadband/narrowband names
// to single tokens (L, R, G, B, Ha, OIII, SII) so grouping is stable across capture programs.
// An unrecognized name passes through verbatim (e.g. a custom filter the user maps later).
func normalizeFilter(raw string) string {
	if f, ok := filterToken(raw); ok {
		return f
	}
	return strings.TrimSpace(raw)
}

// filterToken canonicalizes a single token to a known filter name, reporting whether it is one.
// It is the single source of truth for "is this string a filter?" used by both header/filename
// normalization and directory-name detection. Johnson V is treated as the green channel (these
// older LRGB sessions used R/V/B), surfaced and overridable via the filter-mapping UI.
func filterToken(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "l", "lum", "luminance", "clear":
		return "L", true
	case "r", "red":
		return "R", true
	case "g", "green", "v":
		return "G", true
	case "b", "blue":
		return "B", true
	case "ha", "h-alpha", "halpha", "h_alpha", "hydrogen-alpha":
		return "Ha", true
	case "oiii", "o3", "oxygen":
		return "OIII", true
	case "sii", "s2", "sulfur":
		return "SII", true
	}
	return "", false
}

// parseDateObs parses a FITS DATE-OBS into epoch milliseconds (0 if unparseable).
func parseDateObs(s string) int64 {
	s = strings.TrimSpace(s)
	layouts := []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// frameStat bundles the per-frame pixel "curve" signals classifyByStats compares across one
// capture session (all values are robust summaries of a center sample; see fits.Stats).
type frameStat struct {
	exposureMs int64
	median     float64
	mad        float64 // robust noise (median absolute deviation)
	brightFrac float64 // fraction of pixels well above the noise floor (structure/nebulosity)
	peaks      int     // distinct bright point sources (stars/hot pixels)
}

// Statistical-fallback thresholds. They run only when neither header, filename, nor folder names a
// frame's type, and rely on signals that survive both 16-bit-integer and [0,1]-float frames.
const (
	biasMaxExposureMs = 1500  // bias/offset exposures are essentially zero (ASICAP offsets are 32 µs → 0 ms)
	flatMaxExposureMs = 15000 // flats are short (a bright panel); long exposures are darks/lights, never flats
	lightMinPeaks     = 8     // a real star field shows many bright point sources in the sample
	lightMinBright    = 0.01  // …or appreciable bright structure (nebulosity) even when stars are sparse
	flatBrightFloorX  = 4.0   // a flat's illuminated level is several× the session's dark/bias floor
	flatMaxNonUniform = 0.08  // a flat panel is smooth: MAD/median stays small
	biasFloorX        = 1.5   // a bias frame sits at (not appreciably above) the dark/bias floor
)

// classifyByStats infers a type for each unlabeled frame from its pixel curve and exposure, comparing
// frames across the session. Unlike the old mean-ADU heuristic it can emit Dark — which the heuristic
// never returned, so unlabeled darks silently became lights. Decision order: stars/structure ⇒ light;
// short + bright + uniform ⇒ flat; short + dim ⇒ bias; long + starless but co-exposed with real lights
// ⇒ dark; otherwise (no positive dark evidence) keep as light so faint signal is never discarded.
func classifyByStats(stats []frameStat) []FrameType {
	floor := darkFloor(stats)
	starry := starryExposures(stats)
	out := make([]FrameType, len(stats))
	for i, s := range stats {
		out[i] = classifyOneStat(s, floor, starry)
	}
	return out
}

func classifyOneStat(s frameStat, floor float64, starryExp map[int64]bool) FrameType {
	switch {
	case hasStars(s):
		return Light
	case isFlatCurve(s, floor):
		return Flat
	case isBiasCurve(s, floor):
		return Bias
	case starryExp[s.exposureMs]:
		return Dark // starless, yet real lights exist at this exposure → this is a matching dark
	default:
		return Light // ambiguous starless frame with no co-exposed lights — keep it; grading will filter
	}
}

// hasStars reports whether a frame shows point sources or bright structure (a light), via the peak
// count or the bright-pixel fraction (the latter catches nebulosity-rich narrowband with sparse stars).
func hasStars(s frameStat) bool {
	return s.peaks >= lightMinPeaks || s.brightFrac >= lightMinBright
}

// starryExposures is the set of exposure times that have at least one star/structure-bearing frame —
// i.e. real lights. A starless frame sharing such an exposure is a co-acquired dark.
func starryExposures(stats []frameStat) map[int64]bool {
	m := make(map[int64]bool)
	for _, s := range stats {
		if hasStars(s) {
			m[s.exposureMs] = true
		}
	}
	return m
}

// darkFloor is the session's dark/bias signal level: the smallest frame median (the least-illuminated
// frame). It is the reference a flat must clearly exceed and a bias must sit near.
func darkFloor(stats []frameStat) float64 {
	floor := math.MaxFloat64
	for _, s := range stats {
		if s.median < floor {
			floor = s.median
		}
	}
	if floor == math.MaxFloat64 || floor <= 0 {
		return 1 // no positive reference (empty set / float frames at 0) — avoid divide-by-zero
	}
	return floor
}

// isFlatCurve reports whether a starless frame looks like a flat: a short exposure (bright panel),
// clearly brighter than the session's dark floor, and spatially smooth (structureless).
func isFlatCurve(s frameStat, floor float64) bool {
	if s.exposureMs > flatMaxExposureMs {
		return false // a long exposure is a dark or a light, never a flat
	}
	bright := s.median >= floor*flatBrightFloorX
	uniform := s.median > 0 && s.mad/s.median <= flatMaxNonUniform
	return bright && uniform
}

// isBiasCurve reports whether a starless frame looks like a bias/offset: a near-zero exposure read at
// the dark floor (read-noise only, no accumulated dark current).
func isBiasCurve(s frameStat, floor float64) bool {
	return s.exposureMs <= biasMaxExposureMs && s.median <= floor*biasFloorX
}
