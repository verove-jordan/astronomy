package polaralign

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// axisAt builds an Axis pointing a known amount away from the pole, without going through the fit. It
// is misalignedAxis with the fit metadata a Correction never reads filled in, so the simulator and the
// tests describe a misaligned mount the same way.
func axisAt(site Site, altErrDeg, azKnobDeg float64) Axis {
	a := misalignedAxis(site, altErrDeg, azKnobDeg)
	a.RadiusDeg, a.ArcDeg, a.Samples = 70, 60, 4
	return a
}

// The instruction has to be right in both hemispheres, and "left" is a different compass direction in
// each — which is exactly why the API answers in compass words rather than in signs.
func TestCorrect_DirectionsInBothHemispheres(t *testing.T) {
	for _, site := range []Site{{48.8566, 2.3522}, {-33.87, 151.2}} {
		t.Run(hemisphere(site.LatDeg >= 0), func(t *testing.T) {
			high := Correct(axisAt(site, +0.5, 0), site)
			assert.Equal(t, MoveLower, high.AltMove)
			assert.InDelta(t, 0.5, high.AltErrorDeg, 1e-9)

			low := Correct(axisAt(site, -0.5, 0), site)
			assert.Equal(t, MoveRaise, low.AltMove)
			assert.InDelta(t, -0.5, low.AltErrorDeg, 1e-9)

			east := Correct(axisAt(site, 0, +0.5), site)
			assert.Equal(t, MoveWest, east.AzMove, "an axis east of the pole must move west")
			assert.Greater(t, east.AzErrorDeg, 0.0, "east is positive in both hemispheres")

			west := Correct(axisAt(site, 0, -0.5), site)
			assert.Equal(t, MoveEast, west.AzMove)
			assert.Less(t, west.AzErrorDeg, 0.0)
		})
	}
}

// The trap this test exists for: the azimuth knob turns through MORE than the error it removes, by
// 1/cos(latitude). Reporting only the sky angle makes every user undershoot, every time.
func TestCorrect_KnobTurnExceedsTheSkyError(t *testing.T) {
	for _, lat := range []float64{0, 30, 45, 60} {
		site := Site{lat, 2.3522}
		c := Correct(axisAt(site, 0, 1), site)
		assert.InDelta(t, math.Cos(lat*deg2rad), c.AzErrorDeg/c.AzKnobDeg, 1e-3,
			"at latitude %g the knob must turn 1/cos further than the sky error", lat)
	}
	// At 60° that is a factor of two — the difference between converging and going in circles.
	site := Site{60, 0}
	c := Correct(axisAt(site, 0, 1), site)
	assert.InDelta(t, 0.5, c.AzErrorDeg, 1e-3)
	assert.InDelta(t, 1.0, c.AzKnobDeg, 1e-3)
}

func TestCorrect_TotalAndQuality(t *testing.T) {
	site := Site{48.8566, 2.3522}
	for _, c := range []struct {
		altErr, azKnob float64
		wantQuality    string
	}{
		{0, 0, QualityExcellent},
		{0.02, 0, QualityGood},
		{0.1, 0, QualityFair},
		{1, 0, QualityPoor},
	} {
		got := Correct(axisAt(site, c.altErr, c.azKnob), site)
		assert.Equal(t, c.wantQuality, got.Quality, "alt error %g°", c.altErr)
		assert.InDelta(t, math.Abs(c.altErr)*60, got.TotalArcmin, 0.01)
	}

	// Already good enough: say so instead of sending the user to touch a bolt for nothing.
	perfect := Correct(axisAt(site, 0, 0), site)
	assert.Equal(t, MoveNone, perfect.AltMove)
	assert.Equal(t, MoveNone, perfect.AzMove)
}

// The load-bearing property of the correction rotation: turning the two adjusters as instructed puts
// the polar axis exactly on the pole. Every sign in correct.go is pinned by this one assertion.
func TestCorrection_LandsTheAxisOnThePole(t *testing.T) {
	for _, site := range []Site{{48.8566, 2.3522}, {-33.87, 151.2}, {5, -74}, {70, 20}} {
		for _, altErr := range []float64{-5, -0.5, 0, 0.3, 5} {
			for _, azKnob := range []float64{-5, -0.7, 0, 0.4, 5} {
				a := axisAt(site, altErr, azKnob)
				c := Correct(a, site)
				landed := c.rotation().apply(a.vec())
				off := angleBetween(landed, poleHorizon(site.LatDeg)) * 3600
				assert.Less(t, off, 1e-6,
					"lat %g alt %g az %g left the axis %.4g″ from the pole", site.LatDeg, altErr, azKnob, off)
			}
		}
	}
}

// Why the target marker MUST come from the hardware rotation and not from the obvious "shortest
// rotation that puts the axis on the pole".
//
// Both land the axis on the pole, so both are valid answers to "where should the axis go". They differ
// by a twist ABOUT the pole — and that twist is first order in the azimuth error, not second: swinging
// the mount in azimuth by δ turns the whole sky about the zenith, and the zenith is sin(latitude) of
// the way toward the pole, so the sky twists by δ·sin(lat) that the shortest rotation does not
// include. At latitude 49 that is three quarters of the azimuth error, applied to the whole field.
//
// Ten arcminutes of azimuth error would therefore put the marker seven arcminutes wrong — far enough
// to send the user turning the bolt the wrong way. Hence rotation() models what the bolts do.
func TestCorrection_IsNotTheShortestRotation(t *testing.T) {
	site := Site{48.8566, 2.3522}
	const azErrDeg = 0.01 // 36″ of azimuth
	a := axisAt(site, 0, azErrDeg)
	c := Correct(a, site)

	// A direction well away from the pole is where a twist about the pole shows up most.
	probe := horizonVec(20, 120)
	pole := poleHorizon(site.LatDeg)

	shortAxis, ok := a.vec().cross(pole).unit()
	require.True(t, ok)
	viaShortest := rotateAboutH(probe, shortAxis, angleBetween(a.vec(), pole))
	viaBolts := c.rotation().apply(probe)

	// Both put the AXIS in the same place...
	assert.Less(t, angleBetween(c.rotation().apply(a.vec()), pole)*3600, 1e-6)
	assert.Less(t, angleBetween(rotateAboutH(a.vec(), shortAxis, angleBetween(a.vec(), pole)), pole)*3600, 1e-6)

	// ...and send the rest of the sky to measurably different places.
	gap := angleBetween(viaBolts, viaShortest)
	wantTwist := azErrDeg * math.Sin(site.LatDeg*deg2rad)
	wantGap := wantTwist * math.Sin(angleBetween(probe, pole)*deg2rad)
	assert.InDelta(t, wantGap*3600, gap*3600, 0.5,
		"the two rotations should differ by the azimuth error times sin(latitude)")
	assert.Greater(t, gap*3600, 20.0, "and that difference is first order, not a rounding detail")
}

// The ordering inside rotation() — azimuth first, then altitude — IS only a second-order concern, and
// this is what separates it from the first-order twist above. Doing the two stages the other way round
// moves the sky by about the product of the two errors, which vanishes as the user converges.
func TestCorrection_StageOrderIsSecondOrder(t *testing.T) {
	site := Site{48.8566, 2.3522}
	probe := horizonVec(20, 120)

	for _, errDeg := range []float64{1, 0.1, 0.01} {
		a := axisAt(site, errDeg, errDeg)
		r := Correct(a, site).rotation()

		forward := rotateEast(rotateZenith(probe, r.azDeg), r.tiltDeg)
		reversed := rotateZenith(rotateEast(probe, r.tiltDeg), r.azDeg)
		gap := angleBetween(forward, reversed)

		// Product of the two errors, in the same units — the signature of a second-order term.
		assert.Less(t, gap, 2*errDeg*errDeg,
			"at %g° of error the stage order cost %.4g°", errDeg, gap)
	}
}

// MisalignPointing is the simulator's half of the loop, so the test that matters is that the two
// halves agree: dial a known error into a synthetic mount, sweep it, run the real fit, and get the
// same numbers back. Anything that drifts between the forward model and the measurement shows up here.
func TestMisalignPointing_IsRecoveredByTheFit(t *testing.T) {
	for _, site := range []Site{{48.8566, 2.3522}, {-33.87, 151.2}, {12, 77.6}} {
		for _, c := range []struct{ altErr, azKnob float64 }{
			{0.5, 0}, {0, 0.75}, {-0.4, 0.6}, {2, -1.5}, {0.05, -0.05},
		} {
			// A telescope on a PERFECT mount, swept about the pole: constant declination, stepping
			// hour angle. The simulator then bends each of those into where a misaligned mount would
			// really have been pointing.
			samples := make([]Sample, 4)
			for i := range samples {
				at := testEpoch.Add(time.Duration(i*90) * time.Second)
				haDeg := -30 + float64(i)*20
				ideal := haVec(haDeg, 25).horizon(site.LatDeg)
				idealRA, idealDec := skyFromDir(ideal, site, at, FitOptions{})
				ra, dec := MisalignPointing(idealRA, idealDec, site, at, c.altErr, c.azKnob)
				samples[i] = Sample{RADeg: ra, DecDeg: dec, At: at}
			}

			axis, err := FitAxis(samples, site, FitOptions{})
			require.NoError(t, err, "lat %g alt %g az %g", site.LatDeg, c.altErr, c.azKnob)
			got := Correct(axis, site)

			assert.InDelta(t, c.altErr, got.AltErrorDeg, 0.01,
				"lat %g: altitude error came back wrong", site.LatDeg)
			assert.InDelta(t, c.azKnob, got.AzKnobDeg, 0.01,
				"lat %g: azimuth knob came back wrong", site.LatDeg)
		}
	}
}

// Zero has to be exactly zero: a simulator with no error configured must behave as it always did,
// down to the last bit, or every existing test of it becomes a coin toss.
func TestMisalignPointing_ZeroIsExactlyANoOp(t *testing.T) {
	site := Site{48.8566, 2.3522}
	for _, p := range [][2]float64{{83.6, 22.0}, {201.3, -60.4}, {0, 89.9}} {
		ra, dec := MisalignPointing(p[0], p[1], site, testEpoch, 0, 0)
		assert.Equal(t, p[0], ra)
		assert.Equal(t, p[1], dec)
	}
}
