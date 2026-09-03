package inspect

import (
	"math"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/filters"
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
func normalizeFilter(raw string) string { return filters.Normalize(raw) }

// filterToken canonicalizes a single token to a known filter name, reporting whether it is one.
// The token table lives in internal/filters so the capture sequencer and the wheel-slot UI resolve
// names exactly the way ingest does.
func filterToken(raw string) (string, bool) { return filters.Token(raw) }

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
	// hasStats marks that the pixel curve was actually measured. A frame whose stats could not be
	// read (unreadable file, or no raw developer on this host — sips is macOS-only) must NEVER be
	// classified from its zero-value curve: an all-zero stat is indistinguishable from a bias at
	// the dark floor, which is how processed TIFFs became phantom BIAS frames in the Linux engine.
	hasStats bool
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
	exposed, hasExposed := exposedLevel(stats)
	out := make([]FrameType, len(stats))
	for i, s := range stats {
		out[i] = classifyOneStat(s, floor, exposed, hasExposed, starry)
	}
	return out
}

func classifyOneStat(s frameStat, floor, exposed float64, hasExposed bool, starryExp map[int64]bool) FrameType {
	if !s.hasStats {
		return Light // no measured curve → never guess a calibration type; keep the frame as signal
	}
	switch {
	case hasStars(s):
		return Light
	case isFlatCurve(s, floor):
		return Flat
	case isBiasCurve(s, floor):
		return Bias
	case hasExposed && s.median >= exposed:
		return Light // recorded a scene: whatever it is, a shutter that saw light is not a dark
	case starryExp[s.exposureMs]:
		return Dark // starless, yet real lights exist at this exposure → this is a matching dark
	default:
		return Light // ambiguous starless frame with no co-exposed lights — keep it; grading will filter
	}
}

// Level test thresholds. They give the classifier a second, independent way to recognise a light,
// for frames where counting point sources cannot work.
const (
	// exposedMinFrac places the cut a quarter of the way from the batch's darkest frame to its
	// brightest — far from both, since the two populations sit at opposite ends.
	exposedMinFrac = 0.25
	// exposedMinRatio is how many times brighter the batch's brightest frame must be than its
	// darkest before the test is used at all. It is deliberately steep: a median is only a fair
	// stand-in for "saw a scene" when the frames that saw nothing sat at essentially zero. A
	// deep-sky light whose signal lives in a small bright corner has a DARK median, and on a gentler
	// threshold this rule would read the batch backwards.
	exposedMinRatio = 20
)

// exposedLevel returns the median above which a frame is taken to have recorded a scene, and
// whether the batch supports that judgement.
//
// It exists because star counting fails on wide-field frames. A 24 mm phone frame downscaled for
// classification renders the whole Milky Way as a smooth glow: real 10-second lights of the sea
// horizon measured 0 to 9 peaks, straddling the 8 needed to be called a light, so a third of one
// panel was labelled DARK and would have been subtracted from its own siblings. Their brightness
// was never in doubt — median 0.28 against a dark floor of 0.000.
//
// The reference is the batch's brightest frame, so a batch that also holds flats raises the bar and
// the test simply goes quiet. That is the safe direction: this rule can only add lights, never
// remove them.
func exposedLevel(stats []frameStat) (float64, bool) {
	lo, hi := math.MaxFloat64, -math.MaxFloat64
	for _, s := range stats {
		if !s.hasStats {
			continue
		}
		lo = math.Min(lo, s.median)
		hi = math.Max(hi, s.median)
	}
	if hi <= 0 || lo > hi {
		return 0, false
	}
	if hi < exposedMinRatio*lo {
		return 0, false
	}
	return lo + exposedMinFrac*(hi-lo), true
}

// hasStars reports whether a frame shows point sources or bright structure (a light), via the peak
// count or the bright-pixel fraction (the latter catches nebulosity-rich narrowband with sparse stars).
//
// The peak count is a threshold ABOVE the noise and needs a noise estimate to mean anything. On a
// frame at the true black floor the robust spread is exactly zero, that threshold collapses onto the
// median, and every ripple of read noise becomes a peak: a capped-lens phone bias measured 75 of
// them and was called a light. The bright-pixel fraction has no such problem — it is a proportion,
// so a frame that is 11% bright region still reads as structure however flat its median is, which
// is why only the peak test is gated.
func hasStars(s frameStat) bool {
	if s.brightFrac >= lightMinBright {
		return true
	}
	return s.mad > 0 && s.peaks >= lightMinPeaks
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
// frame). It is the reference a flat must clearly exceed and a bias must sit near. Frames without
// measured stats are excluded — their zero medians would drag the floor to 0 for everyone else.
func darkFloor(stats []frameStat) float64 {
	floor := math.MaxFloat64
	for _, s := range stats {
		if s.hasStats && s.median < floor {
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
