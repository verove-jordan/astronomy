package pipeline

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/verove-jordan/astronomy/internal/skycat"
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
	add := func(tok string) {
		if low := strings.ToLower(tok); tok != "" && !seen[low] {
			seen[low] = true
			out = append(out, tok)
		}
	}
	for _, tok := range strings.FieldsFunc(object, func(r rune) bool {
		return r == '_' || r == '-' || r == ' ' || r == '.'
	}) {
		add(tok)
	}
	// Compound names without separators ("M81M82", "NGC7023mosaic") hide their designations from
	// the split above — no ladder rung resolved them, so SPCC/annotation ran without a position
	// seed. Extract embedded catalogue tokens directly (word boundaries cannot split digit→letter).
	for _, tok := range compoundCatalogRe.FindAllString(object, -1) {
		add(strings.ReplaceAll(strings.ToUpper(tok), " ", ""))
	}
	return out
}

// compoundCatalogRe matches catalogue designations embedded in compound folder names.
var compoundCatalogRe = regexp.MustCompile(`(?i)(?:M|NGC|IC|SH2|LDN)\s*\d{1,5}`)

// resolveSolveCoords finds the plate-solve position seed for a run, trying in order: coords already
// configured on Solve, the user's explicit target (name or "RA,Dec"), the object name's tokens, then
// every input folder's path segments leaf→root. The last rung is what rescues captures whose leaf dir
// is a software placeholder — task #316's lights sat in .../triplet_m66/CapObj, where "CapObj"
// (SharpCap's default) resolves to nothing and only the parent tokenizes to a catalogued "m66"; with
// no seed Siril's internal solver hard-fails and SPCC never runs. Run/output NAMING deliberately does
// not use this ladder: renaming would disconnect the target's prior sessions in the reuse catalog.
// Returns the coords ("ra,dec" decimal degrees) plus a human-readable source, or "", "".
func resolveSolveCoords(opts *Options, object string) (coords, source string) {
	if opts.Solve.Coords != "" {
		return opts.Solve.Coords, "configured"
	}
	if c, ok := resolveTargetHint(opts.TargetHint, opts.CatalogDir); ok {
		return c, fmt.Sprintf("target %q", opts.TargetHint)
	}
	for _, name := range objectCandidates(object) {
		if c, ok := skycat.Resolve(name, opts.CatalogDir); ok {
			return c, fmt.Sprintf("object name %q", name)
		}
	}
	for _, dir := range opts.scanRoots() {
		if c, name, seg, ok := resolvePathSegments(dir, opts.CatalogDir); ok {
			return c, fmt.Sprintf("folder %q (matched %q)", seg, name)
		}
	}
	return "", ""
}

// resolveTargetHint resolves a user-supplied target: an explicit "RA,Dec" pair (decimal degrees or
// sexagesimal, via skycat's FITS-header parsers) or a catalogue name.
func resolveTargetHint(hint, catalogDir string) (string, bool) {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return "", false
	}
	if ra, dec, ok := parseCoordPair(hint); ok {
		return fmt.Sprintf("%.5f,%.5f", ra, dec), true
	}
	if c, ok := skycat.Resolve(hint, catalogDir); ok {
		return c, true
	}
	return "", false
}

// parseCoordPair splits "RA,Dec" and parses both halves ("170.06,12.99" or "10:47:12,+12:59:30").
func parseCoordPair(s string) (ra, dec float64, ok bool) {
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	ra, okRA := skycat.ParseRA(strings.TrimSpace(parts[0]))
	dec, okDec := skycat.ParseDec(strings.TrimSpace(parts[1]))
	if !okRA || !okDec {
		return 0, 0, false
	}
	return ra, dec, true
}

// resolvePathSegments walks dir's path segments leaf→root, skipping the generic capture buckets and
// date-like folders smartObject skips, and returns the first segment with a catalogued token.
func resolvePathSegments(dir, catalogDir string) (coords, name, segment string, ok bool) {
	segs := strings.Split(filepath.Clean(dir), string(filepath.Separator))
	for i := len(segs) - 1; i >= 0; i-- {
		s := strings.TrimSpace(segs[i])
		if s == "" || s == "." || s == ".." {
			continue
		}
		if low := strings.ToLower(s); genericCaptureDir[low] || dateLikeRe.MatchString(s) {
			continue
		}
		for _, cand := range objectCandidates(s) {
			if c, found := skycat.Resolve(cand, catalogDir); found {
				return c, cand, s, true
			}
		}
	}
	return "", "", "", false
}
