package skypano

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/pointing"
)

// The property that makes the whole approach work: a direction fixed to the GROUND, carried forward
// by the rotation, lands at the same azimuth and altitude when read at the later sidereal time. If
// this held only approximately the horizon would drift, and a horizon that is a degree out is
// obvious in a way a star a degree out is not.
func TestRotateAboutPole_KeepsTheGroundWhereItStood(t *testing.T) {
	const lat = 47.2767
	const lstPanel, lstEpoch = 190.0, 213.5 // about an hour and a half apart

	for _, g := range [][2]float64{{206.3, 16.2}, {180, 2}, {90, 30}, {0, 45}, {300, 5}} {
		// Where that fixed ground direction sat in the equatorial frame when the panel was shot.
		v := horizonToEquatorial(g[0], g[1], lat, lstPanel)
		// Carry it forward to the epoch and read it back on the epoch's horizon.
		rz := rotAboutPole((lstEpoch - lstPanel) * math.Pi / 180)
		az, alt := equatorialToHorizon(mulVec3(rz, v), lat, lstEpoch)
		if d := angleGap(az, g[0]); d > 1e-6 {
			t.Errorf("ground at az %.1f alt %.1f moved to az %.6f", g[0], g[1], az)
		}
		if math.Abs(alt-g[1]) > 1e-6 {
			t.Errorf("ground at az %.1f alt %.1f moved to alt %.6f", g[0], g[1], alt)
		}
	}
}

// And the same thing said about a camera: its optical axis must end up pointing at the same place on
// the ground, so the whole foreground lands where it was.
func TestRotateAboutPole_CarriesTheCameraWithIt(t *testing.T) {
	const lat = 47.2767
	const lstPanel, lstEpoch = 190.0, 213.5
	site := pointing.Frame{LatDeg: lat, AzDeg: 206.3, AltDeg: 16.2}

	cam := Camera{F: 2000, Cx: 2016, Cy: 1512}
	axis := horizonToEquatorial(site.AzDeg, site.AltDeg, lat, lstPanel)
	north := northAt(axis)
	cam.R = SetRotation(cross3(axis, north), north, axis)

	moved := RotateAboutPole(cam, lstEpoch-lstPanel)
	az, alt := equatorialToHorizon(moved.Axis(), lat, lstEpoch)
	if d := angleGap(az, site.AzDeg); d > 1e-6 || math.Abs(alt-site.AltDeg) > 1e-6 {
		t.Errorf("the camera's axis moved to az %.4f alt %.4f, want az %.1f alt %.1f",
			az, alt, site.AzDeg, site.AltDeg)
	}
	// The rotation must stay a rotation: the rows have to remain an orthonormal set, or the
	// foreground would be sheared onto the canvas.
	for i := 0; i < 3; i++ {
		if n := dot3(moved.R[i], moved.R[i]); math.Abs(n-1) > 1e-12 {
			t.Errorf("row %d is no longer a unit vector: %.15f", i, n)
		}
		for j := i + 1; j < 3; j++ {
			if d := dot3(moved.R[i], moved.R[j]); math.Abs(d) > 1e-12 {
				t.Errorf("rows %d and %d are no longer perpendicular: %.15f", i, j, d)
			}
		}
	}
}

// Turning by nothing must change nothing — the case a foreground panel shot AT the epoch hits.
func TestRotateAboutPole_ZeroIsIdentity(t *testing.T) {
	cam := Camera{F: 1234, Cx: 10, Cy: 20, R: SetRotation(
		[3]float64{1, 0, 0}, [3]float64{0, 1, 0}, [3]float64{0, 0, 1})}
	got := RotateAboutPole(cam, 0)
	if got.R != cam.R {
		t.Errorf("a zero rotation changed the camera: %v", got.R)
	}
}

// The SKY must move, and by the sidereal angle: this is the difference the correction exists to
// undo, so a rotation that left the stars alone would mean it was doing nothing.
func TestRotateAboutPole_MovesTheStarsByTheSiderealAngle(t *testing.T) {
	star := RADecToVec(266.405, -28.936)
	moved := mulVec3(rotAboutPole(23.5*math.Pi/180), star)
	ra, dec := VecToRADec(moved)
	if d := angleGap(ra, 266.405+23.5); d > 1e-9 {
		t.Errorf("right ascension went to %.6f, want %.3f", ra, 266.405+23.5)
	}
	if math.Abs(dec-(-28.936)) > 1e-9 {
		t.Errorf("declination changed to %.6f", dec)
	}
}
