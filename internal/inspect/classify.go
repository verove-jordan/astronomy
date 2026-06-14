package inspect

import (
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
func normalizeFilter(raw string) string {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(s) {
	case "l", "lum", "luminance", "clear":
		return "L"
	case "r", "red":
		return "R"
	case "g", "green":
		return "G"
	case "b", "blue":
		return "B"
	case "ha", "h-alpha", "halpha", "h_alpha", "hydrogen-alpha":
		return "Ha"
	case "oiii", "o3", "oxygen":
		return "OIII"
	case "sii", "s2", "sulfur":
		return "SII"
	default:
		return s
	}
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

// classifyHeuristic infers a type for frames that lacked an IMAGETYP card, using exposure
// time and a center-sampled mean ADU. It is intentionally coarse — modern capture software
// writes IMAGETYP, so this is a fallback — and every decision is flagged with a warning.
//
// minExposureMs is the shortest exposure among the unclassified frames.
func classifyHeuristic(meanADU float64, exposureMs, minExposureMs int64) FrameType {
	const flatMeanFloor = 8000.0 // flats are deliberately bright (well-lit panel)
	switch {
	case meanADU >= flatMeanFloor:
		return Flat
	case exposureMs <= minExposureMs && exposureMs <= 1000:
		return Bias
	default:
		// Cannot reliably separate dark from light without star detection; default to light.
		return Light
	}
}
