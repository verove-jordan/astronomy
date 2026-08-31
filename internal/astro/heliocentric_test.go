package astro

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// geoFromHelio builds the geocentric position the Standish table implies, in the equinox OF DATE, so
// it can be compared with PlanetPosition: subtract the Earth-Moon barycentre, rotate into J2000
// equatorial, then precess to the date. Both models are geometric (no light-time, no aberration), so
// nothing else has to be matched up.
func geoFromHelio(b Body, t time.Time) (raDeg, decDeg, distAU float64) {
	px, py, pz := HelioEclipticJ2000(b, t)
	ex, ey, ez := HelioEclipticJ2000(BodyEarth, t)
	ra, dec, dist := EclipticJ2000ToEquatorial(px-ex, py-ey, pz-ez)
	ra, dec = PrecessFromJ2000(ra, dec, t)
	return ra, dec, dist
}

// TestHelioEclipticJ2000_AgreesWithPlanetPosition is the contract that lets two ephemerides live in
// one codebase: the 3-D map's heliocentric model and the planner's geocentric one must describe the
// same sky. Both are independently arcminute-class, so they are held to a few arcminutes of each
// other across the span where both are defended.
func TestHelioEclipticJ2000_AgreesWithPlanetPosition(t *testing.T) {
	epochs := []time.Time{}
	for year := 1950; year <= 2050; year += 5 {
		epochs = append(epochs, time.Date(year, 3, 7, 12, 0, 0, 0, time.UTC))
		epochs = append(epochs, time.Date(year, 9, 21, 0, 0, 0, 0, time.UTC))
	}

	// Per-planet tolerance in arcminutes, set just above what the two models actually differ by over
	// this grid so a regression in either shows up. The outer planets are the loose ones: Schlyter
	// carries only the largest hand-picked mutual perturbation terms while Standish absorbs them into
	// fitted rates, so the two drift apart most where those terms matter (Saturn, 10′) and are
	// essentially identical for the inner planets (0.2′).
	tol := map[Planet]float64{
		Mercury: 1, Venus: 1, Mars: 1, Jupiter: 9, Saturn: 13, Uranus: 4, Neptune: 2,
	}

	for _, p := range Planets {
		t.Run(p.String(), func(t *testing.T) {
			worst := 0.0
			for _, epoch := range epochs {
				want := PlanetPosition(p, epoch)
				ra, dec, dist := geoFromHelio(PlanetBody(p), epoch)

				sep := AngularSeparation(ra, dec, want.RADeg, want.DecDeg) * 60 // arcminutes
				if sep > worst {
					worst = sep
				}
				assert.LessOrEqualf(t, sep, tol[p],
					"%s at %s: %.1f′ apart (helio %.4f/%.4f vs planner %.4f/%.4f)",
					p, epoch.Format("2006-01-02"), sep, ra, dec, want.RADeg, want.DecDeg)
				assert.InDeltaf(t, want.GeoDistAU, dist, 0.01*math.Max(1, want.GeoDistAU),
					"%s at %s: geocentric distance disagrees", p, epoch.Format("2006-01-02"))
			}
			t.Logf("%s: worst separation %.1f′ over %d epochs", p, worst, len(epochs))
		})
	}
}

// TestHelioEclipticJ2000_EarthOrbit checks the Earth row against facts that need no ephemeris:
// the orbit's size, and perihelion falling in the first days of January.
func TestHelioEclipticJ2000_EarthOrbit(t *testing.T) {
	minR, maxR := math.Inf(1), math.Inf(-1)
	var perihelion time.Time
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 366; i++ {
		day := start.AddDate(0, 0, i)
		x, y, z := HelioEclipticJ2000(BodyEarth, day)
		r := math.Sqrt(x*x + y*y + z*z)
		if r < minR {
			minR, perihelion = r, day
		}
		if r > maxR {
			maxR = r
		}
		assert.InDelta(t, 0, z, 0.001, "the Earth-Moon barycentre stays in the ecliptic by definition")
	}
	assert.InDelta(t, 0.9833, minR, 0.001, "perihelion distance")
	assert.InDelta(t, 1.0167, maxR, 0.001, "aphelion distance")
	assert.Equal(t, time.January, perihelion.Month(), "Earth reaches perihelion in early January")
	assert.LessOrEqual(t, perihelion.Day(), 6)
}

// TestElementsPosition_Geometry pins the Kepler solve and the orbital-plane rotation against the two
// points on an ellipse whose distance is known exactly from the elements alone.
func TestElementsPosition_Geometry(t *testing.T) {
	el := Elements{A: 2.5, E: 0.2, IDeg: 30, NodeDeg: 40, PeriDeg: 50}

	el.MDeg = 0
	x, y, z := ElementsPosition(el)
	assert.InDelta(t, el.A*(1-el.E), math.Sqrt(x*x+y*y+z*z), 1e-9, "M=0 puts the body at perihelion")

	el.MDeg = 180
	x, y, z = ElementsPosition(el)
	assert.InDelta(t, el.A*(1+el.E), math.Sqrt(x*x+y*y+z*z), 1e-9, "M=180 puts the body at aphelion")

	// A zero-inclination orbit must stay in the ecliptic plane whatever the node and perihelion are.
	el.IDeg, el.MDeg = 0, 123
	_, _, z = ElementsPosition(el)
	assert.InDelta(t, 0, z, 1e-12)
}

// TestEclipticJ2000ToEquatorial_Axes checks the frame rotation on the two directions whose answer is
// fixed by the definition of the frames: the equinox and the ecliptic pole.
func TestEclipticJ2000ToEquatorial_Axes(t *testing.T) {
	ra, dec, dist := EclipticJ2000ToEquatorial(1, 0, 0)
	assert.InDelta(t, 0, ra, 1e-9, "the +x axis IS the equinox, RA 0")
	assert.InDelta(t, 0, dec, 1e-9)
	assert.InDelta(t, 1, dist, 1e-12)

	// The ecliptic north pole sits 23.44° from the celestial pole, at RA 18h.
	ra, dec, _ = EclipticJ2000ToEquatorial(0, 0, 1)
	assert.InDelta(t, 270, ra, 1e-6)
	assert.InDelta(t, 66.56, dec, 0.01)
}

func TestOrbitElements_MeanAnomalyWrapped(t *testing.T) {
	for _, b := range Bodies {
		el := OrbitElements(b, time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC))
		require.Greater(t, el.A, 0.0, "%s has a semi-major axis", b)
		assert.Greater(t, el.MDeg, -180.0, "%s mean anomaly wrapped", b)
		assert.LessOrEqual(t, el.MDeg, 180.0, "%s mean anomaly wrapped", b)
		assert.GreaterOrEqual(t, el.NodeDeg, 0.0)
		assert.Less(t, el.NodeDeg, 360.0)
	}
}
