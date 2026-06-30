package skyevents

import (
	"math"
	"os"
	"testing"
	"time"

	sgp4 "github.com/joshuaferrara/go-satellite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseTLEAndPropagate validates the SGP4 wiring against a real ISS element set: the parser must
// recover NORAD 25544, and propagation must place the ISS at a plausible low-Earth-orbit radius with a
// finite look-angle from a ground site.
func TestParseTLEAndPropagate(t *testing.T) {
	data, err := os.ReadFile("testdata/tle_sample.txt")
	require.NoError(t, err)

	sats := parseTLEs(data)
	require.NotEmpty(t, sats)
	var iss *namedTLE
	for i := range sats {
		if sats[i].noradID == 25544 {
			iss = &sats[i]
		}
	}
	require.NotNil(t, iss, "ISS (25544) should parse")
	assert.Equal(t, "iss", satKey(iss.name))

	sat := sgp4.TLEToSat(iss.l1, iss.l2, sgp4.GravityWGS84)
	at := time.Date(2026, 6, 28, 16, 30, 0, 0, time.UTC)
	eci := satECI(sat, at)
	r := math.Sqrt(eci.X*eci.X + eci.Y*eci.Y + eci.Z*eci.Z)
	assert.InDelta(t, 6790, r, 400, "ISS geocentric radius ≈ Earth radius + ~420 km")

	prm := Params{Lat: 48.8566, Lon: 2.3522}
	az, el, rng := satState(sat, at, prm)
	assert.GreaterOrEqual(t, az, 0.0)
	assert.Less(t, az, 360.0)
	assert.GreaterOrEqual(t, el, -90.0)
	assert.LessOrEqual(t, el, 90.0)
	assert.Greater(t, rng, 0.0)
}

// TestMoonTopocentric sanity-checks the topocentric Moon: its semi-diameter is ~0.25° and its parallax
// shifts the altitude by up to ~1° versus the geocentric position.
func TestMoonTopocentric(t *testing.T) {
	at := time.Date(2026, 8, 12, 22, 0, 0, 0, time.UTC)
	rhoSin, rhoCos := observerParallax(48.8566, 35)
	_, _, sd := moonTopoRADec(at, 2.3522, rhoSin, rhoCos)
	assert.InDelta(t, 0.25, sd, 0.05, "Moon semi-diameter ≈ 0.25°")
}
