package polaralign

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// The whole camera measurement rests on these conventions, and a sign error in any of them produces a
// plausible-looking answer that sends the user turning the wrong bolt. So the frames are pinned
// against internal/astro rather than against themselves.

func TestHaVecHorizon_MatchesAstroHorizontal(t *testing.T) {
	when := time.Date(2026, 8, 4, 22, 17, 31, 0, time.UTC)
	sites := []struct {
		name     string
		lat, lon float64
	}{
		{"Paris", 48.8566, 2.3522},
		{"equator", 0.5, -60},
		{"far north", 68.2, 15.6},
		{"southern", -33.9, 18.4},
	}
	targets := []struct{ ra, dec float64 }{
		{0, 0}, {37.95, 89.26}, {279.23, 38.78}, {101.29, -16.72}, {201.3, -60.4}, {350, 5},
	}
	for _, s := range sites {
		for _, tg := range targets {
			wantAlt, wantAz := astro.Horizontal(tg.ra, tg.dec, s.lat, s.lon, when)
			ha := astro.HourAngleDeg(tg.ra, s.lon, when)
			gotAlt, gotAz := haVec(ha, tg.dec).horizon(s.lat).altAz()
			assert.InDelta(t, wantAlt, gotAlt, 1e-9, "%s alt for ra=%g dec=%g", s.name, tg.ra, tg.dec)
			// Azimuth is meaningless at the exact zenith and wraps at 360; compare the directions.
			assert.InDelta(t, 0,
				angleBetween(horizonVec(wantAlt, wantAz), horizonVec(gotAlt, gotAz)), 1e-9,
				"%s az for ra=%g dec=%g", s.name, tg.ra, tg.dec)
		}
	}
}

func TestFrames_RoundTrip(t *testing.T) {
	for _, c := range []struct{ ha, dec, lat float64 }{
		{0, 0, 45}, {123.4, -12.3, 48.8566}, {-95.5, 78.2, -33.9}, {179.9, 44, 10},
	} {
		v := haVec(c.ha, c.dec)
		ha, dec := v.haDec()
		assert.InDelta(t, norm180(c.ha), ha, 1e-9)
		assert.InDelta(t, c.dec, dec, 1e-9)

		back := v.horizon(c.lat).hourAngle(c.lat)
		assert.InDelta(t, 0, angleBetween(v.horizon(0), back.horizon(0)), 1e-9)

		alt, az := v.horizon(c.lat).altAz()
		assert.InDelta(t, 0, angleBetween(horizonVec(alt, az), v.horizon(c.lat)), 1e-9)
	}
}

// The celestial pole is the fixed point of the whole feature: it must sit due north (south) at an
// altitude equal to the latitude, in both hemispheres.
func TestPoleDirection_SitsAtLatitude(t *testing.T) {
	for _, lat := range []float64{48.8566, 5, 89, -33.9, -70} {
		alt, az := poleHorizon(lat).altAz()
		assert.InDelta(t, math.Abs(lat), alt, 1e-9, "lat %g", lat)
		if lat >= 0 {
			assert.InDelta(t, 0, norm180(az), 1e-9, "north pole is due north")
		} else {
			assert.InDelta(t, 180, az, 1e-9, "south pole is due south")
		}
	}
}

func TestRotateZenith_IncreasesAzimuth(t *testing.T) {
	h := horizonVec(30, 12)
	alt, az := rotateZenith(h, 7).altAz()
	assert.InDelta(t, 30, alt, 1e-9, "a turn about the vertical cannot change altitude")
	assert.InDelta(t, 19, az, 1e-9)
}

func TestRotateEast_RaisesTheNorth(t *testing.T) {
	// Tilting about the east axis lifts what is in the north and drops what is in the south — which is
	// exactly what happens to the sky when the altitude bolt raises the polar axis.
	north := horizonVec(20, 0)
	alt, _ := rotateEast(north, 5).altAz()
	assert.InDelta(t, 25, alt, 1e-9)

	south := horizonVec(20, 180)
	alt, _ = rotateEast(south, 5).altAz()
	assert.InDelta(t, 15, alt, 1e-9)
}

// The load-bearing test for the frame change. The horizon frame is stored (N, E, U) for readability
// but is Cartesian (E, N, U) so that it stays right-handed: rotating in the hour-angle frame and then
// converting must give the same answer as converting and then rotating. Get the field order wrong and
// horizon() becomes a reflection — every rotation carried across it comes out mirrored, and every
// individual function still looks correct.
func TestFrames_RotationsSurviveTheFrameChange(t *testing.T) {
	const lat = 48.8566
	axis, ok := haVec(40, 70).unit()
	require.True(t, ok)
	v := haVec(15, 25)

	for _, deg := range []float64{7, -33, 120} {
		viaHA := rotateAbout(v, axis, deg).horizon(lat)
		viaHorizon := rotateAboutH(v.horizon(lat), axis.horizon(lat), deg)
		assert.InDelta(t, 0, angleBetween(viaHA, viaHorizon), 1e-12,
			"rotating by %g° must not depend on which frame it is done in", deg)
	}
}

// The sky turns about the celestial pole. Pinning the SIGN here is what stops the adjust loop from
// de-rotating tracking the wrong way, which would look like the user turning a bolt.
func TestRotateAboutH_SiderealSignAtThePole(t *testing.T) {
	const lat = 48.8566
	ncp := haVec3{Z: 1}.horizon(lat)
	star := haVec(0, 20)

	// One hour of sidereal time advances the hour angle by 15°.
	want := haVec(15, 20).horizon(lat)
	got := rotateAboutH(star.horizon(lat), ncp, -15)
	assert.InDelta(t, 0, angleBetween(want, got), 1e-12,
		"advancing hour angle is a NEGATIVE right-hand rotation about the north celestial pole")
}

func TestRotateAbout_PoleIsSidereal(t *testing.T) {
	// Rotating about +z (the celestial pole) by θ must advance the hour angle by exactly θ and leave
	// declination alone: that is the sidereal motion the adjust phase has to subtract.
	v := haVec(20, 35)
	ha, dec := rotateAbout(v, haVec3{Z: 1}, 15).haDec()
	assert.InDelta(t, 35, dec, 1e-9)
	assert.InDelta(t, 5, ha, 1e-9, "rotating about +z runs the hour angle backwards (east)")

	// And the axis itself is untouched.
	axis, ok := haVec(77, 12).unit()
	require.True(t, ok)
	moved := rotateAbout(axis, axis, 42)
	assert.InDelta(t, 1, axis.dot(moved), 1e-12)
}
