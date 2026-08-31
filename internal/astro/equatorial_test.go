package astro

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestEquatorial_RoundTripsHorizontal is the contract that matters: Equatorial must undo Horizontal
// exactly, because a phone frame's place on the sky is derived one way and checked the other.
func TestEquatorial_RoundTripsHorizontal(t *testing.T) {
	// The 2026-08-11 session's site and mid-session instant, plus the poles and the meridian, which
	// are where an atan2-based inverse would break if the quadrants were wrong.
	site := struct{ lat, lon float64 }{47.2768, -2.4939}
	when := time.Date(2026, 8, 11, 0, 30, 0, 0, time.UTC)

	tests := []struct {
		name     string
		ra, dec  float64
		lat, lon float64
	}{
		{"galactic centre low in the south-west", 266.4, -29.0, site.lat, site.lon},
		{"perseus high in the north-east", 48.2, 58.0, site.lat, site.lon},
		{"near the north celestial pole", 37.95, 89.26, site.lat, site.lon},
		{"on the celestial equator", 180, 0, site.lat, site.lon},
		{"far south, below this horizon", 90, -70, site.lat, site.lon},
		{"southern-hemisphere observer", 100, -40, -33.9, 151.2},
		{"equatorial observer", 12, 5, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alt, az := Horizontal(tt.ra, tt.dec, tt.lat, tt.lon, when)
			gotRA, gotDec := Equatorial(alt, az, tt.lat, tt.lon, when)

			assert.InDelta(t, tt.dec, gotDec, 1e-9, "declination")
			// Compare RA as a separation so 359.999 and 0.001 do not read as a 360-degree miss.
			assert.InDelta(t, 0, AngularSeparation(tt.ra, tt.dec, gotRA, gotDec), 1e-7, "position")
		})
	}
}

// TestEquatorial_KnownPointings pins the two directions anyone can check by hand: the zenith is at
// the observer's latitude on the meridian, and due north on the horizon sits below the pole.
func TestEquatorial_KnownPointings(t *testing.T) {
	lat, lon := 47.2768, -2.4939
	when := time.Date(2026, 8, 11, 0, 30, 0, 0, time.UTC)

	t.Run("zenith declination equals the observer latitude", func(t *testing.T) {
		ra, dec := Equatorial(90, 0, lat, lon, when)
		assert.InDelta(t, lat, dec, 1e-9)
		assert.InDelta(t, 0, math.Abs(HourAngleDeg(ra, lon, when)), 1e-7, "the zenith is on the meridian")
	})

	t.Run("due north on the horizon is below the pole", func(t *testing.T) {
		_, dec := Equatorial(0, 0, lat, lon, when)
		assert.InDelta(t, 90-lat, dec, 1e-9)
	})
}
