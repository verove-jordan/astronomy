package pipeline

// solveoptics.go keeps the plate solver from being told about the wrong telescope.
//
// The run-level SolveOptions come from the ENGINE'S CONFIG (ASTRO_FOCAL_MM / ASTRO_PIXEL_UM),
// which describes the rig the engine was set up for — here a Takahashi FC-100 DF at 740 mm with an
// ASI1600MM's 3.8 µm pixels. Every run gets them, including a session shot on something else.
//
// A Nikon Z6 M31 set went through with `platesolve -focal=740 -pixelsize=3.8` against frames whose
// own header says XPIXSZ = 5.94. Those numbers claim 1.06 arcsec/px; the camera was nearer 6. A
// solve six times off in scale cannot succeed, so SPCC never ran, colour fell all the way down to
// the star-field gain fallback, and the finish reported "star colours flattened". Nothing in the
// run said the solver had been handed another telescope's optics.
//
// So: the frame's own header decides whether the configured optics belong to it. Pixel size is the
// test because it is the one number a converted raw reliably carries (Siril writes XPIXSZ from the
// camera; FOCALLEN it cannot know — the lens is not in a FITS header). When the two disagree, the
// configured focal length is not this session's either, and passing it is worse than passing
// nothing: it turns "could not solve" into "solved wrongly" whenever the search happens to land.
//
// A run can always say what its optics really were (RunRequest focal_mm / pixel_um), and then this
// steps aside — an explicit answer is never second-guessed.

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// pixelSizeTolFrac is how far the header's pixel size may sit from the configured one and still be
// the same camera. Generous: sensor specs get rounded (3.76 vs 3.8) and nobody should lose SPCC to
// a rounding difference.
const pixelSizeTolFrac = 0.05

// solveOpticsFor returns the solve options to use for imgPath, plus a warning when the configured
// optics had to be dropped because they describe a different camera.
func solveOpticsFor(s siril.SolveOptions, imgPath string, explicit bool) (siril.SolveOptions, string) {
	if explicit || s.PixelUm <= 0 {
		return s, ""
	}
	headerUm, ok := headerPixelUm(imgPath)
	if !ok || withinFrac(headerUm, s.PixelUm, pixelSizeTolFrac) {
		return s, ""
	}
	out := s
	out.FocalMM, out.PixelUm = 0, 0
	return out, fmt.Sprintf(
		"plate solving has no optics for this session: the frames report %.2f µm pixels but the engine is "+
			"configured for %.2f µm at %.0f mm, so those belong to another camera and were not used — "+
			"set focal_mm (and pixel_um) on the run to enable SPCC colour calibration",
		headerUm, s.PixelUm, s.FocalMM)
}

// headerPixelUm reads XPIXSZ from a FITS header. ok is false when the file or the keyword is
// unreadable, which means the check cannot run and the configured optics stand.
func headerPixelUm(path string) (float64, bool) {
	f, err := fits.Open(path)
	if err != nil {
		return 0, false
	}
	v, ok := f.Header.Float("XPIXSZ")
	if !ok || v <= 0 {
		return 0, false
	}
	return v, true
}

func withinFrac(a, b, frac float64) bool {
	if b == 0 {
		return a == 0
	}
	return math.Abs(a-b)/math.Abs(b) <= frac
}
