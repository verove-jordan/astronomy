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
}

var (
	reFilter = regexp.MustCompile(`(?i)filter[-_]([A-Za-z0-9]+)`)
	reExp    = regexp.MustCompile(`(?i)(\d*\.?\d+)\s*sec`)
	reGain   = regexp.MustCompile(`(?i)gain[-_]?(\d+)`)
	reTemp   = regexp.MustCompile(`(-?\d+\.?\d*)C(?:_|\.|$)`)
	reBin    = regexp.MustCompile(`(?i)bin(\d+)`)
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
	if mt := reGain.FindStringSubmatch(low); mt != nil {
		m.Gain, _ = strconv.ParseInt(mt[1], 10, 64)
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

// typeFromDirs infers a frame type from any parent directory name.
func typeFromDirs(path string) FrameType {
	for _, d := range parentDirs(path) {
		switch strings.ToLower(d) {
		case "lights", "light":
			return Light
		case "darks", "dark":
			return Dark
		case "flats", "flat":
			return Flat
		case "bias", "biases", "offset", "offsets":
			return Bias
		case "darkflats", "dark-flats", "flatdarks":
			return DarkFlat
		}
	}
	return Unknown
}

// filterFromDirs treats a single-token parent dir (B, G, L, R, Ha…) as the filter.
func filterFromDirs(path string) string {
	for _, d := range parentDirs(path) {
		switch strings.ToLower(d) {
		case "l", "r", "g", "b", "ha", "oiii", "sii":
			return normalizeFilter(d)
		}
	}
	return ""
}

func parentDirs(path string) []string {
	dir := filepath.Dir(path)
	var out []string
	for i := 0; i < 4 && dir != "." && dir != "/" && dir != ""; i++ {
		out = append(out, filepath.Base(dir))
		dir = filepath.Dir(dir)
	}
	return out
}
