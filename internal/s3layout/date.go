package s3layout

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// eveningShiftMs pulls a capture timestamp back 12 h before taking its date, so after-midnight frames
// group with the evening the session started ("night of"). Applied only to the timestamp fallbacks,
// never to a date a user wrote into a folder name.
const eveningShiftMs = 12 * 60 * 60 * 1000

// ymdRe matches YYYY[sep]MM[sep]DD at the start of a segment (2019-08-26, 2021_08_14, and the prefix of
// 2019-08-26_03_33_51Z). dmyRe matches a full DD[sep]MM[sep]YYYY segment (04_04_2020). compactRe matches
// a bare YYYYMMDD (20260513).
var (
	ymdRe     = regexp.MustCompile(`^((?:19|20)\d\d)[-_.](\d{1,2})[-_.](\d{1,2})`)
	dmyRe     = regexp.MustCompile(`^(\d{1,2})[-_.](\d{1,2})[-_.]((?:19|20)\d\d)$`)
	compactRe = regexp.MustCompile(`^((?:19|20)\d\d)(\d\d)(\d\d)$`)
)

// detectDate resolves the session date dir: a full date written in the path wins; else the median light
// DATE-OBS shifted to "night of"; else a bare year token in the path; else the earliest file mtime; else
// "unknown-date" (never blank, so a light always has a lum/<object>/<date>/ home).
func detectDate(folderRel string, files []FileInfo) string {
	for _, s := range strings.Split(folderRel, "/") {
		if d := parsePathDate(s); d != "" {
			return d
		}
	}
	if ms := medianLightDateObs(files); ms > 0 {
		return dateFromMs(ms - eveningShiftMs)
	}
	if y := bareYear(folderRel); y != "" {
		return y
	}
	if ms := minMTime(files); ms > 0 {
		return dateFromMs(ms - eveningShiftMs)
	}
	return "unknown-date"
}

// parsePathDate normalizes a date-like path segment to YYYY-MM-DD, or "" if the segment is not a date.
func parsePathDate(s string) string {
	if m := ymdRe.FindStringSubmatch(s); m != nil {
		return normDate(m[1], m[2], m[3])
	}
	if m := dmyRe.FindStringSubmatch(s); m != nil {
		return normDate(m[3], m[2], m[1])
	}
	if m := compactRe.FindStringSubmatch(s); m != nil {
		return normDate(m[1], m[2], m[3])
	}
	return ""
}

// normDate validates and zero-pads a Y/M/D triple to YYYY-MM-DD (returns "" on an out-of-range month/day).
func normDate(y, mo, d string) string {
	mi, _ := strconv.Atoi(mo)
	di, _ := strconv.Atoi(d)
	if mi < 1 || mi > 12 || di < 1 || di > 31 {
		return ""
	}
	return fmt.Sprintf("%s-%02d-%02d", y, mi, di)
}

// bareYear returns a standalone 4-digit year token found anywhere in the path (M81_M82_2020 → "2020"),
// the coarse fallback when no DATE-OBS is available.
func bareYear(folderRel string) string {
	for _, seg := range strings.Split(folderRel, "/") {
		for _, tok := range strings.FieldsFunc(seg, func(r rune) bool {
			return r == '_' || r == '-' || r == ' ' || r == '.'
		}) {
			if yearRe.MatchString(tok) {
				return tok
			}
		}
	}
	return ""
}

// medianLightDateObs returns the median DATE-OBS (ms) of the light frames that carry one, or 0.
func medianLightDateObs(files []FileInfo) int64 {
	var ts []int64
	for _, f := range files {
		if f.Type == Light && f.DateObsMs > 0 {
			ts = append(ts, f.DateObsMs)
		}
	}
	if len(ts) == 0 {
		return 0
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	return ts[len(ts)/2]
}

// minMTime returns the earliest file mtime (ms) across all files, or 0.
func minMTime(files []FileInfo) int64 {
	var min int64
	for _, f := range files {
		if f.MTimeMs > 0 && (min == 0 || f.MTimeMs < min) {
			min = f.MTimeMs
		}
	}
	return min
}

// dateFromMs formats an epoch-ms timestamp as a UTC YYYY-MM-DD date.
func dateFromMs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02")
}

// leaf returns the last slash-segment of p ("" for "" or ".").
func leaf(p string) string {
	if p == "" || p == "." {
		return ""
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	return parts[len(parts)-1]
}

// isGenericDir reports whether a directory name is a generic/calibration container (no object identity).
func isGenericDir(name string) bool { return genericDirs[strings.ToLower(name)] }
