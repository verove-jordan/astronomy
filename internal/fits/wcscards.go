package fits

import (
	"math"
	"strconv"
)

// Writing side of the TAN plate-solution support: the engine synthesizes solved FITS files (mosaic
// canvases assembled in Go) whose headers ParseWCS — and Siril/GIMP/star annotation — read back
// like any Siril-solved image.

// NewTanWCS builds a TAN solution from explicit parameters: J2000 tangent point (degrees), 1-based
// FITS reference pixel, CD matrix in degrees/pixel. It precomputes the inverse matrix SkyToPix
// needs (a hand-built WCS literal would silently project everything onto CRPIX). ok=false when the
// matrix is singular. The degenerate test is scale-relative: hardware FMA fusing can leave a ~1e-24
// residue where exact arithmetic gives 0, so an == 0 check misses truly singular matrices.
func NewTanWCS(ra0, dec0, crPix1, crPix2 float64, cd [2][2]float64) (WCS, bool) {
	det := cd[0][0]*cd[1][1] - cd[0][1]*cd[1][0]
	scale := math.Abs(cd[0][0]*cd[1][1]) + math.Abs(cd[0][1]*cd[1][0])
	if det == 0 || math.Abs(det) < scale*1e-12 {
		return WCS{}, false
	}
	w := WCS{RA0: ra0, Dec0: dec0, CRPix1: crPix1, CRPix2: crPix2, CD: cd}
	w.inv = [2][2]float64{
		{cd[1][1] / det, -cd[0][1] / det},
		{-cd[1][0] / det, cd[0][0] / det},
	}
	return w, true
}

// Cards returns the padded FITS header cards encoding w (TAN projection, J2000), ready to pass to
// Image.WriteFITSWith.
func (w WCS) Cards() []string {
	return []string{
		strCard("CTYPE1", "RA---TAN", "TAN (gnomonic) projection"),
		strCard("CTYPE2", "DEC--TAN", "TAN (gnomonic) projection"),
		strCard("CUNIT1", "deg", ""),
		strCard("CUNIT2", "deg", ""),
		card("CRVAL1", wcsFloat(w.RA0), "RA of the tangent point [deg]"),
		card("CRVAL2", wcsFloat(w.Dec0), "Dec of the tangent point [deg]"),
		card("CRPIX1", wcsFloat(w.CRPix1), "reference pixel along axis 1 (1-based)"),
		card("CRPIX2", wcsFloat(w.CRPix2), "reference pixel along axis 2 (1-based)"),
		card("CD1_1", wcsFloat(w.CD[0][0]), "transformation matrix [deg/px]"),
		card("CD1_2", wcsFloat(w.CD[0][1]), ""),
		card("CD2_1", wcsFloat(w.CD[1][0]), ""),
		card("CD2_2", wcsFloat(w.CD[1][1]), ""),
		card("EQUINOX", "2000.", "equinox of celestial coordinates"),
	}
}

// wcsFloat formats a header float with full useful precision; ParseFloat reads the E-notation back.
func wcsFloat(v float64) string {
	return strconv.FormatFloat(v, 'G', 15, 64)
}
