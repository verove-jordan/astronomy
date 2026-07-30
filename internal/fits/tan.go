package fits

import (
	"math"
	"strings"
)

// WCS is a parsed TAN (gnomonic) plate solution, normalized to an explicit CD matrix (a PC matrix
// + CDELT — Siril 1.4's representation — is folded in; PC defaults to identity per the FITS
// convention). Coordinates are J2000 degrees on both sides; no precession is applied anywhere —
// catalogues used against these solutions must be J2000 too.
type WCS struct {
	RA0, Dec0      float64       // CRVAL1/2: the tangent point
	CRPix1, CRPix2 float64       // 1-based FITS reference pixel
	CD             [2][2]float64 // degrees/pixel
	inv            [2][2]float64 // CD⁻¹, precomputed
}

// ParseWCS extracts a usable TAN solution from h. ok=false when CRVAL/CRPIX are missing, CTYPE1
// is present and not a TAN projection, or the matrix is singular/absent.
func ParseWCS(h *Header) (WCS, bool) {
	if h == nil {
		return WCS{}, false
	}
	if ctype, ok := h.String("CTYPE1"); ok && !strings.Contains(strings.ToUpper(ctype), "TAN") {
		return WCS{}, false
	}
	ra0, ok1 := h.Float("CRVAL1")
	dec0, ok2 := h.Float("CRVAL2")
	px1, ok3 := h.Float("CRPIX1")
	px2, ok4 := h.Float("CRPIX2")
	if !(ok1 && ok2 && ok3 && ok4) {
		return WCS{}, false
	}
	cd, ok := cdMatrix(h)
	if !ok {
		return WCS{}, false
	}
	return NewTanWCS(ra0, dec0, px1, px2, cd)
}

// cdMatrix reads the explicit CD matrix, or reconstructs it from PC + CDELT (CD_ij = CDELTi·PC_ij).
func cdMatrix(h *Header) ([2][2]float64, bool) {
	c11, ok1 := h.Float("CD1_1")
	c12, ok2 := h.Float("CD1_2")
	c21, ok3 := h.Float("CD2_1")
	c22, ok4 := h.Float("CD2_2")
	if ok1 && ok2 && ok3 && ok4 {
		return [2][2]float64{{c11, c12}, {c21, c22}}, true
	}
	d1, ok1 := h.Float("CDELT1")
	d2, ok2 := h.Float("CDELT2")
	if !(ok1 && ok2) {
		return [2][2]float64{}, false
	}
	return [2][2]float64{
		{d1 * h.floatOr("PC1_1", 1), d1 * h.floatOr("PC1_2", 0)},
		{d2 * h.floatOr("PC2_1", 0), d2 * h.floatOr("PC2_2", 1)},
	}, true
}

// SkyToPix projects J2000 (ra,dec) degrees to 0-based pixel coordinates in the FITS axis frame
// (x along axis 1, y along axis 2 — mapping axis 2 to file rows is the caller's concern).
// ok=false when the point is at or beyond 90° from the tangent point.
func (w WCS) SkyToPix(raDeg, decDeg float64) (x, y float64, ok bool) {
	const degRad = math.Pi / 180
	sinD, cosD := math.Sincos(decDeg * degRad)
	sinD0, cosD0 := math.Sincos(w.Dec0 * degRad)
	sinA, cosA := math.Sincos((raDeg - w.RA0) * degRad)

	div := sinD*sinD0 + cosD*cosD0*cosA // cosine of the angular distance to the tangent point
	if div < 1e-12 {
		return 0, 0, false
	}
	xi := cosD * sinA / div / degRad                     // standard coordinate ξ, degrees
	eta := (sinD*cosD0 - cosD*sinD0*cosA) / div / degRad // standard coordinate η, degrees
	p1 := w.CRPix1 + w.inv[0][0]*xi + w.inv[0][1]*eta    // 1-based pixel along axis 1
	p2 := w.CRPix2 + w.inv[1][0]*xi + w.inv[1][1]*eta    // 1-based pixel along axis 2
	return p1 - 1, p2 - 1, true
}

// PixToSky is the inverse gnomonic projection: 0-based pixel → J2000 degrees, RA in [0,360).
func (w WCS) PixToSky(x, y float64) (raDeg, decDeg float64) {
	const degRad = math.Pi / 180
	dx, dy := x+1-w.CRPix1, y+1-w.CRPix2
	xi := (w.CD[0][0]*dx + w.CD[0][1]*dy) * degRad
	eta := (w.CD[1][0]*dx + w.CD[1][1]*dy) * degRad

	sinD0, cosD0 := math.Sincos(w.Dec0 * degRad)
	norm := math.Sqrt(1 + xi*xi + eta*eta)
	decDeg = math.Asin((sinD0+eta*cosD0)/norm) / degRad
	raDeg = w.RA0 + math.Atan2(xi, cosD0-eta*sinD0)/degRad
	raDeg = math.Mod(raDeg, 360)
	if raDeg < 0 {
		raDeg += 360
	}
	return raDeg, decDeg
}

// ScaleArcsecPerPix returns the mean plate scale √|det CD| in arcseconds per pixel.
func (w WCS) ScaleArcsecPerPix() float64 {
	det := w.CD[0][0]*w.CD[1][1] - w.CD[0][1]*w.CD[1][0]
	return math.Sqrt(math.Abs(det)) * 3600
}
