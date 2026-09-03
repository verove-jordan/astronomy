// Package skypano places wide-field frames on the celestial sphere and renders them onto one canvas.
//
// It exists because the existing mosaic cannot: internal/mosaic projects onto a gnomonic (TAN)
// tangent plane, which is undefined at 90 degrees from its centre and grotesque well before that,
// and it assembles a single mono plane. A Milky Way arch spans about 180 degrees in colour. Siril's
// plate solver is no help either at a 72-degree field.
//
// What makes it tractable is that a phone records where it was pointed. That prior is good to about
// a degree, which turns blind plate solving into refinement.
package skypano

import "math"

// Camera maps directions on the sky to pixels in one frame.
//
// The model is a pinhole with THREE radial terms. A pinhole is exact for a rectilinear lens at any
// field width — there is no small-angle assumption here — but "already lens corrected by Apple" turned
// out not to mean rectilinear. Measured against the catalogue on this session's panels, a pure pinhole
// leaves a residual that is almost purely radial and reaches 18 px near mid-field before turning over
// again by the corners. One term cannot make that shape; the displacement it produces is F·(K1·r³ +
// K2·r⁵ + K3·r⁷), and it takes the second term's opposite sign to rise and fall.
//
// That residual was the whole story behind the trailed stars in the mosaic. Each panel matched its own
// stars well enough near the axis, so each looked solved, but two panels viewing the same star at
// different FIELD POSITIONS placed it up to 13 px apart — and averaging those in the blend drew a dash.
type Camera struct {
	// R takes a celestial unit vector into camera coordinates: its ROWS are the camera's axes
	// expressed in the equatorial frame, so cam = R · v. The axes are image +x (right), image +y
	// (down, since pixel rows run downward) and +z (the optical axis).
	R [3][3]float64
	// F is the focal length in pixels.
	F float64
	// K1, K2 and K3 are the radial distortion coefficients, applied as
	// (1 + K1·r² + K2·r⁴ + K3·r⁶) with r in normalized units.
	K1, K2, K3 float64
	// RadialCorr is an EMPIRICAL correction to that factor, sampled uniformly in normalized radius
	// over [0, RadialCorrMaxR] and interpolated linearly; beyond the last sample it holds. Zero
	// length means no correction.
	//
	// It exists because the polynomial cannot describe this lens at its corners. Measured after the
	// three-term fit, the residual stayed under 1.6 px out to r≈1600 px and then ran to +4.7 px at
	// 1700 and +12 px past 1800 — the corners, since 1600 is already beyond the frame's own top and
	// bottom edges. That is not a coefficient that needs tuning: r/F reaches 0.9 there, where r⁷
	// swings so hard that a fit anchored by the dense inner field cannot also follow the edge. A
	// table has no such shape to impose, and it is still ONE curve shared by every panel, fitted from
	// thousands of stars, so it stays far better constrained than the thing it corrects.
	RadialCorr     []float64
	RadialCorrMaxR float64
	// Cx, Cy are the principal point in pixels.
	Cx, Cy float64
}

// Project maps a celestial unit vector to pixel coordinates. ok is false when the direction lies
// behind the camera.
func (c Camera) Project(v [3]float64) (x, y float64, ok bool) {
	cam := [3]float64{
		c.R[0][0]*v[0] + c.R[0][1]*v[1] + c.R[0][2]*v[2],
		c.R[1][0]*v[0] + c.R[1][1]*v[1] + c.R[1][2]*v[2],
		c.R[2][0]*v[0] + c.R[2][1]*v[1] + c.R[2][2]*v[2],
	}
	if cam[2] <= 1e-9 {
		return 0, 0, false
	}
	xn, yn := cam[0]/cam[2], cam[1]/cam[2]
	d := c.radial(xn*xn + yn*yn)
	return c.Cx + c.F*xn*d, c.Cy + c.F*yn*d, true
}

// Unproject maps pixel coordinates back to a celestial unit vector. The radial term is inverted by
// a short fixed-point iteration, which converges quickly for the small distortions in play.
func (c Camera) Unproject(x, y float64) [3]float64 {
	dx, dy := (x-c.Cx)/c.F, (y-c.Cy)/c.F
	xn, yn := dx, dy
	if c.K1 != 0 || c.K2 != 0 || c.K3 != 0 {
		for i := 0; i < 12; i++ {
			d := c.radial(xn*xn + yn*yn)
			if d == 0 {
				break
			}
			xn, yn = dx/d, dy/d
		}
	}
	cam := normalize3([3]float64{xn, yn, 1})
	// R is orthonormal, so its transpose takes camera coordinates back to the sky.
	return [3]float64{
		c.R[0][0]*cam[0] + c.R[1][0]*cam[1] + c.R[2][0]*cam[2],
		c.R[0][1]*cam[0] + c.R[1][1]*cam[1] + c.R[2][1]*cam[2],
		c.R[0][2]*cam[0] + c.R[1][2]*cam[1] + c.R[2][2]*cam[2],
	}
}

// radial is the distortion factor at squared normalized radius r2.
func (c Camera) radial(r2 float64) float64 {
	d := 1 + r2*(c.K1+r2*(c.K2+r2*c.K3))
	if len(c.RadialCorr) == 0 || c.RadialCorrMaxR <= 0 {
		return d
	}
	return d + c.corrAt(math.Sqrt(r2))
}

// corrAt interpolates the empirical table at normalized radius r.
func (c Camera) corrAt(r float64) float64 {
	n := len(c.RadialCorr)
	if n == 1 {
		return c.RadialCorr[0]
	}
	t := r / c.RadialCorrMaxR * float64(n-1)
	if t <= 0 {
		return c.RadialCorr[0]
	}
	if t >= float64(n-1) {
		return c.RadialCorr[n-1]
	}
	i := int(t)
	f := t - float64(i)
	return c.RadialCorr[i]*(1-f) + c.RadialCorr[i+1]*f
}

// Axis is the direction the camera points.
func (c Camera) Axis() [3]float64 { return [3]float64{c.R[2][0], c.R[2][1], c.R[2][2]} }

// FocalForHalfFOV returns the focal length in pixels that gives halfFOVDeg across halfExtentPx.
func FocalForHalfFOV(halfExtentPx, halfFOVDeg float64) float64 {
	return halfExtentPx / math.Tan(halfFOVDeg*math.Pi/180)
}

// SetRotation builds R from the camera's axes expressed in the equatorial frame.
func SetRotation(right, down, axis [3]float64) [3][3]float64 {
	return [3][3]float64{
		{right[0], right[1], right[2]},
		{down[0], down[1], down[2]},
		{axis[0], axis[1], axis[2]},
	}
}

// Rotate returns R turned by small angles about the camera's own axes: yaw about +y (down), pitch
// about +x (right) and roll about +z (the optical axis), in radians. This is the parameterisation
// the solver walks, because near the prior the three are almost independent.
func Rotate(r [3][3]float64, pitch, yaw, roll float64) [3][3]float64 {
	out := mul3(rotX(pitch), r)
	out = mul3(rotY(yaw), out)
	return mul3(rotZ(roll), out)
}

func rotX(a float64) [3][3]float64 {
	s, c := math.Sin(a), math.Cos(a)
	return [3][3]float64{{1, 0, 0}, {0, c, -s}, {0, s, c}}
}

func rotY(a float64) [3][3]float64 {
	s, c := math.Sin(a), math.Cos(a)
	return [3][3]float64{{c, 0, s}, {0, 1, 0}, {-s, 0, c}}
}

func rotZ(a float64) [3][3]float64 {
	s, c := math.Sin(a), math.Cos(a)
	return [3][3]float64{{c, -s, 0}, {s, c, 0}, {0, 0, 1}}
}

func mul3(a, b [3][3]float64) [3][3]float64 {
	var out [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			out[i][j] = a[i][0]*b[0][j] + a[i][1]*b[1][j] + a[i][2]*b[2][j]
		}
	}
	return out
}

// RADecToVec converts equatorial degrees to a unit vector (x toward RA 0 on the equator, z to the
// north celestial pole).
func RADecToVec(raDeg, decDeg float64) [3]float64 {
	ra, dec := raDeg*math.Pi/180, decDeg*math.Pi/180
	return [3]float64{math.Cos(dec) * math.Cos(ra), math.Cos(dec) * math.Sin(ra), math.Sin(dec)}
}

// VecToRADec is the inverse of RADecToVec, with RA normalized to [0,360).
func VecToRADec(v [3]float64) (raDeg, decDeg float64) {
	raDeg = math.Mod(math.Atan2(v[1], v[0])*180/math.Pi+360, 360)
	return raDeg, math.Asin(clamp1(v[2]/math.Sqrt(dot3(v, v)))) * 180 / math.Pi
}

func cross3(a, b [3]float64) [3]float64 {
	return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

func dot3(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func normalize3(v [3]float64) [3]float64 {
	n := math.Sqrt(dot3(v, v))
	if n == 0 {
		return v
	}
	return [3]float64{v[0] / n, v[1] / n, v[2] / n}
}

func neg3(v [3]float64) [3]float64 { return [3]float64{-v[0], -v[1], -v[2]} }

func clamp1(v float64) float64 { return math.Max(-1, math.Min(1, v)) }
