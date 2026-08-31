package solarsystem

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// TestSpecFromStandish_MatchesAstro is the pin that lets this package carry its own per-day
// propagator: converting the per-century table into it must be a rewrite, not an approximation.
func TestSpecFromStandish_MatchesAstro(t *testing.T) {
	for _, b := range astro.Bodies {
		t.Run(b.String(), func(t *testing.T) {
			spec := specFromStandish(b)
			for year := 1800; year <= 2050; year += 25 {
				when := time.Date(year, 6, 1, 0, 0, 0, 0, time.UTC)
				wx, wy, wz := astro.HelioEclipticJ2000(b, when)
				gx, gy, gz := spec.PositionAt(astro.JulianDate(when))
				assert.InDelta(t, wx, gx, 1e-9, "x in %d", year)
				assert.InDelta(t, wy, gy, 1e-9, "y in %d", year)
				assert.InDelta(t, wz, gz, 1e-9, "z in %d", year)
			}
		})
	}
}

func TestAll_RegistryIsCoherent(t *testing.T) {
	bodies := All()
	require.NotEmpty(t, bodies)

	seen := map[string]bool{}
	for _, b := range bodies {
		assert.False(t, seen[b.Key], "duplicate key %q", b.Key)
		seen[b.Key] = true

		assert.NotEmpty(t, b.Colour, "%s has a colour", b.Key)
		assert.Greater(t, b.RadiusKm, 0.0, "%s has a radius", b.Key)
		assert.NotEmpty(t, b.Source, "%s cites a source", b.Key)
		assert.NotZero(t, b.Pole.WDot, "%s rotates", b.Key)

		if b.Parent != "" {
			_, ok := Find(b.Parent)
			assert.Truef(t, ok, "%s names a parent that exists", b.Key)
		}
		if b.Kind != KindStar {
			assert.Truef(t, b.Orbit != nil || b.Series != "", "%s knows how to move", b.Key)
		}
	}
	assert.Equal(t, "sun", bodies[0].Key, "the Sun leads the list")
	// Moons follow their planet directly, which is what makes the picker read as systems.
	for i, b := range bodies {
		if b.Kind == KindMoon {
			require.Greater(t, i, 0)
			assert.Contains(t, []string{b.Parent, "moon"}, bodies[i-1].Key,
				"%s follows its parent or another of its moons", b.Key)
		}
	}
}

// TestAxialTilt derives each world's axial tilt from its IAU pole and its own orbit plane, and holds
// the answer against the published figure. It is the check that the rotation model is not merely
// self-consistent but actually points where these worlds point — the retrograde rotators (Venus,
// Uranus, Pluto) come out past 90° with no special-casing anywhere.
func TestAxialTilt(t *testing.T) {
	want := map[string]float64{
		"mercury": 0.034, "venus": 177.36, "earth": 23.44, "mars": 25.19,
		"jupiter": 3.13, "saturn": 26.73, "uranus": 97.77, "neptune": 28.32,
	}
	jd := astro.JulianDate(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for key, tilt := range want {
		b, ok := Find(key)
		require.True(t, ok, key)
		assert.InDeltaf(t, tilt, AxialTiltDeg(b, jd), 0.3, "%s axial tilt", key)
	}

	// Pluto is deliberately not in that table. The 122.5° usually quoted for it predates the
	// post-New-Horizons pole the IAU now publishes; from that pole and Standish's orbit the obliquity
	// comes out near 119.6°, so this pins what the cited inputs actually imply and asserts the fact
	// that matters either way — Pluto is tipped past a right angle, and turns backwards along its orbit.
	pluto, ok := Find("pluto")
	require.True(t, ok)
	assert.InDelta(t, 119.6, AxialTiltDeg(pluto, jd), 0.3, "pluto obliquity from the IAU 2015 pole")
	assert.Greater(t, AxialTiltDeg(pluto, jd), 90.0)
}

func TestOrientation_BasisIsOrthonormal(t *testing.T) {
	jd := astro.JulianDate(time.Date(2026, 8, 13, 3, 21, 0, 0, time.UTC))
	for _, b := range All() {
		o := b.Pole.OrientationAt(jd)
		x, y, z := o.Basis()
		assert.InDeltaf(t, 1, norm(x), 1e-12, "%s x is a unit vector", b.Key)
		assert.InDeltaf(t, 1, norm(y), 1e-12, "%s y is a unit vector", b.Key)
		assert.InDeltaf(t, 1, norm(z), 1e-12, "%s z is a unit vector", b.Key)
		assert.InDeltaf(t, 0, dot(x, y), 1e-12, "%s x⊥y", b.Key)
		assert.InDeltaf(t, 0, dot(y, z), 1e-12, "%s y⊥z", b.Key)
		assert.InDeltaf(t, 0, dot(z, x), 1e-12, "%s z⊥x", b.Key)
		// Right-handed: x × y must be z, not −z, or every texture is drawn mirrored.
		assert.InDeltaf(t, 1, dot(cross(x, y), z), 1e-12, "%s basis is right-handed", b.Key)
		assert.InDeltaf(t, 0, norm(subtract(z, o.Axis())), 1e-12, "%s Axis is the basis z", b.Key)
	}
}

// TestOrientation_EarthPrimeMeridianTracksSiderealTime checks the prime-meridian angle against the
// one body whose rotation we can verify independently: Earth's W must turn with sidereal time, so
// the difference between the two stays fixed as the day goes on.
func TestOrientation_EarthPrimeMeridianTracksSiderealTime(t *testing.T) {
	earth, ok := Find("earth")
	require.True(t, ok)

	base := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	var first float64
	for h := 0; h < 24; h += 3 {
		when := base.Add(time.Duration(h) * time.Hour)
		w := earth.Pole.OrientationAt(astro.JulianDate(when)).WDeg
		diff := math.Mod(math.Mod(w-astro.GMST(when), 360)+360, 360)
		if h == 0 {
			first = diff
			continue
		}
		assert.InDeltaf(t, first, diff, 0.05, "Earth's prime meridian keeps pace with sidereal time at +%dh", h)
	}
}

func TestStateAt_Geometry(t *testing.T) {
	site := Site{LatDeg: 48.85, LonDeg: 2.35}
	when := time.Date(2026, 8, 13, 22, 0, 0, 0, time.UTC)
	snap := StateAt(when, site)

	states := map[string]BodyState{}
	for _, s := range snap.Bodies {
		states[s.Key] = s
	}

	assert.Equal(t, 0.0, norm(states["sun"].Helio), "the Sun is the origin of the scene")
	assert.InDelta(t, 1.0, states["earth"].HelioDistAU, 0.02, "Earth is one astronomical unit out")
	assert.InDelta(t, 0, states["earth"].GeoDistAU, 1e-12, "Earth is at zero distance from itself")

	// The Moon: distance in kilometres, and a local vector that is genuinely the geocentric one.
	moonKm := norm(*states["moon"].Local) * kmPerAU
	assert.Greater(t, moonKm, 356000.0, "the Moon stays outside perigee")
	assert.Less(t, moonKm, 407000.0, "the Moon stays inside apogee")
	assert.InDelta(t, moonKm, states["moon"].GeoDistAU*kmPerAU, 1.0)

	// The Sun's altitude must match what the planner computes for the same instant and place.
	assert.InDelta(t, astro.SunAltitude(when, site.LatDeg, site.LonDeg), states["sun"].AltDeg, 0.05)

	// Apparent diameters, in arcseconds, against the figures an observer knows by heart.
	assert.InDelta(t, 1920, states["sun"].AngularDiamArcsec, 40, "the Sun is about half a degree across")
	assert.InDelta(t, 1870, states["moon"].AngularDiamArcsec, 130, "so is the Moon")
	assert.Greater(t, states["jupiter"].AngularDiamArcsec, 29.0)
	assert.Less(t, states["jupiter"].AngularDiamArcsec, 51.0)
}

// TestStateAt_MarsOpposition2025 checks the geometry against a date anyone can look up: Mars came to
// opposition on 2025-01-16, 0.642 AU away.
//
// Opposition is a lining-up in longitude, not in space. Mars's orbit is inclined, and at 0.64 AU
// that 1.85° of heliocentric tilt is magnified into more than 4° of geocentric latitude, so the true
// elongation falls a few degrees short of a straight line — which is exactly why Mars oppositions
// are not eclipses of Mars.
func TestStateAt_MarsOpposition2025(t *testing.T) {
	when := time.Date(2025, 1, 16, 2, 0, 0, 0, time.UTC)
	for _, s := range StateAt(when, Site{}).Bodies {
		if s.Key != "mars" {
			continue
		}
		assert.Greater(t, s.ElongationDeg, 175.0, "Mars is opposite the Sun at opposition")
		assert.Less(t, s.PhaseAngleDeg, 3.0, "and so shows an all-but-full disc")
		assert.Greater(t, s.IllumFraction, 0.999)
		assert.InDelta(t, 0.642, s.GeoDistAU, 0.01, "at its 2025 opposition distance")
		assert.Less(t, s.Magnitude, -1.3, "and near its brightest")
		return
	}
	t.Fatal("mars is missing from the snapshot")
}

// TestStateAt_SaturnRingPlaneCrossing2025 pins the ring geometry to the crossing of 2025-03-23,
// when Saturn's rings turned edge-on to Earth: the opening angle passes through zero there and is
// wide open a decade away from it.
func TestStateAt_SaturnRingPlaneCrossing2025(t *testing.T) {
	at := func(when time.Time) float64 {
		for _, s := range StateAt(when, Site{}).Bodies {
			if s.Key == "saturn" {
				return s.RingOpenDeg
			}
		}
		t.Fatal("saturn is missing")
		return 0
	}

	crossing := at(time.Date(2025, 3, 23, 12, 0, 0, 0, time.UTC))
	assert.InDelta(t, 0, crossing, 0.5, "the rings are edge-on at the 2025 crossing")

	before := at(time.Date(2020, 3, 23, 12, 0, 0, 0, time.UTC))
	after := at(time.Date(2032, 3, 23, 12, 0, 0, 0, time.UTC))
	assert.Greater(t, math.Abs(before), 15.0, "and wide open five years earlier")
	assert.Greater(t, math.Abs(after), 15.0, "and again on the far side")
	assert.Negative(t, before*after, "with opposite faces presented either side of the crossing")
}

func TestSpec_LaplaceFrameRotatesIntoTheEcliptic(t *testing.T) {
	// A circular orbit in a plane whose pole IS the ecliptic pole must come out identical to one
	// declared in the ecliptic frame directly.
	eclipticPoleRA, eclipticPoleDec := 270.0, 90-23.43928
	circular := Spec{EpochJD: astro.J2000, A: 0.01, N: 1, PeriodDays: 360}

	inEcliptic := circular
	inEcliptic.Frame = FrameEcliptic
	inLaplace := circular
	inLaplace.Frame = FrameLaplace
	inLaplace.PoleRA, inLaplace.PoleDec = eclipticPoleRA, eclipticPoleDec

	for _, day := range []float64{0, 30, 111, 250} {
		ax, ay, az := inEcliptic.PositionAt(astro.J2000 + day)
		bx, by, bz := inLaplace.PositionAt(astro.J2000 + day)
		assert.InDelta(t, ax, bx, 1e-9, "day %v", day)
		assert.InDelta(t, ay, by, 1e-9, "day %v", day)
		assert.InDelta(t, az, bz, 1e-9, "day %v", day)
	}
}

func subtract(a, b [3]float64) [3]float64 {
	return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]}
}

// TestMoonMagnitude checks the phase law against the three brightnesses anyone can look up: full
// Moon, half Moon, and a thin crescent.
func TestMoonMagnitude(t *testing.T) {
	moon, ok := Find("moon")
	require.True(t, ok)

	for _, tc := range []struct{ phase, want, tol float64 }{
		{0, -12.73, 0.01},  // full
		{90, -10.13, 0.15}, // first or last quarter
		{170, -4.97, 0.3},  // a thin crescent, a day or so from new
	} {
		got := apparentMagnitude(moon, time.Now(), BodyState{PhaseAngleDeg: tc.phase})
		assert.InDeltaf(t, tc.want, got, tc.tol, "phase %.0f°", tc.phase)
	}
}
