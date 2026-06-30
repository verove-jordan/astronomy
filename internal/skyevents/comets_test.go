package skyevents

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseComets checks the fixed-column MPC parser against a real offline CometEls.txt sample, using
// C/1995 O1 (Hale-Bopp) as the anchor (q≈0.923 AU, e≈0.995, H=−2.0, n=4.0).
func TestParseComets(t *testing.T) {
	data, err := os.ReadFile("testdata/cometels_sample.txt")
	require.NoError(t, err)
	comets := parseComets(data)
	require.NotEmpty(t, comets)

	var hb *cometElem
	for i := range comets {
		if comets[i].Name == "C/1995 O1 (Hale-Bopp)" {
			hb = &comets[i]
			break
		}
	}
	require.NotNil(t, hb, "Hale-Bopp should parse from the sample")
	assert.InDelta(t, 0.922938, hb.Q, 1e-5)
	assert.InDelta(t, 0.994903, hb.E, 1e-5)
	assert.InDelta(t, -2.0, hb.H, 1e-6)
	assert.InDelta(t, 4.0, hb.Slope, 1e-6)
}

// TestCometGeoAtPerihelion verifies the universal-variable propagation: at perihelion the heliocentric
// distance must equal the perihelion distance q.
func TestCometGeoAtPerihelion(t *testing.T) {
	data, err := os.ReadFile("testdata/cometels_sample.txt")
	require.NoError(t, err)
	comets := parseComets(data)
	require.NotEmpty(t, comets)

	c := comets[0]
	at := jdeToTime(c.TpJDE)
	_, _, rHelio, delta := cometGeo(c, at)
	assert.InDelta(t, c.Q, rHelio, 1e-3, "heliocentric distance at perihelion equals q")
	assert.Greater(t, delta, 0.0)
	// And a day later the comet has moved outward (r increases past perihelion).
	_, _, rLater, _ := cometGeo(c, jdeToTime(c.TpJDE+1))
	assert.Greater(t, rLater, rHelio-1e-6)
}
