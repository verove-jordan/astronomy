// Package pointing turns the metadata a phone writes into a sky pointing. Apple's gravity vector
// gives the camera's altitude above the horizon and its roll, the GPS compass gives the azimuth, and
// the site plus the capture instant turn that pair into a position on the celestial sphere.
//
// This exists because a hand-framed night has no pointing headers. Without it, frames shot at nine
// different tripod positions look identical to the engine and can only be grouped by guesswork; with
// it, the grouping is a measurement, and every panel arrives at the plate solver with a prior good
// to about a degree — which is the difference between refining a solution and searching for one.
package pointing

import (
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

const (
	deg2rad = math.Pi / 180
	rad2deg = 180 / math.Pi
)

// rollDeadZone is the in-plane fraction of gravity below which roll cannot be measured. 1e-6 puts
// the boundary within a thousandth of a degree of straight up, far tighter than any tripod.
const rollDeadZone = 1e-6

// Frame is where one exposure was aimed. Azimuth is the compass convention (N=0, increasing
// eastward) to match astro.Horizontal; altitude is the optical axis above the horizon, so a phone
// lying face-up on a table with its rear camera against the surface reports about -85.
type Frame struct {
	AzDeg   float64
	AltDeg  float64
	RollDeg float64

	// LatDeg/LonDeg and At are needed only to place the frame on the sky; grouping works without
	// them. HasSite/HasTime say whether Equatorial can answer.
	LatDeg  float64
	LonDeg  float64
	At      time.Time
	HasSite bool
	HasTime bool
}

// FromMeta derives the pointing from a frame's metadata. ok is false unless both the gravity vector
// and the compass bearing are present — a partial answer here would be worse than none, since a
// missing tilt silently reads as "aimed at the horizon".
func FromMeta(m rawmeta.Meta) (Frame, bool) {
	if !m.HasGravity || !m.HasCompass {
		return Frame{}, false
	}
	alt, roll, ok := AltRoll(m.Gravity)
	if !ok {
		return Frame{}, false
	}
	f := Frame{
		AzDeg:   m.CompassDeg,
		AltDeg:  alt,
		RollDeg: roll,
		LatDeg:  m.LatDeg,
		LonDeg:  m.LonDeg,
		HasSite: m.HasSite,
	}
	if m.TakenAtMs > 0 {
		f.At, f.HasTime = time.UnixMilli(m.TakenAtMs).UTC(), true
	}
	return f, true
}

// AltRoll decomposes Apple's AccelerationVector into the camera's altitude and roll.
//
// A stationary device's accelerometer reads proper acceleration, which points at the zenith, so the
// vector IS the local up direction expressed in device axes: +Z out of the rear camera, +Y toward
// the bottom of the phone, +X toward the left side as seen from the front. Altitude therefore falls
// straight out of the Z component, and roll is the angle in the image plane between the phone's top
// edge and up, positive toward +X.
//
// The sign of roll relative to the *displayed* image depends on how device axes map onto image axes
// once EXIF orientation is applied; that mapping is fixed against real stars when panels are solved.
// Grouping only needs roll to be consistent between frames, which it is either way.
func AltRoll(g [3]float64) (altDeg, rollDeg float64, ok bool) {
	n := math.Sqrt(g[0]*g[0] + g[1]*g[1] + g[2]*g[2])
	if n <= 0 {
		return 0, 0, false
	}
	altDeg = math.Asin(clamp1(g[2]/n)) * rad2deg

	// Aimed at the zenith (or the nadir) gravity has no component left in the image plane, so there
	// is no horizontal reference and roll is genuinely undefined rather than zero. Report 0 and say
	// so, because atan2 would otherwise hand back an arbitrary angle — 180 degrees, as it happens —
	// that looks like a real measurement and would split one panel in two.
	if math.Hypot(g[0], g[1]) < rollDeadZone*n {
		return altDeg, 0, true
	}
	return altDeg, math.Atan2(g[0], -g[1]) * rad2deg, true
}

// Axis is the unit vector along the optical axis in the local east/north/up frame.
func (f Frame) Axis() [3]float64 {
	az, alt := f.AzDeg*deg2rad, f.AltDeg*deg2rad
	return [3]float64{
		math.Sin(az) * math.Cos(alt),
		math.Cos(az) * math.Cos(alt),
		math.Sin(alt),
	}
}

// imageUp is the unit vector, in the same east/north/up frame, along the top of the image. At zero
// roll it lies in the vertical plane through the optical axis; roll rotates it about that axis.
func (f Frame) imageUp() [3]float64 {
	az, alt, roll := f.AzDeg*deg2rad, f.AltDeg*deg2rad, f.RollDeg*deg2rad
	up := [3]float64{
		-math.Sin(az) * math.Sin(alt),
		-math.Cos(az) * math.Sin(alt),
		math.Cos(alt),
	}
	right := cross(f.Axis(), up)
	return [3]float64{
		up[0]*math.Cos(roll) - right[0]*math.Sin(roll),
		up[1]*math.Cos(roll) - right[1]*math.Sin(roll),
		up[2]*math.Cos(roll) - right[2]*math.Sin(roll),
	}
}

// SeparationDeg is the true angular distance between two optical axes. It is deliberately the
// great-circle angle rather than a difference of azimuths: half of a Milky Way arch session sits
// near the zenith, where ten degrees of azimuth is barely two degrees of sky, and grouping on raw
// azimuth would split one panel into several.
func SeparationDeg(a, b Frame) float64 {
	av, bv := a.Axis(), b.Axis()
	return math.Acos(clamp1(av[0]*bv[0]+av[1]*bv[1]+av[2]*bv[2])) * rad2deg
}

// Equatorial returns the sky position the optical axis points at, plus the position angle of the
// image's up direction, measured east of north. ok is false without a site and a capture time.
//
// This is a prior, not a plate solve: a phone compass is good to a few degrees and the tilt to a
// fraction of one, so it seeds a solver rather than replacing it.
func (f Frame) Equatorial() (raDeg, decDeg, paDeg float64, ok bool) {
	if !f.HasSite || !f.HasTime {
		return 0, 0, 0, false
	}
	raDeg, decDeg = astro.Equatorial(f.AltDeg, f.AzDeg, f.LatDeg, f.LonDeg, f.At)

	axis := toEquatorial(f.Axis(), f.LatDeg, f.LonDeg, f.At)
	up := toEquatorial(f.imageUp(), f.LatDeg, f.LonDeg, f.At)

	// Position angle is measured in the sky frame at the axis: east is the direction of increasing
	// right ascension there, north the direction of increasing declination.
	east := normalize(cross([3]float64{0, 0, 1}, axis))
	north := cross(axis, east)
	return raDeg, decDeg, norm360(math.Atan2(dot(up, east), dot(up, north)) * rad2deg), true
}

// Basis returns the camera's axes as unit vectors in the equatorial frame: the optical axis, the
// image's right, and the image's up — all as the picture is DISPLAYED, after EXIF rotation.
// Together they are the rotation that takes a direction on the sky into the camera, which is what a
// wide-field solver needs as its starting guess. ok is false without a site and a capture time.
func (f Frame) Basis() (axis, right, up [3]float64, ok bool) {
	if !f.HasSite || !f.HasTime {
		return axis, right, up, false
	}
	axis = toEquatorial(f.Axis(), f.LatDeg, f.LonDeg, f.At)
	up = toEquatorial(f.imageUp(), f.LatDeg, f.LonDeg, f.At)
	return axis, cross(axis, up), up, true
}

// toEquatorial rotates a local east/north/up vector into the equatorial frame (x toward RA 0 on the
// equator, z toward the north celestial pole) for the given site and instant.
func toEquatorial(v [3]float64, latDeg, lonDeg float64, t time.Time) [3]float64 {
	lat := latDeg * deg2rad
	lst := astro.LST(t, lonDeg) * deg2rad

	east := [3]float64{-math.Sin(lst), math.Cos(lst), 0}
	north := [3]float64{-math.Sin(lat) * math.Cos(lst), -math.Sin(lat) * math.Sin(lst), math.Cos(lat)}
	up := [3]float64{math.Cos(lat) * math.Cos(lst), math.Cos(lat) * math.Sin(lst), math.Sin(lat)}

	out := [3]float64{}
	for i := range out {
		out[i] = v[0]*east[i] + v[1]*north[i] + v[2]*up[i]
	}
	return out
}

func cross(a, b [3]float64) [3]float64 {
	return [3]float64{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

func dot(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func normalize(v [3]float64) [3]float64 {
	n := math.Sqrt(dot(v, v))
	if n == 0 {
		return v
	}
	return [3]float64{v[0] / n, v[1] / n, v[2] / n}
}

func clamp1(v float64) float64 { return math.Max(-1, math.Min(1, v)) }

func norm360(d float64) float64 { return math.Mod(math.Mod(d, 360)+360, 360) }
