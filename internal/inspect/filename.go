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
//
// An EXPLICIT calibration/light folder (darks/flats/bias/offset/lights/science/object) outranks
// SharpCap's generic "CapObj" capture folder, regardless of proximity: SharpCap writes BOTH lights and
// calibration into a "CapObj/" subfolder (its default captured-object name), so real trees nest it under
// the type folder — .../darks/CapObj/<session>/x.FIT, .../flats/CapObj/... A frame there must read as a
// DARK from the "darks" grandparent, not a LIGHT from the nearer "CapObj". CapObj therefore names a type
// only as a fallback, when no ancestor states an explicit one (a bare .../CapObj/<session>/x.FIT is an
// unlabeled light). Two passes keep this independent of how deep CapObj sits.
func typeFromDirs(path string) FrameType {
	dirs := parentDirs(path)
	for _, d := range dirs {
		if t := typeFromDirName(d); t != Unknown {
			return t // nearest explicit calibration/light folder wins
		}
	}
	for _, d := range dirs {
		if tokenSet(d)["capobj"] {
			return Light // SharpCap captured-object folder: an unlabeled light, only when nothing else typed it
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
	// NB: "capobj" (SharpCap's default capture folder) is deliberately NOT a Light signal here — it
	// appears under both light and calibration trees. typeFromDirs applies it only as a fallback.
	return Unknown
}

// processedTokens are filename words that mark a PROCESSED image (a stack/finish output someone
// stored beside their captures), never a raw sub — e.g. "m27_R_stacked.tif". Matched per token so
// an object name containing the letters is safe.
var processedTokens = map[string]bool{
	"stacked": true, "stack": true, "master": true, "combined": true, "final": true,
	"mosaic": true, "annotated": true, "preview": true, "thumb": true, "starless": true,
	"autosave": true, // Siril/live-stack outputs saved beside the raws (Autosave.tif, Autosave001.tif)
}

// isProcessedName reports whether a file's base name tokens mark it as a processed output.
func isProcessedName(path string) bool {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for tk := range tokenSet(base) {
		// Trailing digits are a copy counter, not identity ("Autosave001", "stacked2").
		if processedTokens[tk] || processedTokens[strings.TrimRight(tk, "0123456789")] {
			return true
		}
	}
	return false
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

// gainFromName extracts a gain from a name, accepting both "gain300" and the legacy "0gain"/"180gain"
// suffix form. It matches per token (split on _ - . space) so a number in a neighbouring token is never
// swept in: in "darks_0gain_300s" the gain is 0 (from "0gain"), not 300 — the "300" is the exposure.
func gainFromName(low string) (int64, bool) {
	toks := strings.FieldsFunc(low, isTokenSep)
	for i, tok := range toks {
		// Suffix form first ("0gain", "180gain"): a digit-prefixed "gain" is a label, not a value to
		// read forward from — so this must win before the glued-prefix rule on the same token.
		if mt := reGainSuffix.FindStringSubmatch(tok); mt != nil {
			if g, err := strconv.ParseInt(mt[1], 10, 64); err == nil {
				return g, true
			}
		}
		// Glued prefix form within one token ("gain300").
		if mt := reGain.FindStringSubmatch(tok); mt != nil {
			if g, err := strconv.ParseInt(mt[1], 10, 64); err == nil {
				return g, true
			}
		}
		// Separated prefix form: a standalone "gain" token followed by a number ("gain 300" / "gain_300").
		if strings.EqualFold(tok, "gain") && i+1 < len(toks) {
			if g, err := strconv.ParseInt(toks[i+1], 10, 64); err == nil {
				return g, true
			}
		}
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

// filterFromDirs treats a parent dir named primarily by a filter as that filter. For a calibration
// folder (flats/darks/bias) the name states its purpose, so a filter token ANYWHERE in it is an
// intentional qualifier — "flats_0gain_Ha" is Ha flats. Otherwise only the FIRST token may be the
// filter — so "L", "Ha", "Ha_300s", "Red", "V" resolve, while a compound session name that merely
// mentions a filter ("m81_m82_LRGB_0gain_Ha_120s") is correctly ignored. Nearest parent wins.
func filterFromDirs(path string) string {
	for _, d := range parentDirs(path) {
		toks := strings.FieldsFunc(d, isTokenSep)
		if len(toks) == 0 {
			continue
		}
		if isCalibration(typeFromDirName(d)) {
			for _, tk := range toks {
				if f, ok := filterToken(tk); ok {
					return f
				}
			}
			continue
		}
		if f, ok := filterToken(toks[0]); ok {
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
