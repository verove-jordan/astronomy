package polaralign

import "math"

// The ground-fixed frames the camera measurement works in, and the single place their conventions are
// written down.
//
// A mount is bolted to the ground, so its polar axis is fixed while the sky turns past it. That is the
// whole reason the measurement works: rotating the telescope about the RA axis sweeps the optical axis
// around a circle centred on that axis — but it is only a circle when the points are expressed
// somewhere the ground does not move. Right ascension is not such a place. Hour angle is.
//
// Two frames, both ground-fixed, related by one rotation about the east axis:
//
//	hourAngle  +x to where the celestial equator crosses the meridian, +y east, +z to the north
//	           celestial pole. Hour angle is west-positive, as in astro.HourAngleDeg.
//	horizon    +n north, +e east, +u up. Azimuth is measured from north, increasing eastward, as in
//	           astro.Horizontal.
//
// haVec(h, d).horizon(lat).altAz() reproduces astro.Horizontal exactly, and frames_test.go pins it
// against that function. These conventions are not allowed to drift from the rest of internal/astro.

const (
	deg2rad = math.Pi / 180
	rad2deg = 180 / math.Pi
)

// haVec3 is a unit direction in the hour-angle frame.
type haVec3 struct{ X, Y, Z float64 }

// hVec3 is a unit direction in the horizon frame.
//
// The fields are named for the compass, but the CARTESIAN order is (E, N, U) — east, north, up — and
// every cross product and rotation below uses that order. This is not cosmetic. (N, E, U) is a
// LEFT-handed triple, and building cross products on it would make horizon() a reflection: a rotation
// carried from the hour-angle frame into this one would silently come out backwards, which is a sign
// error that produces a perfectly plausible wrong answer. With (E, N, U) the frame is right-handed,
// horizon() is a proper rotation, and rotations mean the same thing on both sides of it —
// TestFrames_RotationsSurviveTheFrameChange holds that line.
type hVec3 struct{ N, E, U float64 }

// haVec builds the hour-angle unit vector for a west-positive hour angle and a declination.
func haVec(haDeg, decDeg float64) haVec3 {
	sinH, cosH := math.Sincos(haDeg * deg2rad)
	sinD, cosD := math.Sincos(decDeg * deg2rad)
	// −sinH on the east axis is what makes hour angle run WESTWARD: at H = +6h the direction must be
	// on the west side, i.e. at negative y.
	return haVec3{X: cosD * cosH, Y: -cosD * sinH, Z: sinD}
}

// haDec reads a hour-angle direction back as angles. haDeg comes back in (-180,180], matching
// astro.HourAngleDeg.
func (v haVec3) haDec() (haDeg, decDeg float64) {
	return norm180(math.Atan2(-v.Y, v.X) * rad2deg), math.Asin(clamp1(v.Z)) * rad2deg
}

// horizon rotates a hour-angle direction into the horizon frame. Both frames are bolted to the ground,
// so this is a plain rotation about the shared east axis by (90° − latitude) — no time, no sidereal
// angle, nothing that can go stale between two frames of a measurement.
func (v haVec3) horizon(latDeg float64) hVec3 {
	sinP, cosP := math.Sincos(latDeg * deg2rad)
	return hVec3{
		N: -v.X*sinP + v.Z*cosP,
		E: v.Y,
		U: v.X*cosP + v.Z*sinP,
	}
}

// hourAngle is the inverse rotation.
func (h hVec3) hourAngle(latDeg float64) haVec3 {
	sinP, cosP := math.Sincos(latDeg * deg2rad)
	return haVec3{
		X: -h.N*sinP + h.U*cosP,
		Y: h.E,
		Z: h.N*cosP + h.U*sinP,
	}
}

// horizonVec builds a horizon unit vector from altitude and azimuth (azimuth from north, eastward).
func horizonVec(altDeg, azDeg float64) hVec3 {
	sinA, cosA := math.Sincos(altDeg * deg2rad)
	sinZ, cosZ := math.Sincos(azDeg * deg2rad)
	return hVec3{N: cosA * cosZ, E: cosA * sinZ, U: sinA}
}

// altAz reads a horizon direction back as altitude and azimuth in [0,360).
func (h hVec3) altAz() (altDeg, azDeg float64) {
	return math.Asin(clamp1(h.U)) * rad2deg, norm360(math.Atan2(h.E, h.N) * rad2deg)
}

// poleHorizon is where a perfectly aligned polar axis points: due north at an altitude equal to the
// latitude, or due south of it below the equator. It is the target of the whole exercise.
func poleHorizon(latDeg float64) hVec3 {
	sinP, cosP := math.Sincos(latDeg * deg2rad)
	// Straight from haVec3{Z: sign}.horizon(lat): the celestial pole is ±ẑ in the hour-angle frame.
	if latDeg < 0 {
		return hVec3{N: -cosP, E: 0, U: -sinP}
	}
	return hVec3{N: cosP, E: 0, U: sinP}
}

// --- small vector helpers, kept local so the conventions above stay self-contained ---

func (v haVec3) dot(o haVec3) float64 { return v.X*o.X + v.Y*o.Y + v.Z*o.Z }

func (v haVec3) cross(o haVec3) haVec3 {
	return haVec3{
		X: v.Y*o.Z - v.Z*o.Y,
		Y: v.Z*o.X - v.X*o.Z,
		Z: v.X*o.Y - v.Y*o.X,
	}
}

func (v haVec3) minus(o haVec3) haVec3 {
	return haVec3{X: v.X - o.X, Y: v.Y - o.Y, Z: v.Z - o.Z}
}

func (v haVec3) norm() float64 { return math.Sqrt(v.dot(v)) }

// unit normalizes, returning ok=false for a vector too short to have a direction.
func (v haVec3) unit() (haVec3, bool) {
	n := v.norm()
	if n < 1e-12 {
		return haVec3{}, false
	}
	return haVec3{X: v.X / n, Y: v.Y / n, Z: v.Z / n}, true
}

func (v haVec3) scaled(k float64) haVec3 {
	return haVec3{X: v.X * k, Y: v.Y * k, Z: v.Z * k}
}

func (h hVec3) dot(o hVec3) float64 { return h.N*o.N + h.E*o.E + h.U*o.U }

// cross is the right-handed cross product in the (E, N, U) Cartesian order — see the note on hVec3.
func (h hVec3) cross(o hVec3) hVec3 {
	return hVec3{
		E: h.N*o.U - h.U*o.N,
		N: h.U*o.E - h.E*o.U,
		U: h.E*o.N - h.N*o.E,
	}
}

func (h hVec3) unit() (hVec3, bool) {
	n := math.Sqrt(h.dot(h))
	if n < 1e-12 {
		return hVec3{}, false
	}
	return hVec3{N: h.N / n, E: h.E / n, U: h.U / n}, true
}

// angleBetween is the great-circle angle in degrees between two unit horizon directions. The atan2
// form is used rather than acos because acos loses all its precision at exactly the small angles this
// feature exists to measure.
func angleBetween(a, b hVec3) float64 {
	c := a.cross(b)
	return math.Atan2(math.Sqrt(c.dot(c)), a.dot(b)) * rad2deg
}

// rotateZenith turns a horizon direction about the vertical by degDeg, in the direction of increasing
// azimuth. This is what the mount's AZIMUTH adjuster does to the whole telescope.
func rotateZenith(h hVec3, degDeg float64) hVec3 {
	s, c := math.Sincos(degDeg * deg2rad)
	return hVec3{N: h.N*c - h.E*s, E: h.N*s + h.E*c, U: h.U}
}

// rotateEast turns a horizon direction about the east axis by degDeg, raising the altitude of things
// in the north. This is what the mount's ALTITUDE adjuster does to the whole telescope.
func rotateEast(h hVec3, degDeg float64) hVec3 {
	s, c := math.Sincos(degDeg * deg2rad)
	return hVec3{N: h.N*c - h.U*s, E: h.E, U: h.N*s + h.U*c}
}

// rodrigues rotates (v) about the UNIT axis (a) by degDeg. The two frame types deliberately do not
// convert into one another — mixing them up is the bug this whole file exists to prevent — so the
// rotation itself is written once here and wrapped for each.
func rodrigues(vx, vy, vz, ax, ay, az, degDeg float64) (float64, float64, float64) {
	s, c := math.Sincos(degDeg * deg2rad)
	cx := ay*vz - az*vy
	cy := az*vx - ax*vz
	cz := ax*vy - ay*vx
	d := ax*vx + ay*vy + az*vz
	return vx*c + cx*s + ax*d*(1-c),
		vy*c + cy*s + ay*d*(1-c),
		vz*c + cz*s + az*d*(1-c)
}

// rotateAbout spins a hour-angle direction around a unit axis. Rotating about +z is exactly sidereal
// motion, which is what makes it the natural place to express "the sky has turned since that frame".
func rotateAbout(v, axis haVec3, degDeg float64) haVec3 {
	x, y, z := rodrigues(v.X, v.Y, v.Z, axis.X, axis.Y, axis.Z, degDeg)
	return haVec3{X: x, Y: y, Z: z}
}

// rotateAboutH spins a horizon direction around a unit axis, right-hand rule, in the (E, N, U)
// Cartesian order. Used to undo the tracking rotation about the mount's own polar axis, which is where
// the fit lives.
//
// Note that the right-hand rule about Up turns east toward north, so it DECREASES compass azimuth —
// compass azimuth runs the other way round. rotateZenith below is the one that speaks in azimuth.
func rotateAboutH(v, axis hVec3, degDeg float64) hVec3 {
	e, n, u := rodrigues(v.E, v.N, v.U, axis.E, axis.N, axis.U, degDeg)
	return hVec3{N: n, E: e, U: u}
}

// norm180 wraps an angle into (-180,180]. astro keeps its own copy unexported, and polaralign.go
// already keeps a local norm360 for the same reason.
func norm180(d float64) float64 {
	d = norm360(d)
	if d > 180 {
		d -= 360
	}
	return d
}

// clamp1 guards asin/acos against arguments a hair outside [-1,1] from rounding.
func clamp1(x float64) float64 {
	if x > 1 {
		return 1
	}
	if x < -1 {
		return -1
	}
	return x
}
