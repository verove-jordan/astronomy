package inspect

import (
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Many capture programs (e.g. ASIStudio/ASICap) omit IMAGETYP/FILTER from the FITS header and
// instead encode everything in the filename and folder, e.g.
//
//	Light_ASIImg_30sec_Bin1_filter-B_-20.0C_gain300_2025-03-27_234852_frame0001.fit
//
// fileMeta holds what we can recover that way; it fills only the fields the header lacked.
type fileMeta struct {
	Type       FrameType
	Filter     string
	ExposureMs int64
	Gain       int64
	TempMilliC int64
	HasTemp    bool
	Bin        int
	WheelSlot  int // EFW filter-wheel position parsed from a SharpCap filename (0 = none)
}

var (
	reFilter = regexp.MustCompile(`(?i)filter[-_]([A-Za-z0-9]+)`)
	reExp    = regexp.MustCompile(`(?i)(\d*\.?\d+)\s*sec`)
	reGain   = regexp.MustCompile(`(?i)gain[-_]?(\d+)`)
	// reGainSuffix catches the legacy folder form where the number precedes "gain" (e.g.
	// darks_0gain_300s, m81_m82_LRGB_0gain_Ha_180gain_120s).
	reGainSuffix = regexp.MustCompile(`(?i)(\d+)gain`)
	reTemp       = regexp.MustCompile(`(-?\d+\.?\d*)C(?:_|\.|$)`)
	reBin        = regexp.MustCompile(`(?i)bin(\d+)`)
	// reWheelSlot extracts the EFW slot from a SharpCap/ASICAP capture name, e.g.
	// "2021-08-14-0047_6-1-CapObj_0000.FIT" → slot 1. Anchored on the date-time prefix so ordinary
	// names (Light_..._frame0001.fit) never match.
	reWheelSlot = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{4}_\d+-(\d+)-`)
)

// parseFilenameMeta extracts metadata from a file's name and its parent directory names.
func parseFilenameMeta(path string) fileMeta {
	name := filepath.Base(path)
	low := strings.ToLower(name)
	m := fileMeta{Type: typeFromToken(low)}

	if m.Type == Unknown {
		m.Type = typeFromDirs(path) // e.g. .../Darks/.., .../Bias/.., .../Flats/B/..
	}
	if mt := reFilter.FindStringSubmatch(name); mt != nil {
		m.Filter = normalizeFilter(mt[1])
	} else {
		m.Filter = filterFromDirs(path)
	}
	if mt := reExp.FindStringSubmatch(low); mt != nil {
		if sec, err := strconv.ParseFloat(mt[1], 64); err == nil {
			m.ExposureMs = int64(math.Round(sec * 1000))
		}
	}
	if g, ok := gainFromName(low); ok {
		m.Gain = g
	} else {
		m.Gain = gainFromDirs(path)
	}
	if mt := reTemp.FindStringSubmatch(name); mt != nil {
		if t, err := strconv.ParseFloat(mt[1], 64); err == nil {
			m.TempMilliC = int64(math.Round(t * 1000))
			m.HasTemp = true
		}
	}
	if mt := reBin.FindStringSubmatch(low); mt != nil {
		m.Bin, _ = strconv.Atoi(mt[1])
	}
	if mt := reWheelSlot.FindStringSubmatch(name); mt != nil {
		m.WheelSlot, _ = strconv.Atoi(mt[1])
	}
	return m
}

func typeFromToken(low string) FrameType {
	switch {
	case strings.Contains(low, "flat") && strings.Contains(low, "dark"):
		return DarkFlat
	case strings.HasPrefix(low, "light"):
		return Light
	case strings.HasPrefix(low, "dark"):
		return Dark
	case strings.HasPrefix(low, "flat"):
		return Flat
	case strings.HasPrefix(low, "bias"), strings.HasPrefix(low, "offset"):
		return Bias
	default:
		return Unknown
	}
}

// typeFromDirs infers a frame type from any parent directory name, nearest first. It matches on
// word tokens (split on _ - . space), so decorated/compound folders like "darks_0gain_300s_-25deg",
// "offset_-15_250gain", "flats_0gain_Ha" or "master_darks" are recognized — while a token that merely
// contains the word (e.g. "darkstar") is not.
func typeFromDirs(path string) FrameType {
	for _, d := range parentDirs(path) {
		if t := typeFromDirName(d); t != Unknown {
			return t
		}
	}
	return Unknown
}

func typeFromDirName(dir string) FrameType {
	set := tokenSet(dir)
	hasDark := set["dark"] || set["darks"]
	hasFlat := set["flat"] || set["flats"] || set["flatfield"] || set["flatfields"]
	darkFlat := set["darkflat"] || set["darkflats"] || set["flatdark"] || set["flatdarks"]
	switch {
	case darkFlat || (hasDark && hasFlat):
		return DarkFlat
	case hasFlat:
		return Flat
	case set["bias"] || set["biases"] || set["offset"] || set["offsets"] || set["zero"]:
		return Bias
	case hasDark:
		return Dark
	case set["light"] || set["lights"] || set["science"] || set["object"]:
		return Light
	}
	return Unknown
}

// tokenSet splits a directory name into lowercased word tokens (on _ - . space) for keyword matching.
func tokenSet(name string) map[string]bool {
	set := make(map[string]bool)
	for _, tk := range strings.FieldsFunc(strings.ToLower(name), isTokenSep) {
		set[tk] = true
	}
	return set
}

func isTokenSep(r rune) bool {
	return r == '_' || r == '-' || r == '.' || r == ' '
}

// gainFromName extracts a gain from a single name token, accepting both "gain300" and the legacy
// "0gain"/"180gain" suffix form.
func gainFromName(low string) (int64, bool) {
	if mt := reGain.FindStringSubmatch(low); mt != nil {
		g, err := strconv.ParseInt(mt[1], 10, 64)
		return g, err == nil
	}
	if mt := reGainSuffix.FindStringSubmatch(low); mt != nil {
		g, err := strconv.ParseInt(mt[1], 10, 64)
		return g, err == nil
	}
	return 0, false
}

// gainFromDirs recovers a gain from a parent directory name (e.g. darks_0gain_300s) for calibration
// frames whose header and filename carry none. Returns 0 when no parent encodes a gain.
func gainFromDirs(path string) int64 {
	for _, d := range parentDirs(path) {
		if g, ok := gainFromName(strings.ToLower(d)); ok {
			return g
		}
	}
	return 0
}

// filterFromDirs treats a parent dir named primarily by a filter as that filter: its FIRST token
// (split on _ - . space) must be a known filter — so "L", "Ha", "Ha_300s", "R band", "Red", "V" all
// resolve, while a compound session name that merely mentions a filter ("m81_m82_LRGB_0gain_Ha_120s")
// is correctly ignored (its first token is the object, not a filter). Nearest parent wins.
func filterFromDirs(path string) string {
	for _, d := range parentDirs(path) {
		head := strings.FieldsFunc(d, isTokenSep)
		if len(head) == 0 {
			continue
		}
		if f, ok := filterToken(head[0]); ok {
			return f
		}
	}
	return ""
}

// parentDirs returns the nearest ancestor directory base names (nearest first), bounded so a
// coincidental far-up match can't override a nearer, more specific folder.
func parentDirs(path string) []string {
	dir := filepath.Dir(path)
	var out []string
	for i := 0; i < 6 && dir != "." && dir != "/" && dir != ""; i++ {
		out = append(out, filepath.Base(dir))
		dir = filepath.Dir(dir)
	}
	return out
}
