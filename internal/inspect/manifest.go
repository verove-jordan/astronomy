package inspect

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Many older captures have bare filenames (e.g. 2021-02-03-2317_6-1-CapObj_0000.FIT) with no
// filter/gain/type in the header or name. A hand-written `info.txt` sidecar next to them records the
// capture order, one filter token per capture sub-run, plus gain/exposure/temperature, e.g.
//
//	LLL RR GG BB Ha Ha
//	gain L200 RGB250 Ha300
//
// meaning "3 luminance sub-runs, then 2 red, 2 green, 2 blue, 2 Ha; gain L=200, R/G/B=250, Ha=300".
// applyManifests parses these and back-fills the frames as a metadata FALLBACK: it only fills fields the
// header/filename left blank, and never overrides them. Numeric filter-wheel notations and count
// mismatches are warned, not forced.

// manifestSlot is one capture sub-run's metadata, in capture order (one slot per single filter token,
// so "LLL" expands to three L slots).
type manifestSlot struct {
	Filter     string
	Gain       int64
	HasGain    bool
	ExposureMs int64
}

// manifest is the parsed content of one info.txt sidecar: the ordered filter slots plus a global
// temperature, with any unparseable lines surfaced as warnings.
type manifest struct {
	Slots      []manifestSlot
	TempMilliC int64
	HasTemp    bool
	Warnings   []string
}

// knownFilters are matched longest-first so multi-char filters (Ha/OIII/SII) win over single letters.
// Aliases ("O3"→OIII, "S2"→SII) are included because hand-written info.txt legends use them;
// normalizeFilter canonicalizes each match, so keep this list in sync with filterToken (classify.go).
// when expanding a glued run like "LLLRGBHaHa". V (Johnson-V) is recognized and normalized to G (these
// older LRGB sessions used R/V/B), so an info.txt legend like "RVB" parses to R/G/B.
var knownFilters = []string{"OIII", "O3", "SII", "S2", "Ha", "L", "R", "G", "B", "V"}

var (
	reManifestFile = regexp.MustCompile(`(?i)^info\.txt(\.txt)?$`)
	reFilterNum    = regexp.MustCompile(`^([A-Za-z]+)(\d+)$`)
	reExpToken     = regexp.MustCompile(`^(\d*\.?\d+)s$`)
	reTempLine     = regexp.MustCompile(`(?i)^(-?\d+(?:\.\d+)?)\s*(?:°|º|deg|degc)$`)
	reArrowExp     = regexp.MustCompile(`(?i)^([A-Za-z]+)\s*->\s*(\d*\.?\d+)\s*s$`)
	reTimestamp    = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})[_-](\d{2})[_-](\d{2})[_-](\d{2})`)
)

// applyManifests finds info.txt sidecars under root and back-fills filter/gain/exposure/temperature onto
// the frames in each manifest's capture sub-directories, in chronological order, for fields the header
// and filename did not provide. It is best-effort: every problem is recorded in inv.Warnings.
func applyManifests(ctx context.Context, root string, inv *Inventory) {
	manifests := findManifests(ctx, root)
	if len(manifests) == 0 {
		return
	}
	framesByDir := map[string][]*Frame{}
	for _, fr := range inv.Frames {
		d := filepath.Dir(fr.Path)
		framesByDir[d] = append(framesByDir[d], fr)
	}
	for _, mf := range manifests {
		applyOneManifest(mf, framesByDir, inv)
	}
}

// findManifests walks root (directories only) and returns the paths of info.txt / info.txt.txt sidecars.
func findManifests(ctx context.Context, root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if reManifestFile.MatchString(d.Name()) {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// applyOneManifest parses one sidecar and labels the frames beneath it. When the frames carry physical
// EFW slots (sidecar/filename), those are ground truth and the manifest is used only as the slot→name
// legend (order-independent). Otherwise it maps the manifest's ordered slots onto the capture
// sub-directories positionally — aligning as many as line up rather than discarding everything on a
// count mismatch.
func applyOneManifest(mfPath string, framesByDir map[string][]*Frame, inv *Inventory) {
	label := rel(inv.Root, mfPath)
	data, err := os.ReadFile(mfPath)
	if err != nil {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf("%s: read failed: %v", label, err))
		return
	}
	man := parseManifest(string(data))
	for _, w := range man.Warnings {
		inv.Warnings = append(inv.Warnings, label+": "+w)
	}
	subdirs := captureSubdirs(filepath.Dir(mfPath), framesByDir)
	if assignByWheelSlot(man, subdirs, framesByDir, inv) {
		return // physical slots present → legend-based naming supersedes positional folder mapping
	}
	if len(man.Slots) == 0 {
		return // numeric/empty manifest and no slots — leave it to signal detection
	}
	applyPositional(man, subdirs, framesByDir, inv, label)
}

// applyPositional maps manifest slots onto capture sub-directories positionally (chronological order),
// aligning min(slots, folders) and warning about any remainder. This tolerates a hand-written info.txt
// that lists one more (or fewer) tokens than there are folders — e.g. a session cut short — instead of
// the old all-or-nothing skip that dumped every light into the guess-prone signal detector.
func applyPositional(man manifest, subdirs []string, framesByDir map[string][]*Frame, inv *Inventory, label string) {
	n := len(man.Slots)
	if len(subdirs) < n {
		n = len(subdirs)
	}
	for i := 0; i < n; i++ {
		applySlot(man, man.Slots[i], framesByDir[subdirs[i]])
	}
	if len(man.Slots) != len(subdirs) {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"%s: %d filter slots but %d capture folders — mapped the first %d in capture order, rest left to detection",
			label, len(man.Slots), len(subdirs), n))
	}
}

// captureSubdirs returns the directories at or below dir that directly contain frames, ordered
// chronologically by the capture timestamp in their name (lexical fallback).
func captureSubdirs(dir string, framesByDir map[string][]*Frame) []string {
	prefix := dir + string(filepath.Separator)
	var dirs []string
	for d := range framesByDir {
		if d == dir || strings.HasPrefix(d, prefix) {
			dirs = append(dirs, d)
		}
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		ki, kj := chronoKey(dirs[i]), chronoKey(dirs[j])
		if ki != kj {
			return ki < kj
		}
		return dirs[i] < dirs[j]
	})
	return dirs
}

// chronoKey extracts the YYYY-MM-DD_HH_MM_SS timestamp from a directory name (which sorts lexically as
// chronologically), falling back to the base name when there is none.
func chronoKey(dir string) string {
	base := filepath.Base(dir)
	if ts := reTimestamp.FindString(base); ts != "" {
		return ts
	}
	return base
}

// applySlot fills the frames of one capture sub-run from its slot, only where the header/filename left a
// field blank. A manifest filter implies a light frame. Gain 0 is treated as "unset" because the bare
// legacy files that need a manifest carry no GAIN card (the modern files that do are never overridden,
// as they already have a filter and a non-zero gain).
func applySlot(man manifest, slot manifestSlot, frames []*Frame) {
	for _, fr := range frames {
		if isCalibration(fr.Type) {
			continue // calibration/video frames are typed by their own folder, never by a light manifest
		}
		if fr.Filter == "" && slot.Filter != "" {
			fr.Filter = slot.Filter
			fr.ClassSource = SourceManifest
			fr.FilterConfidence = 1
			if fr.Type == Unknown {
				fr.Type = Light
			}
		}
		if fr.Gain == 0 && slot.HasGain {
			fr.Gain = slot.Gain
		}
		if fr.ExposureMs == 0 && slot.ExposureMs > 0 {
			fr.ExposureMs = slot.ExposureMs
		}
		if !fr.HasTemp && man.HasTemp {
			fr.TempMilliC, fr.HasTemp = man.TempMilliC, true
		}
	}
}

// parseManifest tokenizes an info.txt body into ordered filter slots, resolving gain/exposure by
// precedence (per-line inline > per-filter map > global single).
func parseManifest(text string) manifest {
	var man manifest
	perFilterGain := map[string]int64{}
	perFilterExp := map[string]int64{}
	var globalGain int64
	var hasGlobalGain bool
	var globalExpMs int64

	type seqLine struct {
		filters []string
		gain    int64
		hasGain bool
		expMs   int64
	}
	var seqs []seqLine

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		low := strings.ToLower(line)

		switch {
		case matchArrowExposure(line, perFilterExp):
			// handled (per-filter exposure recorded)
		case strings.HasPrefix(low, "gain") && looksLikeGainMap(fields):
			for f, g := range parseGainMap(fields[1:]) {
				perFilterGain[f] = g
			}
		case reTempLine.MatchString(line):
			man.TempMilliC, man.HasTemp = parseTempLine(line)
		default:
			filters, gain, hasGain, expMs, ok := parseSeqLine(fields)
			switch {
			case ok:
				seqs = append(seqs, seqLine{filters, gain, hasGain, expMs})
			case isBareGain(fields):
				globalGain, hasGlobalGain = bareGain(fields), true
			case isBareExposure(fields):
				globalExpMs = bareExposure(fields)
			default:
				man.Warnings = append(man.Warnings, "ignored line "+strconv.Quote(line))
			}
		}
	}

	for _, s := range seqs {
		for _, f := range s.filters {
			slot := manifestSlot{Filter: f}
			switch {
			case s.hasGain:
				slot.Gain, slot.HasGain = s.gain, true
			case has(perFilterGain, f):
				slot.Gain, slot.HasGain = perFilterGain[f], true
			case hasGlobalGain:
				slot.Gain, slot.HasGain = globalGain, true
			}
			switch {
			case s.expMs > 0:
				slot.ExposureMs = s.expMs
			case perFilterExp[f] > 0:
				slot.ExposureMs = perFilterExp[f]
			case globalExpMs > 0:
				slot.ExposureMs = globalExpMs
			}
			man.Slots = append(man.Slots, slot)
		}
	}
	return man
}

// parseSeqLine reads a filter-sequence line, expanding glued/spaced/repeated filter tokens and picking
// up an inline "N gain" and/or "Ns" exposure. ok is false when the line holds no filter token.
func parseSeqLine(fields []string) (filters []string, gain int64, hasGain bool, expMs int64, ok bool) {
	for i := 0; i < len(fields); i++ {
		tok := fields[i]
		if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
			if i+1 < len(fields) && strings.EqualFold(fields[i+1], "gain") {
				gain, hasGain = n, true
				i++
			}
			continue
		}
		if e, isExp := parseExposureToken(tok); isExp {
			expMs = e
			continue
		}
		if strings.EqualFold(tok, "gain") {
			continue
		}
		if fs, isFilter := expandFilterWord(tok); isFilter {
			filters = append(filters, fs...)
		}
	}
	return filters, gain, hasGain, expMs, len(filters) > 0
}

// expandFilterWord splits a contiguous filter word (e.g. "LLLRGBHaHa") into single normalized filter
// tokens, greedily matching multi-char filters first. ok is false if the word holds any non-filter
// character, so metadata words ("gain", "moon") are never mistaken for filters.
func expandFilterWord(word string) ([]string, bool) {
	var out []string
	for i := 0; i < len(word); {
		matched := ""
		for _, f := range knownFilters {
			if strings.HasPrefix(strings.ToLower(word[i:]), strings.ToLower(f)) {
				matched = f
				break
			}
		}
		if matched == "" {
			return nil, false
		}
		out = append(out, normalizeFilter(matched))
		i += len(matched)
	}
	return out, len(out) > 0
}

// looksLikeGainMap reports whether the tokens after a leading "gain" are all filter+number compounds
// (e.g. "L200", "RGB250"), distinguishing "gain L200 RGB250" from a bare "gain 300".
func looksLikeGainMap(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	for _, f := range fields[1:] {
		if !reFilterNum.MatchString(f) {
			return false
		}
	}
	return true
}

// parseGainMap turns ["L200","RGB250","Ha300"] into {L:200, R:250, G:250, B:250, Ha:300}.
func parseGainMap(tokens []string) map[string]int64 {
	out := map[string]int64{}
	for _, tok := range tokens {
		m := reFilterNum.FindStringSubmatch(tok)
		if m == nil {
			continue
		}
		g, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			continue
		}
		if fs, ok := expandFilterWord(m[1]); ok {
			for _, f := range fs {
				out[f] = g
			}
		}
	}
	return out
}

// matchArrowExposure records a "L -> 2s" per-filter exposure line, returning true when it matched.
func matchArrowExposure(line string, perFilterExp map[string]int64) bool {
	m := reArrowExp.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	sec, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return false
	}
	fs, ok := expandFilterWord(m[1])
	if !ok {
		return false
	}
	for _, f := range fs {
		perFilterExp[f] = int64(math.Round(sec * 1000))
	}
	return true
}

func parseExposureToken(tok string) (int64, bool) {
	m := reExpToken.FindStringSubmatch(strings.ToLower(tok))
	if m == nil {
		return 0, false
	}
	sec, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return int64(math.Round(sec * 1000)), true
}

func parseTempLine(line string) (int64, bool) {
	m := reTempLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return 0, false
	}
	c, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return int64(math.Round(c * 1000)), true
}

// isBareGain reports a line that is only a number and the word "gain" (in any order), e.g. "300 gain".
func isBareGain(fields []string) bool {
	num, gain := false, false
	for _, f := range fields {
		switch {
		case strings.EqualFold(f, "gain"):
			gain = true
		case isInt(f):
			num = true
		default:
			return false
		}
	}
	return num && gain
}

func bareGain(fields []string) int64 {
	for _, f := range fields {
		if n, err := strconv.ParseInt(f, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// isBareExposure reports a line that is only an exposure, e.g. "120s" or "30 s".
func isBareExposure(fields []string) bool {
	_, ok := parseExposureToken(strings.Join(fields, ""))
	return ok
}

func bareExposure(fields []string) int64 {
	ms, _ := parseExposureToken(strings.Join(fields, ""))
	return ms
}

func isInt(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

func has(m map[string]int64, k string) bool {
	_, ok := m[k]
	return ok
}
