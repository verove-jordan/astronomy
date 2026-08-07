package annotate

import (
	"image"
	_ "image/png" // registers the PNG decoder for image.Decode
	"os"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// The master→PNG row order is the one orientation question the annotation used to answer from
// metadata alone, by reading the master's ROWORDER card. That card cannot be trusted: it describes
// how the FITS was written, not what the finish did with it. Measured on a real M42 run, the master
// carried ROWORDER='TOP-DOWN' — so no flip was applied — yet the delivered final.png was mirrored
// against it, and every label and footprint landed reflected about the horizontal axis. Because the
// WCS-flip validation works entirely in the master's own file grid, it happily validated the wrong
// answer: the result was internally consistent and globally upside down.
//
// So this is now decided the same way the WCS flip already was — empirically, against the pixels
// that actually ship. Detect stars on final.png, project the master's own detections both ways, and
// keep whichever lines up. The card becomes the fallback for when the measurement cannot decide.

const (
	// rowFlipProbeStars caps how many of the master's brightest peaks are projected. A few hundred is
	// far more than enough to separate two hypotheses that differ by a whole mirror.
	rowFlipProbeStars = 300
	// rowFlipTolPx is how close a projected peak must land to a PNG detection to count. Generous
	// enough for the finish's resampling, tight enough that a mirrored field cannot match by luck.
	rowFlipTolPx = 3.0
	// rowFlipMinMatches / rowFlipDominance: the winner must both clear a floor and clearly beat the
	// loser, mirroring chooseFlip's rule. A sparse or badly-stretched final image simply declines to
	// answer, and the ROWORDER card is used instead.
	rowFlipMinMatches = 12
	rowFlipDominance  = 3
)

// finalPeaks detects stars directly on the delivered PNG. The PNG is 8-bit and non-linearly
// stretched, so its peak list is not comparable to the master's in COUNT — but the bright stars are
// in the same places, which is all a flip test needs.
func finalPeaks(path string) ([]postprocess.StarPeak, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	im := fits.NewImage(w, h, 1)
	lum := im.Pix[0]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bb, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			lum[y*w+x] = float32(0.25*float64(r)+0.5*float64(g)+0.25*float64(bb)) / 65535
		}
	}
	return postprocess.DetectStarPeaks(im, postprocess.StarDetectOptions{
		Sigma: 5, MaxStars: -1, MinSepPx: 6, SatLevel: 2, MaxHalfMax: 40,
	}), nil
}

// chooseRowFlip measures the master→final row order. It returns the chosen flip, how many probes
// matched under it, how many were tried, and whether the measurement was conclusive; when it is not,
// the caller keeps the ROWORDER card's answer.
func chooseRowFlip(m mapping, masterPeaks []postprocess.StarPeak, finalPath string) (flip bool, matched, tried int, ok bool) {
	pngPeaks, err := finalPeaks(finalPath)
	if err != nil || len(pngPeaks) == 0 {
		return m.fileFlip, 0, 0, false
	}
	grid := newPeakGrid(pngPeaks)

	probes := masterPeaks
	if len(probes) > rowFlipProbeStars {
		probes = probes[:rowFlipProbeStars] // brightest first
	}
	score := func(f bool) int {
		mm := m
		mm.fileFlip = f
		n := 0
		for _, p := range probes {
			x, y, in := mm.toFinal(float64(p.X), float64(p.Y))
			if !in {
				continue
			}
			if _, found := grid.nearest(x, y, rowFlipTolPx); found {
				n++
			}
		}
		return n
	}
	asIs, mirrored := score(false), score(true)
	winner, loser, chosen := asIs, mirrored, false
	if mirrored > asIs {
		winner, loser, chosen = mirrored, asIs, true
	}
	if winner < rowFlipMinMatches || winner < rowFlipDominance*loser {
		return m.fileFlip, winner, len(probes), false
	}
	return chosen, winner, len(probes), true
}
