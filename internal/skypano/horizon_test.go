package skypano

import (
	"fmt"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/pointing"
)

// The three directions below have answers that follow from the definition of the horizon rather than
// from any formula, which is what makes them worth testing: they pin the HANDEDNESS. Get azimuth
// backwards and the arch is mirrored east for west, which looks entirely plausible in a finished
// panorama and is discoverable only against cases like these.
func TestHorizonToEquatorial_KnownDirections(t *testing.T) {
	const lat, lst = 47.2767, 100.0 // the Loire-Atlantique coast; any sidereal time will do

	t.Run("due north at the latitude's altitude is the celestial pole", func(t *testing.T) {
		_, dec := VecToRADec(horizonToEquatorial(0, lat, lat, lst))
		if math.Abs(dec-90) > 1e-6 {
			t.Errorf("dec = %.6f, want +90", dec)
		}
	})

	t.Run("the zenith is at the site's latitude, on the meridian", func(t *testing.T) {
		ra, dec := VecToRADec(horizonToEquatorial(0, 90, lat, lst))
		if math.Abs(dec-lat) > 1e-6 {
			t.Errorf("dec = %.6f, want the latitude %.6f", dec, lat)
		}
		if d := angleGap(ra, lst); d > 1e-6 {
			t.Errorf("RA = %.6f, want the sidereal time %.1f", ra, lst)
		}
	})

	// Due east on the horizon is where the celestial equator rises. An object there is six hours from
	// transit and rising, so its hour angle is -6h and its right ascension is the sidereal time plus
	// 90 degrees. Due WEST is the same declination and the other sign, which is the whole point.
	t.Run("due east on the horizon is rising, six hours before transit", func(t *testing.T) {
		ra, dec := VecToRADec(horizonToEquatorial(90, 0, lat, lst))
		if math.Abs(dec) > 1e-6 {
			t.Errorf("dec = %.6f, want 0", dec)
		}
		if d := angleGap(ra, lst+90); d > 1e-6 {
			t.Errorf("RA = %.6f, want %.1f (east of the meridian)", ra, lst+90)
		}
		raW, _ := VecToRADec(horizonToEquatorial(270, 0, lat, lst))
		if d := angleGap(raW, lst-90); d > 1e-6 {
			t.Errorf("due west RA = %.6f, want %.1f", raW, lst-90)
		}
	})
}

func TestHorizon_RoundTrips(t *testing.T) {
	const lat, lst = 47.2767, 213.5
	for _, az := range []float64{0, 44, 90, 180, 206, 300, 359} {
		for _, alt := range []float64{0.5, 16, 39, 63, 75.6, 89} {
			v := horizonToEquatorial(az, alt, lat, lst)
			gotAz, gotAlt := equatorialToHorizon(v, lat, lst)
			if d := angleGap(gotAz, az); d > 1e-6 {
				t.Errorf("az %.1f alt %.1f: azimuth came back as %.6f", az, alt, gotAz)
			}
			if math.Abs(gotAlt-alt) > 1e-6 {
				t.Errorf("az %.1f alt %.1f: altitude came back as %.6f", az, alt, gotAlt)
			}
		}
	}
}

// A canvas in the horizon frame must place a direction where the conversion says it stands, and get
// it back. This is the property Render depends on.
// BOTH projections must round-trip in the horizon frame. Only equirectangular was covered at first,
// and the gap hid a real bug: the stereographic branch works on the direction VECTOR, and the horizon
// case was leaving that vector in equatorial coordinates while the canvas centre was in azimuth and
// altitude — two frames, so every angle was wrong and panels landed at negative pixel coordinates.
func TestCanvas_HorizonFrameRoundTrips(t *testing.T) {
	for _, proj := range []Projection{Equirectangular, Stereographic} {
		c := Canvas{
			Proj: proj, Fr: Horizon, W: 2000, H: 900,
			Lon0: 130, Lat0: 40, ScaleDegPerPix: 0.1,
			SiteLatDeg: 47.2767, LSTDeg: 213.5,
		}
		for _, p := range [][2]float64{{100, 100}, {1000, 450}, {1900, 800}, {500, 200}} {
			v, ok := c.PixToSky(p[0], p[1])
			if !ok {
				t.Fatalf("proj %d: pixel (%.0f,%.0f) did not project", proj, p[0], p[1])
			}
			x, y, ok := c.SkyToPix(v)
			if !ok {
				t.Fatalf("proj %d: direction from (%.0f,%.0f) did not come back", proj, p[0], p[1])
			}
			if math.Abs(x-p[0]) > 1e-3 || math.Abs(y-p[1]) > 1e-3 {
				t.Errorf("proj %d: (%.0f,%.0f) round-tripped to (%.3f,%.3f)", proj, p[0], p[1], x, y)
			}
		}
	}
}

// The point of naming ONE instant: the same star is at a different azimuth an hour later, so a
// panorama assembled without fixing the instant would place each panel by its own clock.
func TestHorizon_TheSkyTurnsBetweenPanels(t *testing.T) {
	const lat = 47.2767
	v := RADecToVec(266.405, -28.936) // the galactic centre
	az1, alt1 := equatorialToHorizon(v, lat, 100)
	az2, alt2 := equatorialToHorizon(v, lat, 115) // one hour later: 15 degrees of sidereal time
	if math.Abs(az1-az2) < 1 && math.Abs(alt1-alt2) < 1 {
		t.Fatalf("the sky did not move in an hour: (%.2f,%.2f) then (%.2f,%.2f)", az1, alt1, az2, alt2)
	}
	if alt2 <= alt1 {
		t.Errorf("the galactic centre should still be rising at this hour: %.2f then %.2f", alt1, alt2)
	}
}

// angleGap is the separation of two angles in degrees, wrapping at 360.
func angleGap(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 360)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// The arch canvas has to be PLANNED as well as projected, and planning is where a new frame can fail
// quietly: these panels span nearly 180 degrees of azimuth and reach past the zenith, where an
// azimuth/altitude grid converges and a naive extent can come out empty or absurd. The pointings are
// the real ones from the 2026-08-10 session.
func TestPlanCanvasAt_HorizonFrameSpansTheRealArch(t *testing.T) {
	site := pointing.Frame{
		LatDeg: 47.2767, LonDeg: -2.4933,
		At:      time.Date(2026, 8, 11, 2, 27, 30, 0, time.UTC),
		HasSite: true, HasTime: true,
	}
	lst := astro.LST(site.At, site.LonDeg)
	pointings := [][2]float64{
		{215.2, 39.3}, {206.3, 16.2}, {206.6, 39.5}, {34.9, 49.8},
		{35.0, 63.2}, {44.5, 63.1}, {44.4, 74.1}, {44.1, 75.6},
	}
	var panels []Panel
	for i, p := range pointings {
		f := site
		f.AzDeg, f.AltDeg = p[0], p[1]
		// A small stand-in image with a camera scaled to match it: the panel's ANGULAR field is what
		// matters here, and eight full-resolution frames would be a gigabyte of test fixture. The two
		// must be consistent — a full-size camera over a small image makes panelBoundary sample a
		// corner of the field rather than the panel, which is a fixture bug that looks exactly like a
		// projection bug.
		const pw, ph = 64, 48
		cam, ok := PriorCamera(f, 6, pw, ph, true) // orientation 6: the phone shot these in portrait
		if !ok {
			t.Fatalf("pointing %d gave no camera", i)
		}
		cam.F = pw / 2 / math.Tan(36*deg) // the frames' real ~72-degree width
		panels = append(panels, Panel{Name: fmt.Sprintf("p%02d", i+1), Cam: cam,
			Img: fits.NewImage(pw, ph, 3)})
	}

	c, err := PlanCanvasAt(panels, Equirectangular, Horizon, 0.03, site.LatDeg, lst)
	if err != nil {
		t.Fatalf("the arch canvas could not be planned: %v", err)
	}
	// Azimuth across, altitude up: the arch is far wider than it is tall.
	if c.W <= c.H {
		t.Errorf("canvas %dx%d is not laid out as an arch", c.W, c.H)
	}
	// It must cover the real span — about 170 degrees of azimuth — without running away.
	spanDeg := float64(c.W) * c.ScaleDegPerPix
	if spanDeg < 120 || spanDeg > 360 {
		t.Errorf("canvas spans %.0f degrees of azimuth, want the arch's ~170", spanDeg)
	}
	if c.SiteLatDeg != site.LatDeg || c.LSTDeg != lst {
		t.Errorf("the canvas did not keep where and when it is standing")
	}

	// Every panel must land on it, and the two ends must land far apart — if azimuth were being
	// wrapped wrongly they would pile up on top of each other.
	var xs []float64
	for _, p := range panels {
		x, y, ok := c.SkyToPix(p.Cam.Axis())
		if !ok {
			t.Fatalf("panel %s does not project onto the arch", p.Name)
		}
		if x < 0 || y < 0 || x > float64(c.W) || y > float64(c.H) {
			t.Errorf("panel %s lands off the canvas at (%.0f,%.0f)", p.Name, x, y)
		}
		xs = append(xs, x)
	}
	sort.Float64s(xs)
	if spread := (xs[len(xs)-1] - xs[0]) * c.ScaleDegPerPix; spread < 100 {
		t.Errorf("the panels only spread %.0f degrees apart; the session spans about 170", spread)
	}
}

// horizonCentre has to find the middle of the arc the panels COVER, not the average of their azimuth
// numbers. A session straddling due north is the case that separates the two: 350 and 10 average to
// 180, which is due south — the opposite side of the sky from the data.
func TestHorizonCentre_HandlesAnArchStraddlingNorth(t *testing.T) {
	const lat, lst = 47.2767, 213.5
	mk := func(azs ...float64) []Panel {
		var out []Panel
		for _, a := range azs {
			cam := Camera{Cx: 32, Cy: 24, F: 32 / math.Tan(36*deg)}
			cam.R = SetRotation(
				[3]float64{1, 0, 0}, [3]float64{0, 1, 0}, horizonToEquatorial(a, 45, lat, lst))
			out = append(out, Panel{Cam: cam, Img: fits.NewImage(64, 48, 3)})
		}
		return out
	}
	az0, _ := horizonCentre(mk(350, 355, 5, 10), lat, lst)
	if d := angleGap(az0, 0); d > 1 {
		t.Errorf("centre = %.2f, want due north; averaging the numbers would say 180", az0)
	}
	az0, _ = horizonCentre(mk(34.9, 44.1, 206.6, 215.2), lat, lst)
	if d := angleGap(az0, 125.05); d > 1 {
		t.Errorf("centre = %.2f, want 125 — the middle of the arc actually covered", az0)
	}
}

// The arch is drawn stereographically because equirectangular cannot hold a field that reaches the
// ZENITH: azimuth is degenerate there, so the crown is stretched over every azimuth. Measured on a
// real session that produced an 11871-pixel canvas whose top was a smear.
func TestPlanCanvasAt_StereographicHoldsAZenithArch(t *testing.T) {
	site := pointing.Frame{
		LatDeg: 47.2767, LonDeg: -2.4933,
		At:      time.Date(2026, 8, 11, 2, 27, 30, 0, time.UTC),
		HasSite: true, HasTime: true,
	}
	lst := astro.LST(site.At, site.LonDeg)
	// The real session: from 16 degrees up to past the zenith.
	pointings := [][2]float64{{206.3, 16.2}, {215.2, 39.3}, {206.6, 39.5}, {34.9, 49.8},
		{35.0, 63.2}, {44.4, 74.1}, {44.1, 75.6}}
	var panels []Panel
	for i, p := range pointings {
		f := site
		f.AzDeg, f.AltDeg = p[0], p[1]
		const pw, ph = 64, 48
		cam, ok := PriorCamera(f, 6, pw, ph, true)
		require.True(t, ok)
		cam.F = pw / 2 / math.Tan(36*deg)
		panels = append(panels, Panel{Name: fmt.Sprintf("p%02d", i+1), Cam: cam, Img: fits.NewImage(pw, ph, 3)})
	}

	eq, err := PlanCanvasAt(panels, Equirectangular, Horizon, 0.03, site.LatDeg, lst)
	require.NoError(t, err)
	st, err := PlanCanvasAt(panels, Stereographic, Horizon, 0.03, site.LatDeg, lst)
	require.NoError(t, err)

	// Equirectangular has to spread the zenith over the whole circle of azimuth; stereographic does not.
	assert.Greater(t, float64(eq.W)*eq.ScaleDegPerPix, 300.0,
		"equirectangular spans nearly every azimuth once the field reaches the zenith")
	assert.Less(t, st.W, eq.W/2, "stereographic holds the same sky in far less canvas")

	// And every panel still lands on it.
	for _, p := range panels {
		x, y, ok := st.SkyToPix(p.Cam.Axis())
		require.True(t, ok, "panel %s does not project", p.Name)
		assert.GreaterOrEqual(t, x, 0.0)
		assert.GreaterOrEqual(t, y, 0.0)
		assert.LessOrEqual(t, x, float64(st.W))
		assert.LessOrEqual(t, y, float64(st.H))
	}
}
