package s3layout

import (
	"regexp"
	"strings"
)

// genericDirs are folder names that carry no object identity (calibration classes, capture-tool scaffolding
// and generic containers), so the object detector skips them and the calib-set namer date-suffixes them.
var genericDirs = map[string]bool{
	"darks": true, "dark": true, "bias": true, "biases": true, "offset": true, "offsets": true,
	"flats": true, "flat": true, "darkflats": true, "darkflat": true, "flatdarks": true,
	"calib": true, "calibration": true, "master": true, "masters": true,
	"lights": true, "light": true, "sub": true, "subs": true, "capture": true, "captures": true,
	"input": true, "inputs": true, "data": true, "fits": true, "raw": true, "sorted": true,
	"sorted_dng": true, "images": true, "session": true, "sessions": true,
}

// filterDirs are single-filter folder names — they name a channel, not the object.
var filterDirs = map[string]bool{
	"l": true, "lum": true, "luminance": true, "r": true, "red": true, "g": true, "green": true,
	"b": true, "blue": true, "ha": true, "halpha": true, "oiii": true, "o3": true, "sii": true, "s2": true,
	"rgb": true, "lrgb": true, "v": true, "c": true,
}

// yearRe matches a standalone 4-digit year (1900–2099) — a trailing capture-year token to peel off a
// compound object name (M81_M82_2020 → M81_M82) and to recognise a bare-year date fallback.
var yearRe = regexp.MustCompile(`^(?:19|20)\d\d$`)

// counterRe matches a short trailing sequence counter (moon_2 → moon).
var counterRe = regexp.MustCompile(`^\d{1,2}$`)

// catalogRe matches a leading catalogue prefix so it can be upper-cased (m31 → M31, ngc6960 → NGC6960).
var catalogRe = regexp.MustCompile(`(?i)^(m|ngc|ic|sh2|ldn|abell|pgc|ugc|c)(\d)`)

// sanitizeRe strips any character not allowed in an S3 path segment.
var sanitizeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// detectObject resolves the object name for a folder: path segments leaf→root (skipping generic, date and
// filter dirs, peeling a trailing year/counter token off a compound name), then the dominant FITS OBJECT
// header among the lights, else "" (the caller falls back to the legacy layout).
func detectObject(folderRel string, files []FileInfo) string {
	segs := strings.Split(folderRel, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := strings.TrimSpace(segs[i])
		if s == "" || genericDirs[strings.ToLower(s)] || filterDirs[strings.ToLower(s)] || parsePathDate(s) != "" {
			continue
		}
		if obj := peelObject(s); obj != "" {
			return normalizeObject(obj)
		}
	}
	if obj := dominantObject(files); obj != "" {
		return normalizeObject(obj)
	}
	return ""
}

// peelObject drops a trailing standalone year or short counter token from a compound name (M81_M82_2020 →
// M81_M82, orion_2019 → orion, moon_2 → moon), leaving single tokens like NGC6888 / C2019 intact.
func peelObject(s string) string {
	tokens := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == ' ' || r == '.' })
	for len(tokens) > 1 {
		last := tokens[len(tokens)-1]
		if yearRe.MatchString(last) || counterRe.MatchString(last) {
			tokens = tokens[:len(tokens)-1]
			continue
		}
		break
	}
	return strings.Join(tokens, "_")
}

// normalizeObject upper-cases a leading catalogue prefix and strips illegal path characters.
func normalizeObject(s string) string {
	s = catalogRe.ReplaceAllStringFunc(s, strings.ToUpper)
	s = sanitizeRe.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

// dominantObject returns the most common non-empty OBJECT header among the light frames.
func dominantObject(files []FileInfo) string {
	counts := map[string]int{}
	for _, f := range files {
		if f.Type == Light && strings.TrimSpace(f.Object) != "" {
			counts[strings.TrimSpace(f.Object)]++
		}
	}
	best, bestN := "", 0
	for o, n := range counts {
		if n > bestN {
			best, bestN = o, n
		}
	}
	return best
}
