package scene3d

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

// vec3 is a direction in 3-space. Positions are only ever a direction times a distance, so the
// whole package needs no separate point type.
type vec3 struct{ X, Y, Z float64 }

func (a vec3) dot(b vec3) float64 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }

func (a vec3) sub(b vec3) vec3 { return vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }

func (a vec3) scale(k float64) vec3 { return vec3{a.X * k, a.Y * k, a.Z * k} }

func (a vec3) length() float64 { return math.Sqrt(a.dot(a)) }

// unit normalises a, or reports false for a degenerate (zero-length) vector.
func (a vec3) unit() (vec3, bool) { return a.unitAbove(0) }

// unitAbove normalises a only when it is meaningfully long.
//
// The axis construction below cannot test against exact zero. Its inputs are unit vectors, so the
// leftover of subtracting two nearly-parallel ones is ~1e-16 and never 0 — and on arm64 the fused
// multiply-add leaves a different residue again, which is exactly how a truly singular WCS matrix
// once slipped past a `det == 0` check in fits.ParseWCS. A threshold on the length is the honest
// test: for unit inputs that length IS the sine of the angle between them, so minLen is an angle.
func (a vec3) unitAbove(minLen float64) (vec3, bool) {
	n := a.length()
	if !(n > minLen) || math.IsNaN(n) || math.IsInf(n, 0) {
		return vec3{}, false
	}
	return a.scale(1 / n), true
}

// minAxisSine is the smallest angle (in radians, as a sine) two of the frame's anchors may subtend
// and still define an axis. 1e-9 rad is 0.2 milliarcsec; the narrowest real field is arcminutes
// across, so nothing legitimate is anywhere near it.
const minAxisSine = 1e-9

// perpendicularTo removes a's component along the unit vector u (Gram-Schmidt), keeping direction.
func (a vec3) perpendicularTo(u vec3) vec3 { return a.sub(u.scale(a.dot(u))) }

// skyToVec converts an equatorial position (degrees, ICRS/J2000 — the frame both the plate solution
// and the star catalogue use) to a unit vector. No precession anywhere in this package: everything
// it consumes is already in one frame.
func skyToVec(raDeg, decDeg float64) vec3 {
	const degRad = math.Pi / 180
	sinRA, cosRA := math.Sincos(raDeg * degRad)
	sinDec, cosDec := math.Sincos(decDeg * degRad)
	return vec3{cosDec * cosRA, cosDec * sinRA, sinDec}
}

// basis is the scene's coordinate frame: the observer sits at the origin, +Z points at the field
// centre, and +X/+Y run along the final image's own x/y axes. A pinhole camera at the origin looking
// down +Z with TanHalfW/TanHalfH therefore reproduces the photograph exactly — which is what lets the
// depth slider open from "the picture" into "the volume" without the stars sliding around.
//
// It is orthonormal by construction. That is exact rather than approximate because a TAN (gnomonic)
// solution IS a pinhole projection: straight lines through the origin, so the image plane's axes are
// genuinely perpendicular directions on the sky once each is taken perpendicular to the axis.
type basis struct {
	X, Y, Z            vec3
	TanHalfW, TanHalfH float64 // half-field tangents: the camera's aspect and zoom
	FovYDeg            float64 // vertical field of view, for display
	RightHanded        bool    // false when the sky parity makes X×Y point away from Z (mirrored field)
}

// newBasis builds the scene frame from the three sky positions annotate anchored the final image
// with. It fails only on a degenerate frame (a zero-size image or a collapsed solution), which the
// caller reports as "no 3D scene" rather than drawing something wrong.
func newBasis(f annotate.Frame) (basis, error) {
	z := skyToVec(f.CenterRA, f.CenterDec)
	ex := skyToVec(f.XEdgeRA, f.XEdgeDec)
	ey := skyToVec(f.YEdgeRA, f.YEdgeDec)

	x, ok := ex.perpendicularTo(z).unitAbove(minAxisSine)
	if !ok {
		return basis{}, fmt.Errorf("scene3d: degenerate image x axis at RA %.4f Dec %.4f", f.CenterRA, f.CenterDec)
	}
	// y is taken perpendicular to BOTH z and x. Squaring it up against x matters: a TAN field's two
	// image axes are perpendicular in the image, and any residue here would shear the reconstruction.
	y, ok := ey.perpendicularTo(z).perpendicularTo(x).unitAbove(minAxisSine)
	if !ok {
		return basis{}, fmt.Errorf("scene3d: degenerate image y axis at RA %.4f Dec %.4f", f.CenterRA, f.CenterDec)
	}

	b := basis{X: x, Y: y, Z: z}
	// tan of the half-field angle = (component across the axis) / (component along it), which is
	// exactly the gnomonic projection of the edge midpoint.
	b.TanHalfW = math.Abs(ex.dot(x) / ex.dot(z))
	b.TanHalfH = math.Abs(ey.dot(y) / ey.dot(z))
	if !(b.TanHalfW > 0) || !(b.TanHalfH > 0) {
		return basis{}, fmt.Errorf("scene3d: degenerate field of view (%.3e x %.3e)", b.TanHalfW, b.TanHalfH)
	}
	b.FovYDeg = 2 * math.Atan(b.TanHalfH) * 180 / math.Pi
	b.RightHanded = vec3{
		x.Y*y.Z - x.Z*y.Y,
		x.Z*y.X - x.X*y.Z,
		x.X*y.Y - x.Y*y.X,
	}.dot(z) > 0
	return b, nil
}

// project maps an equatorial position to a unit direction in scene coordinates. The result's Z is
// positive for anything in front of the observer, which is what the vertex shader divides by to lay
// a star on its depth plane.
func (b basis) project(raDeg, decDeg float64) vec3 {
	v := skyToVec(raDeg, decDeg)
	return vec3{v.dot(b.X), v.dot(b.Y), v.dot(b.Z)}
}
