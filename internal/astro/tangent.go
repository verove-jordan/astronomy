package astro

import "math"

// Gnomonic (TAN) tangent-plane projection, shared by the mosaic tile planner and the mosaic
// assembler's validation math. The formulae are the pixel-free half of internal/fits/tan.go
// (SkyToPix/PixToSky) so both sides agree on conventions: standard coordinate ξ is east-positive,
// η is north-positive, both in DEGREES; all sky coordinates are J2000 degrees.

// TangentPlane projects (raDeg,decDeg) onto the tangent plane at (ra0,dec0). ok=false when the
// point is at or beyond 90° from the tangent point (the projection diverges there). RA wrap and
// pole proximity are handled by the trigonometry: Δra only ever enters through sin/cos.
func TangentPlane(ra0, dec0, raDeg, decDeg float64) (xiDeg, etaDeg float64, ok bool) {
	sinD, cosD := math.Sincos(decDeg * deg2rad)
	sinD0, cosD0 := math.Sincos(dec0 * deg2rad)
	sinA, cosA := math.Sincos((raDeg - ra0) * deg2rad)

	div := sinD*sinD0 + cosD*cosD0*cosA // cosine of the angular distance to the tangent point
	if div < 1e-12 {
		return 0, 0, false
	}
	xiDeg = cosD * sinA / div * rad2deg
	etaDeg = (sinD*cosD0 - cosD*sinD0*cosA) / div * rad2deg
	return xiDeg, etaDeg, true
}

// TangentSky inverts TangentPlane: standard coordinates (degrees) back to J2000 RA/Dec degrees,
// RA normalized to [0,360).
func TangentSky(ra0, dec0, xiDeg, etaDeg float64) (raDeg, decDeg float64) {
	xi := xiDeg * deg2rad
	eta := etaDeg * deg2rad
	sinD0, cosD0 := math.Sincos(dec0 * deg2rad)

	norm := math.Sqrt(1 + xi*xi + eta*eta)
	decDeg = math.Asin(clamp1((sinD0+eta*cosD0)/norm)) * rad2deg
	raDeg = norm360(ra0 + math.Atan2(xi, cosD0-eta*sinD0)*rad2deg)
	return raDeg, decDeg
}
