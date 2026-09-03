package pointing

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

// TestAltRoll pins the decomposition against gravity vectors read out of real frames from the
// 2026-08-11 session, plus the degenerate orientations that have an obvious right answer.
func TestAltRoll(t *testing.T) {
	tests := []struct {
		name     string
		g        [3]float64
		wantAlt  float64
		wantRoll float64
		wantOK   bool
	}{
		{
			name: "IMG_0892, aimed at the galactic centre in the south-west",
			g:    [3]float64{0.02677475101, -0.7755961416, 0.6358115677},
			// Portrait, tilted a little under halfway up and held almost square to the vertical.
			wantAlt: 39.33, wantRoll: 1.98, wantOK: true,
		},
		{
			name: "IMG_1040, aimed near the zenith",
			g:    [3]float64{0.00964, -0.24811, 0.97155},
			// Nearly straight up: the difference between this and the dark below is what makes the
			// calibration frames self-identifying.
			wantAlt: 75.66, wantRoll: 2.23, wantOK: true,
		},
		{
			name: "IMG_0910, a dark shot with the phone face-down",
			g:    [3]float64{0.01800, -0.13276, -0.98652},
			// The rear camera is pointing at the table. No light frame can look like this.
			wantAlt: -82.27, wantRoll: 7.72, wantOK: true,
		},
		{
			name:    "held upright, aimed at the horizon",
			g:       [3]float64{0, -1, 0},
			wantAlt: 0, wantRoll: 0, wantOK: true,
		},
		{
			name: "flat on its back, aimed at the zenith",
			g:    [3]float64{0, 0, 1},
			// Roll has no meaning here: nothing is left of gravity in the image plane. The guard must
			// report 0 rather than the 180 that a bare atan2 of two zeroes returns.
			wantAlt: 90, wantRoll: 0, wantOK: true,
		},
		{
			name:    "rolled thirty degrees at the horizon",
			g:       [3]float64{0.5, -math.Sqrt(3) / 2, 0},
			wantAlt: 0, wantRoll: 30, wantOK: true,
		},
		{
			name:   "no vector at all",
			g:      [3]float64{0, 0, 0},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alt, roll, ok := AltRoll(tt.g)

			require.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			assert.InDelta(t, tt.wantAlt, alt, 0.01, "altitude")
			assert.InDelta(t, tt.wantRoll, roll, 0.01, "roll")
		})
	}
}

// TestSeparationDeg_NearZenithAzimuthIsCompressed is the reason separation is computed from the
// axis vectors and not from a difference of azimuths. The same ten degrees of compass is ten
// degrees of sky at the horizon but under three near the zenith, and grouping on the raw number
// would shatter a zenith panel into several.
func TestSeparationDeg_NearZenithAzimuthIsCompressed(t *testing.T) {
	atHorizon := SeparationDeg(Frame{AzDeg: 40, AltDeg: 0}, Frame{AzDeg: 50, AltDeg: 0})
	nearZenith := SeparationDeg(Frame{AzDeg: 40, AltDeg: 75.6}, Frame{AzDeg: 50, AltDeg: 75.6})

	assert.InDelta(t, 10, atHorizon, 1e-9)
	assert.InDelta(t, 2.484, nearZenith, 0.01)
}

func TestSeparationDeg_KnownPairs(t *testing.T) {
	tests := []struct {
		name    string
		a, b    Frame
		wantSep float64
	}{
		{"identical pointings", Frame{AzDeg: 44, AltDeg: 75}, Frame{AzDeg: 44, AltDeg: 75}, 0},
		{"pure altitude change", Frame{AzDeg: 206, AltDeg: 16}, Frame{AzDeg: 206, AltDeg: 39}, 23},
		{"zenith to horizon", Frame{AzDeg: 0, AltDeg: 90}, Frame{AzDeg: 123, AltDeg: 0}, 90},
		{"opposite horizons", Frame{AzDeg: 35, AltDeg: 0}, Frame{AzDeg: 215, AltDeg: 0}, 180},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.wantSep, SeparationDeg(tt.a, tt.b), 1e-6)
		})
	}
}

// TestFrame_Equatorial_RoundTrip checks the pointing lands back where it started once converted to
// the sky and read off again.
func TestFrame_Equatorial_RoundTrip(t *testing.T) {
	f := Frame{
		AzDeg: 44.1, AltDeg: 75.6, RollDeg: 2.2,
		LatDeg: 47.2768, LonDeg: -2.4939,
		At:      time.Date(2026, 8, 11, 1, 15, 0, 0, time.UTC),
		HasSite: true, HasTime: true,
	}

	ra, dec, _, ok := f.Equatorial()
	require.True(t, ok)

	alt, az := astro.Horizontal(ra, dec, f.LatDeg, f.LonDeg, f.At)
	assert.InDelta(t, f.AltDeg, alt, 1e-9)
	assert.InDelta(t, f.AzDeg, az, 1e-9)
}

// TestFrame_Equatorial_PositionAngle pins the orientation of the image on the sky using pointings
// whose answer can be reasoned out by hand.
func TestFrame_Equatorial_PositionAngle(t *testing.T) {
	when := time.Date(2026, 8, 11, 1, 15, 0, 0, time.UTC)

	t.Run("aimed due north with the image upright, up is celestial north", func(t *testing.T) {
		// Tilting up from the north horizon point heads straight for the pole, so the top of the
		// image points at declination-increasing north: position angle zero.
		f := Frame{AzDeg: 0, AltDeg: 0, LatDeg: 47.2768, LonDeg: -2.4939, At: when, HasSite: true, HasTime: true}

		_, _, pa, ok := f.Equatorial()

		require.True(t, ok)
		assert.InDelta(t, 0, math.Min(pa, 360-pa), 1e-6)
	})

	t.Run("aimed due east from the equator, up is celestial west", func(t *testing.T) {
		// On the equator the celestial equator runs east horizon -> zenith -> west horizon, and the
		// eastern horizon holds the LARGER right ascension (it reaches the meridian later). Tilting
		// up therefore heads west, position angle 270. A flipped east vector would give 90.
		f := Frame{AzDeg: 90, AltDeg: 0, LatDeg: 0, LonDeg: 0, At: when, HasSite: true, HasTime: true}

		_, _, pa, ok := f.Equatorial()

		require.True(t, ok)
		assert.InDelta(t, 270, pa, 1e-6)
	})

	t.Run("roll rotates the image one for one", func(t *testing.T) {
		base := Frame{AzDeg: 206, AltDeg: 39, LatDeg: 47.2768, LonDeg: -2.4939, At: when, HasSite: true, HasTime: true}
		rolled := base
		rolled.RollDeg = 30

		_, _, paBase, _ := base.Equatorial()
		_, _, paRolled, _ := rolled.Equatorial()

		diff := math.Mod(math.Abs(paRolled-paBase), 360)
		assert.InDelta(t, 30, math.Min(diff, 360-diff), 1e-6)
	})
}

func TestFromMeta(t *testing.T) {
	full := rawmeta.Meta{
		Gravity: [3]float64{0.00964, -0.24811, 0.97155}, HasGravity: true,
		CompassDeg: 43.891, HasCompass: true,
		LatDeg: 47.2768, LonDeg: -2.4939, HasSite: true,
		TakenAtMs: time.Date(2026, 8, 11, 1, 23, 13, 0, time.UTC).UnixMilli(),
	}

	tests := []struct {
		name    string
		meta    rawmeta.Meta
		wantOK  bool
		wantAlt float64
	}{
		{"complete iphone frame", full, true, 75.66},
		{"no gravity means no tilt, which must not read as the horizon", withoutGravity(full), false, 0},
		{"no compass means no bearing", withoutCompass(full), false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := FromMeta(tt.meta)

			require.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			assert.InDelta(t, tt.wantAlt, f.AltDeg, 0.01)
			assert.InDelta(t, tt.meta.CompassDeg, f.AzDeg, 1e-9)
			assert.True(t, f.HasSite)
			assert.True(t, f.HasTime)
		})
	}
}

func withoutGravity(m rawmeta.Meta) rawmeta.Meta { m.HasGravity = false; return m }
func withoutCompass(m rawmeta.Meta) rawmeta.Meta { m.HasCompass = false; return m }
