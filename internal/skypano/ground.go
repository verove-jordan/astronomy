package skypano

// ground.go places something that is fixed to the EARTH on a canvas drawn for one instant.
//
// Every other panel here holds sky, and sky is why the panels can be solved at all: the stars turn
// with the equatorial frame, so a panel solved from its own stars is placed correctly on an
// equatorial canvas no matter when it was shot. The ground does the opposite. It does not move in
// azimuth and altitude at all, so in the equatorial frame it sweeps round the celestial pole at
// fifteen degrees an hour, and a foreground panel solved from the stars ABOVE it carries an
// orientation that is right for its stars and wrong for its horizon by exactly the time between when
// it was shot and the instant the arch is drawn for.
//
// The correction is therefore one rotation about the celestial pole, and nothing else. That it is
// only the pole matters: it means the foreground needs no separate projection path, no second canvas
// and no change to Render. Turn the camera, and the existing machinery puts the ground where it stood.

import "math"

// RotateAboutPole returns cam turned about the celestial pole by deltaLSTDeg.
//
// To place a foreground shot when the local sidereal time was lstPanel onto a canvas drawn for
// lstEpoch, pass lstEpoch - lstPanel. A direction fixed to the ground has a right ascension that
// tracks the sidereal time, so advancing the clock by an angle advances its RA by the same angle,
// and turning the camera by that angle carries its whole field with it.
func RotateAboutPole(cam Camera, deltaLSTDeg float64) Camera {
	rz := rotAboutPole(deltaLSTDeg * math.Pi / 180)
	out := cam
	// R's ROWS are the camera's axes in the equatorial frame, so each row is rotated on its own.
	for i := 0; i < 3; i++ {
		out.R[i] = mulVec3(rz, cam.R[i])
	}
	return out
}

// rotAboutPole is a rotation by theta about +z, which is the celestial pole in this frame — the
// same sense as increasing right ascension.
func rotAboutPole(theta float64) [3][3]float64 {
	c, s := math.Cos(theta), math.Sin(theta)
	return [3][3]float64{
		{c, -s, 0},
		{s, c, 0},
		{0, 0, 1},
	}
}

func mulVec3(m [3][3]float64, v [3]float64) [3]float64 {
	return [3]float64{
		m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2],
		m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2],
		m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2],
	}
}
