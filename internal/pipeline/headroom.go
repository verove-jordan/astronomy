// Highlight headroom for the deep-sky finishing stretch. The MTF autostretch maps the brightest linear
// pixel to 1.0, so a saturated star core (or galaxy nucleus) clips to pure white and loses its per-
// channel colour ratios before any GIMP highlight shoulder runs. Capping the linear highlights just
// below 1.0 first — with a ratio-preserving roll-off — means the stretch maps them just below white and
// the star keeps its natural hue. This is the deep-sky analogue of the planetary finish Headroom/fmul.
package pipeline

import (
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/nightscape"
)

// stretchHeadroomKneeGap is how far below the ceiling the roll-off begins: only the brightest linear
// highlights (star cores, galaxy nuclei) are compressed; everything dimmer is identity, so the sky /
// galaxy / nebula tones stay exactly as they were.
const stretchHeadroomKneeGap = 0.05

// applyStretchHeadroom rolls off the linear highlights of src so bright cores are capped at `headroom`
// (< 1.0) with their per-channel ratios (colour) intact, writing the result to dst — in place when
// src == dst (header preserved), otherwise as a fresh FITS. Mono or RGB. headroom ≤ 0 or ≥ 1 → no-op
// ("", nil). Soft-fail: returns a note and any error; callers keep going.
func applyStretchHeadroom(src, dst string, headroom float64) (string, error) {
	if headroom <= 0 || headroom >= 1 {
		return "", nil
	}
	im, err := fits.ReadImage(src)
	if err != nil {
		return "", fmt.Errorf("stretch headroom: read %s: %w", filepath.Base(src), err)
	}
	knee := headroom - stretchHeadroomKneeGap
	if knee <= 0 {
		knee = headroom * 0.9
	}
	nightscape.CompressHighlights(im, knee, headroom)
	if src == dst {
		err = im.OverwriteData(dst)
	} else {
		err = im.WriteFITS(dst)
	}
	if err != nil {
		return "", fmt.Errorf("stretch headroom: write %s: %w", filepath.Base(dst), err)
	}
	return fmt.Sprintf("highlight headroom (linear cores capped ≤ %.2f, colour preserved)", headroom), nil
}

// headroomSource returns the FITS path a mono/L stretch should load: when headroom is active it writes a
// highlight-rolled copy of src into stretchDir/<name>_lin.fits and returns that; otherwise src unchanged.
// A read/write error is a soft note and falls back to the original src (the stretch still runs).
func headroomSource(src, name, stretchDir string, headroom float64, notes *[]string) string {
	if headroom <= 0 || headroom >= 1 {
		return src
	}
	dst := filepath.Join(stretchDir, name+"_lin.fits")
	n, err := applyStretchHeadroom(src, dst, headroom)
	if err != nil {
		*notes = append(*notes, name+" stretch headroom skipped: "+err.Error())
		return src
	}
	if n != "" {
		*notes = append(*notes, name+" "+n)
	}
	return dst
}
