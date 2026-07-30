package annotate

import (
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// countDetect tunes the shared detector for COUNTING rather than calibration sampling: the
// threshold stays noise-floor-relative (median + 5·MAD of the stack's own luma — a pure function
// of the pixels, so the count is stable run-to-run), the cap is removed, saturated bright-star
// cores are kept (their plateaus thin to one peak via MinSepPx), and the width filter is relaxed
// so bloomed bright stars still count while galaxy cores stay excluded.
var countDetect = postprocess.StarDetectOptions{
	Sigma:      5,
	MaxStars:   -1,
	MinSepPx:   6,
	SatLevel:   2, // ≥1 disables the saturation exclusion
	MaxHalfMax: 40,
}

// detectAndCount runs the detector once over the linear master and counts the peaks inside the
// final crop window (what the user actually sees). The full peak list is returned for label
// matching and flip validation.
func detectAndCount(im *fits.Image, m mapping) ([]postprocess.StarPeak, int) {
	peaks := postprocess.DetectStarPeaks(im, countDetect)
	n := 0
	for _, p := range peaks {
		if m.inWindow(p.X, p.Y) {
			n++
		}
	}
	return peaks, n
}
