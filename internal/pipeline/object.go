package pipeline

import (
	"path/filepath"
	"regexp"
	"strings"
)

// genericCaptureDir holds the lowercased folder names that carry no target identity — the sorting
// buckets and format/type labels a user wraps around their lights. smartObject skips them so a run is
// named after its target (e.g. "MilkyWay"), not a generic leaf like "Sorted_DNG".
var genericCaptureDir = map[string]bool{
	"sorted_dng": true, "sorted_fits": true, "sorted_raw": true, "sorted": true,
	"dng": true, "dngs": true, "raw": true, "raws": true, "fits": true, "fit": true,
	"tif": true, "tiff": true, "jpg": true, "jpeg": true, "png": true, "heic": true,
	"lights": true, "light": true, "sub": true, "subs": true, "frame": true, "frames": true,
	"capture": true, "captures": true, "osc": true, "export": true, "exports": true,
	"stack": true, "stacked": true, "processed": true, "input": true, "inputs": true, "data": true,
}

// dateLikeRe matches a folder that is just a capture date (DD_MM_YYYY, YYYY-MM-DD, 20260513, …) — a
// per-session label, not a target identity, so smartObject skips it too.
var dateLikeRe = regexp.MustCompile(`^\d{1,4}[-_.]\d{1,2}[-_.]\d{1,4}$|^\d{6,8}$`)

// smartObject derives a meaningful, stable output-folder name (the target) from the input path: it
// walks the path from the leaf upward and returns the first segment that is neither a generic
// capture/format bucket nor a capture date. So input/MilkyWay/13_05_2026/Sorted_DNG → "MilkyWay",
// keeping every night of a target together under output/<target>/<runID> (consistent with the deepsky
// path). Falls back to the leaf base (then "session" via sanitize), so it is never empty and never
// worse than naming after the leaf folder.
func smartObject(dir string) string {
	clean := filepath.Clean(dir)
	segs := strings.Split(clean, string(filepath.Separator))
	for i := len(segs) - 1; i >= 0; i-- {
		s := strings.TrimSpace(segs[i])
		if s == "" || s == "." || s == ".." {
			continue
		}
		if low := strings.ToLower(s); genericCaptureDir[low] || dateLikeRe.MatchString(s) {
			continue
		}
		return sanitize(s)
	}
	return sanitize(filepath.Base(clean))
}

// objectCandidates returns plate-solve name candidates for an object/folder name: the full name first,
// then each of its tokens (split on the usual separators). A compound capture name like "M81_M82_2020"
// rarely matches a catalogue as a whole, but its tokens ("M81", "M82") do — giving the solver a
// position seed instead of a fragile blind solve. "Most specific first"; duplicates/empties dropped.
func objectCandidates(object string) []string {
	out := []string{object}
	seen := map[string]bool{strings.ToLower(object): true}
	for _, tok := range strings.FieldsFunc(object, func(r rune) bool {
		return r == '_' || r == '-' || r == ' ' || r == '.'
	}) {
		if low := strings.ToLower(tok); tok != "" && !seen[low] {
			seen[low] = true
			out = append(out, tok)
		}
	}
	return out
}
