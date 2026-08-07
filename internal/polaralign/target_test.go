package polaralign

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// testFrame builds a plate solution centred on the given sky position, with a plate scale and a
// rotation. parity < 0 mirrors the image, which is what a star diagonal does and what half the frames
// in this repo's own input folder look like — the marker has to land correctly either way.
func testFrame(t *testing.T, raDeg, decDeg, scaleArcsecPx, rotDeg float64, w, h int, parity float64) Frame {
	t.Helper()
	s := scaleArcsecPx / 3600
	cosR, sinR := math.Cos(rotDeg*deg2rad), math.Sin(rotDeg*deg2rad)
	cd := [2][2]float64{
		{-s * cosR * -parity, s * sinR},
		{s * sinR * -parity, s * cosR},
	}
	wcs, ok := fits.NewTanWCS(raDeg, decDeg, float64(w)/2+1, float64(h)/2+1, cd)
	require.True(t, ok, "the synthetic plate solution must be non-singular")
	return Frame{WCS: wcs, WidthPx: w, HeightPx: h, At: testEpoch}
}

// The assertion that separates a working feature from one that sends the user the wrong way: a polar
// axis east of the pole must put the marker EAST of the frame centre. With R⁻¹ instead of R it lands
// exactly the other side, and every other test in this package still passes.
func TestTarget_MovesEastForAnEastwardAxis(t *testing.T) {
	site := Site{48.8566, 2.3522}
	// Look at the meridian, on the equator, where east is unambiguously "higher right ascension".
	frame := testFrame(t, onMeridian(site), 0, 2, 0, 4656, 3520, 1)
	centreRA, centreDec := frame.WCS.PixToSky(frame.centrePix())

	east := Correct(axisAt(site, 0, +0.5), site)
	tgt, ok := east.Target(frame, FitOptions{})
	require.True(t, ok)
	assert.Greater(t, norm180(tgt.RADeg-centreRA), 0.0,
		"an axis east of the pole must put the target east of centre")

	west := Correct(axisAt(site, 0, -0.5), site)
	tgt, ok = west.Target(frame, FitOptions{})
	require.True(t, ok)
	assert.Less(t, norm180(tgt.RADeg-centreRA), 0.0)

	// And an axis that points too high must put the target higher in declination.
	high := Correct(axisAt(site, +0.5, 0), site)
	tgt, ok = high.Target(frame, FitOptions{})
	require.True(t, ok)
	assert.Greater(t, tgt.DecDeg, centreDec,
		"an axis pointing too high must put the target north of centre")

	low := Correct(axisAt(site, -0.5, 0), site)
	tgt, ok = low.Target(frame, FitOptions{})
	require.True(t, ok)
	assert.Less(t, tgt.DecDeg, centreDec)
}

// The pixel has to be the same point as the sky position, out through SkyToPix and back through
// PixToSky — including when the image is rotated and mirrored.
func TestTarget_PixelRoundTrip(t *testing.T) {
	site := Site{48.8566, 2.3522}
	ra := onMeridian(site)
	for _, parity := range []float64{1, -1} {
		for _, rot := range []float64{0, 37, 195} {
			frame := testFrame(t, ra, 20, 1.06, rot, 4656, 3520, parity)
			c := Correct(axisAt(site, 0.05, -0.03), site)

			tgt, ok := c.Target(frame, FitOptions{})
			require.True(t, ok)

			backRA, backDec := frame.WCS.PixToSky(tgt.X, tgt.Y)
			sep := astro.AngularSeparation(backRA, backDec, tgt.RADeg, tgt.DecDeg) * 3600
			assert.Less(t, sep, 0.05, "parity %g rot %g: pixel and sky disagree by %.3f″", parity, rot, sep)

			// The pixel offset must agree with the angle on the sky at the frame's own plate scale.
			assert.InDelta(t, tgt.OffsetArcmin*60/1.06, tgt.OffsetPx, tgt.OffsetPx*0.01,
				"parity %g rot %g", parity, rot)
			assert.InDelta(t, tgt.X/4656, tgt.NX, 1e-12)
			assert.InDelta(t, tgt.Y/3520, tgt.NY, 1e-12)
		}
	}
}

// A well-aligned mount has nothing to point at: the marker sits on the crosshairs.
func TestTarget_PerfectAlignmentTargetsTheCentre(t *testing.T) {
	site := Site{48.8566, 2.3522}
	frame := testFrame(t, onMeridian(site), 25, 1.06, 12, 4656, 3520, 1)
	c := Correct(axisAt(site, 0, 0), site)

	tgt, ok := c.Target(frame, FitOptions{})
	require.True(t, ok)
	assert.Less(t, tgt.OffsetPx, 0.5)
	assert.False(t, tgt.OffFrame)
	assert.Less(t, tgt.OffsetArcmin, 0.01)
}

// The first measurement of a mount that has never been aligned routinely puts the marker outside the
// image. That is not an error — but the UI has to know, or it draws a reticle nobody can see.
func TestTarget_ReportsOffFrame(t *testing.T) {
	site := Site{48.8566, 2.3522}
	// 1.4° wide field, one degree of error: the marker leaves the frame.
	frame := testFrame(t, onMeridian(site), 20, 1.06, 0, 4656, 3520, 1)

	near := Correct(axisAt(site, 0.05, 0), site)
	tgt, ok := near.Target(frame, FitOptions{})
	require.True(t, ok)
	assert.False(t, tgt.OffFrame)

	far := Correct(axisAt(site, 1.5, 0), site)
	tgt, ok = far.Target(frame, FitOptions{})
	require.True(t, ok)
	assert.True(t, tgt.OffFrame, "a degree and a half of error cannot fit in a 1.4° field")
	assert.InDelta(t, 90, tgt.OffsetArcmin, 2, "but the distance is still reported honestly")
}

// onMeridian is roughly the right ascension crossing the meridian at the test epoch — the cleanest
// place to reason about east and west, since there "east" is unambiguously "higher right ascension".
// Precession makes it a third of a degree out, which does not matter to a question about signs.
func onMeridian(site Site) float64 { return astro.LST(testEpoch, site.LonDeg) }
