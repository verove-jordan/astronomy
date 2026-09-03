package calib

// dims.go keeps a master that was shot on a different sensor from being handed to Siril.
//
// Siril does NOT fail on a master of the wrong size. `calibrate` logs "Images must have same
// dimensions.", SKIPS that correction, writes the pp_ frames anyway and finishes with "Sequence
// processing succeeded" and exit status 0 — measured on Siril 1.4.3 with a 200×150 flat against
// 300×200 lights: the same flat at the lights' own size divided out (a left/right gradient came
// through as a 0.333 ratio), the mismatched one left every pixel exactly as it went in. The
// pipeline only warns when Siril RETURNS an error, so such a run reported success while its lights
// were stacked raw — and Selection.Notes still named the master as applied.
//
// Nothing upstream prevents it. bestDark and bestBias gate on gain/offset/bin; pickFlat gates on
// nothing at all (sameCamera is only a tiebreaker inside flatBeats, so a flat is borrowed across
// cameras by design, to share dust and vignetting between filters and nights); and no master
// carries a sensor identity — master_frames.instrument exists in the schema but is never written.
// A library that has served one camera therefore offers all of its masters to the next camera whose
// gain/offset/bin happen to line up, which is the ordinary case the first night a new body is used.
//
// The filtering happens on the CANDIDATE POOL rather than on the finished Selection, and that
// distinction is the whole point. Removing a master after the match has already chosen it loses the
// fallback: on an ASI2600MC capture, bestFlat's filter-matched pass picked the one library flat
// recorded as "RGB" (a Nikon master, 6064×4040), and striking it from the result left the set with
// no flat at all — while the capture's own fifty flats sat unused, invisible to that pass because
// nameColorChannel labels only LIGHTS "RGB" and leaves calibration frames' filter empty. Filtering
// first lets bestFlat's second, filter-blind pass find them.
//
// Pixel dimensions are the test because they are the one property every master carries in its own
// header, they cost a few KB to read, and no two sensors that differ can share them.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// KeepMatchingDims returns the masters that can actually be applied to lightPath — those whose pixel
// dimensions equal the light's — plus a note naming what was excluded ("" when nothing was).
//
// A master whose dimensions cannot be read is KEPT: an unreadable header is not evidence of a
// mismatch. That covers the two cases that matter — a master still to be built from this capture's
// own frames has no path yet, and a library master not yet pulled from the S3 mirror is fetched
// after selection — so neither is thrown away on the strength of a failed open.
//
// When the light frame itself cannot be read (a camera raw is not a FITS) the pool is returned
// untouched: with nothing to compare against, the check cannot run.
func KeepMatchingDims(masters []Master, lightPath string) ([]Master, string) {
	lw, lh, ok := imageDims(lightPath)
	if !ok {
		return masters, ""
	}
	kept := make([]Master, 0, len(masters))
	dropped := map[string]int{}
	for _, m := range masters {
		w, h, ok := imageDims(m.Path)
		if !ok || (w == lw && h == lh) {
			kept = append(kept, m)
			continue
		}
		dropped[fmt.Sprintf("%d×%d", w, h)]++
	}
	if len(dropped) == 0 {
		return kept, ""
	}
	return kept, fmt.Sprintf(
		"%d calibration master(s) from another sensor (%s) were not considered — these lights are %d×%d",
		total(dropped), describeDims(dropped), lw, lh)
}

// poolFor is KeepMatchingDims for a whole light set, taking one representative frame as the size
// reference — a set is by definition one camera at one binning, so every frame in it agrees.
func poolFor(set inspect.Set, masters []Master) ([]Master, string) {
	if len(set.Frames) == 0 {
		return masters, ""
	}
	return KeepMatchingDims(masters, set.Frames[0].Path)
}

// describeDims renders the dropped masters' sizes as "6064×4040 (×3), 4032×3024", biggest group
// first, so the note says WHICH other camera's masters the library offered.
func describeDims(dropped map[string]int) string {
	dims := make([]string, 0, len(dropped))
	for d := range dropped {
		dims = append(dims, d)
	}
	sort.Slice(dims, func(i, j int) bool {
		if dropped[dims[i]] != dropped[dims[j]] {
			return dropped[dims[i]] > dropped[dims[j]]
		}
		return dims[i] < dims[j]
	})
	parts := make([]string, 0, len(dims))
	for _, d := range dims {
		if n := dropped[d]; n > 1 {
			parts = append(parts, fmt.Sprintf("%s (×%d)", d, n))
		} else {
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, ", ")
}

func total(dropped map[string]int) int {
	n := 0
	for _, c := range dropped {
		n += c
	}
	return n
}

// imageDims reads a FITS file's pixel dimensions from its header alone — fits.Open parses the header
// and never touches the data unit, so this costs a few KB per master rather than the master itself.
// ok is false when the file is unreadable, is not a FITS, or declares no usable NAXIS1/NAXIS2.
func imageDims(path string) (w, h int, ok bool) {
	if path == "" {
		return 0, 0, false
	}
	f, err := fits.Open(path)
	if err != nil {
		return 0, 0, false
	}
	w, h = f.Dimensions()
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// SameDims reports whether two image files share their pixel dimensions. known is false when either
// header could not be read, in which case same carries no information — callers guard on known.
func SameDims(a, b string) (same, known bool) {
	aw, ah, aok := imageDims(a)
	bw, bh, bok := imageDims(b)
	if !aok || !bok {
		return false, false
	}
	return aw == bw && ah == bh, true
}
