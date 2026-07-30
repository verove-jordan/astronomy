// Post-compose orientation guard for the colour base — see ensureRowOrientation.
package pipeline

import (
	"fmt"
	"math"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// ensureRowOrientation compares the FILE row order of a composed FITS (the colour base) against a
// reference channel master and reverses its rows in place when they are decisively mirrored.
// Siril's compose can emit its output with reversed file rows relative to its inputs (observed with
// 1.4.3 rgbcomp on the nebula path) while stamping the SAME ROWORDER card — the stretched base TIFF
// then enters the GIMP composite upside-down relative to the L/Ha layers, which load straight from
// the aligned masters (M42: the final's luminance was vertically mirrored against its colours). The
// card lies in exactly this case, so the guard compares CONTENT: normalized row-sum profiles,
// direct vs mirrored. A non-decisive margin leaves the file untouched — no fix beats a wrong one.
func ensureRowOrientation(path, refPath string) (string, error) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return "", err
	}
	ref, err := fits.ReadImage(refPath)
	if err != nil {
		return "", err
	}
	if im.W != ref.W || im.H != ref.H {
		return "", nil // different canvas — geometry is owned elsewhere; nothing to compare
	}
	direct, mirrored := rowProfileMatch(im, ref)
	if mirrored <= direct+0.15 {
		return "", nil
	}
	for c := range im.Pix {
		reverseRows(im.Pix[c], im.W, im.H)
	}
	if err := im.OverwriteData(path); err != nil {
		return "", fmt.Errorf("orientation fix: %w", err)
	}
	return fmt.Sprintf("base orientation corrected — the composed base was row-mirrored vs %s (match %.2f direct / %.2f mirrored)",
		filepath.Base(refPath), direct, mirrored), nil
}

// rowProfileMatch correlates the two images' normalized row-sum profiles (plane 0), direct and
// vertically mirrored. Row sums are invariant to horizontal shifts and cheap at full height, and
// the real cases are unambiguous either way (measured 0.99 vs 0.25 on the mirrored M42 base,
// 0.09 vs 0.79 on a healthy deepsky base).
func rowProfileMatch(im, ref *fits.Image) (direct, mirrored float64) {
	pa := rowProfile(im)
	pb := rowProfile(ref)
	n := len(pa)
	var d, m float64
	for y := 0; y < n; y++ {
		d += pa[y] * pb[y]
		m += pa[y] * pb[n-1-y]
	}
	return d / float64(n), m / float64(n)
}

// rowProfile returns the zero-mean, unit-variance row-sum profile of the first plane.
func rowProfile(im *fits.Image) []float64 {
	p := make([]float64, im.H)
	pix := im.Pix[0]
	for y := 0; y < im.H; y++ {
		s := 0.0
		row := y * im.W
		for x := 0; x < im.W; x++ {
			s += float64(pix[row+x])
		}
		p[y] = s
	}
	var mean float64
	for _, v := range p {
		mean += v
	}
	mean /= float64(len(p))
	var vr float64
	for i := range p {
		p[i] -= mean
		vr += p[i] * p[i]
	}
	sd := math.Sqrt(vr/float64(len(p))) + 1e-12
	for i := range p {
		p[i] /= sd
	}
	return p
}

// reverseRows mirrors one plane vertically in place.
func reverseRows(pix []float32, w, h int) {
	tmp := make([]float32, w)
	for y := 0; y < h/2; y++ {
		a := pix[y*w : y*w+w]
		b := pix[(h-1-y)*w : (h-1-y)*w+w]
		copy(tmp, a)
		copy(a, b)
		copy(b, tmp)
	}
}
