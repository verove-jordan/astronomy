package skypano

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanvas_RoundTrip(t *testing.T) {
	for _, c := range []struct {
		name string
		cv   Canvas
	}{
		{"stereographic equatorial", Canvas{Proj: Stereographic, Fr: Equatorial, W: 4000, H: 3000, Lon0: 300, Lat0: 20, ScaleDegPerPix: 0.03}},
		{"stereographic galactic", Canvas{Proj: Stereographic, Fr: Galactic, W: 4000, H: 3000, Lon0: 60, Lat0: 0, ScaleDegPerPix: 0.03}},
		{"equirectangular equatorial", Canvas{Proj: Equirectangular, Fr: Equatorial, W: 4000, H: 3000, Lon0: 300, Lat0: 20, ScaleDegPerPix: 0.03}},
		{"equirectangular galactic", Canvas{Proj: Equirectangular, Fr: Galactic, W: 6000, H: 2000, Lon0: 60, Lat0: 0, ScaleDegPerPix: 0.03}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, p := range [][2]float64{
				{float64(c.cv.W) / 2, float64(c.cv.H) / 2},
				{10, 10},
				{float64(c.cv.W) - 10, 10},
				{10, float64(c.cv.H) - 10},
				{float64(c.cv.W) - 10, float64(c.cv.H) - 10},
			} {
				v, ok := c.cv.PixToSky(p[0], p[1])
				require.True(t, ok, "pixel %v should project", p)
				assert.InDelta(t, 1, math.Sqrt(dot3(v, v)), 1e-12, "must be a unit vector")

				x, y, ok := c.cv.SkyToPix(v)
				require.True(t, ok)
				// A thousandth of a pixel: the round trip goes through several inverse trig
				// functions, so it accumulates a little, and a millionth would be measuring float64
				// rather than the projection.
				assert.InDelta(t, p[0], x, 1e-3, "x")
				assert.InDelta(t, p[1], y, 1e-3, "y")
			}
		})
	}
}

// TestCanvas_StereographicSpansMoreThanAHemisphere is the property that ruled internal/mosaic out: a
// gnomonic canvas is undefined 90 degrees from its centre, and a Milky Way arch is wider than that.
func TestCanvas_StereographicSpansMoreThanAHemisphere(t *testing.T) {
	c := Canvas{Proj: Stereographic, Fr: Equatorial, W: 100, H: 100, Lon0: 0, Lat0: 0, ScaleDegPerPix: 1}
	centre := RADecToVec(0, 0)

	for _, sep := range []float64{45, 89, 90, 120, 150} {
		v := RADecToVec(sep, 0)
		x, y, ok := c.SkyToPix(v)

		require.True(t, ok, "%.0f degrees from centre must still project", sep)
		back, ok := c.PixToSky(x, y)
		require.True(t, ok)
		got := math.Acos(clamp1(dot3(centre, back))) * 180 / math.Pi
		assert.InDelta(t, sep, got, 1e-6, "separation must survive the round trip")

	}
}

// TestGalacticFrame_PutsTheBandOnTheHorizontal checks the conversion against the two directions that
// define it.
func TestGalacticFrame_PutsTheBandOnTheHorizontal(t *testing.T) {
	t.Run("the galactic centre is the origin", func(t *testing.T) {
		l, b := vecToLonLat(equatorialToGalactic(RADecToVec(266.40500, -28.93617)))

		assert.InDelta(t, 0, math.Min(l, 360-l), 0.01, "longitude")
		assert.InDelta(t, 0, b, 0.01, "latitude")
	})

	t.Run("the galactic north pole is at latitude 90", func(t *testing.T) {
		_, b := vecToLonLat(equatorialToGalactic(RADecToVec(192.85948, 27.12825)))

		assert.InDelta(t, 90, b, 0.01)
	})

	t.Run("round trip", func(t *testing.T) {
		for _, v := range [][2]float64{{0, 0}, {279.23, 38.78}, {310.36, 45.28}, {45, -60}} {
			eq := RADecToVec(v[0], v[1])
			ra, dec := VecToRADec(galacticToEquatorial(equatorialToGalactic(eq)))

			assert.InDelta(t, v[1], dec, 1e-9)
			assert.InDelta(t, 0, math.Min(math.Abs(ra-v[0]), 360-math.Abs(ra-v[0])), 1e-9)
		}
	})
}

func TestEdgeWeight(t *testing.T) {
	o := RenderOptions{FeatherPx: 100, EdgeTrimPx: 10}

	tests := []struct {
		name string
		x, y float64
		want float64
	}{
		{"deep inside is full weight", 2000, 1500, 1},
		{"outside the trim is zero", 5, 1500, 0},
		{"exactly at the trim is zero", 10, 1500, 0},
		{"half way through the feather", 60, 1500, 0.5},
		{"just inside the feather", 109, 1500, 0.9998},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, edgeWeight(tt.x, tt.y, 4032, 3024, o), 0.01)
		})
	}
}
