package skypano

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func archCanvas() Canvas {
	// An arch centred 30 degrees up, drawn stereographically — the way nightpano builds it.
	return Canvas{
		Proj: Stereographic, Fr: Horizon,
		W: 401, H: 401, Lon0: 180, Lat0: 30, ScaleDegPerPix: 0.25,
		SiteLatDeg: 43.5, LSTDeg: 300,
	}
}

func TestCanvas_AltitudeAt(t *testing.T) {
	c := archCanvas()
	cx, cy := float64(c.W)/2, float64(c.H)/2

	t.Run("the centre is the canvas centre altitude", func(t *testing.T) {
		alt, ok := c.AltitudeAt(cx, cy)
		require.True(t, ok)
		assert.InDelta(t, c.Lat0, alt, 1e-6)
	})

	t.Run("altitude rises upward and falls downward", func(t *testing.T) {
		up, ok1 := c.AltitudeAt(cx, cy-100)
		down, ok2 := c.AltitudeAt(cx, cy+100)
		require.True(t, ok1)
		require.True(t, ok2)
		assert.Greater(t, up, c.Lat0, "a pixel above centre must be higher in the sky")
		assert.Less(t, down, c.Lat0, "a pixel below centre must be lower in the sky")
		// 100 px at 0.25 deg/px is 25 degrees; stereographic stretches with radius, so allow slack.
		assert.InDelta(t, 25, up-c.Lat0, 3.0)
	})

	// The whole point: the horizon is not a row on a stereographic canvas, so the sign has to come
	// from the projection rather than from y.
	t.Run("it crosses zero below the centre", func(t *testing.T) {
		var crossing float64
		found := false
		for dy := 0.0; dy < float64(c.H)/2; dy++ {
			if alt, ok := c.AltitudeAt(cx, cy+dy); ok && alt < 0 {
				crossing, found = dy, true
				break
			}
		}
		require.True(t, found, "the canvas should reach below the horizon")
		assert.Greater(t, crossing, 100.0, "30 degrees down cannot be fewer than 100 rows at 0.25 deg/px")
	})

	t.Run("it refuses a canvas that is not in the horizon frame", func(t *testing.T) {
		for _, fr := range []Frame{Equatorial, Galactic} {
			d := c
			d.Fr = fr
			_, ok := d.AltitudeAt(cx, cy)
			assert.False(t, ok, "frame %v must not report an altitude", fr)
		}
	})
}

// The refactor that introduced AltitudeAt pulled the projection maths out of PixToSky; the landscape
// under an arch is entirely below the horizon and is projected through that same call, so a negative
// altitude must still map.
func TestCanvas_PixToSky_StillMapsBelowTheHorizon(t *testing.T) {
	c := archCanvas()
	cx, cy := float64(c.W)/2, float64(c.H)/2

	var tested int
	for dy := 0.0; dy < float64(c.H)/2; dy++ {
		alt, ok := c.AltitudeAt(cx, cy+dy)
		if !ok || alt >= 0 {
			continue
		}
		_, skyOK := c.PixToSky(cx, cy+dy)
		require.True(t, skyOK, "PixToSky refused a pixel %0.2f degrees below the horizon", alt)
		if tested++; tested > 20 {
			break
		}
	}
	require.Positive(t, tested, "no below-horizon pixel was exercised")
}
