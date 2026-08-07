package polaralign

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// poleFrame builds a solved frame pointing a known angle away from the pole, in a known direction.
// offsetAltDeg raises the pointing above the pole, offsetAzDeg carries it east of the meridian.
func poleFrame(t *testing.T, site Site, offsetAltDeg, offsetAzDeg, scaleArcsecPx float64) Frame {
	t.Helper()
	dir := misalignedAxis(site, offsetAltDeg, offsetAzDeg).vec()
	ra, dec := skyFromDir(dir, site, testEpoch, FitOptions{})
	return testFrame(t, ra, dec, scaleArcsecPx, 0, 4656, 3520, 1)
}

// The pole marker has to land where the pole really is — measured against internal/astro rather than
// against this package's own arithmetic.
func TestLocate_PutsTheMarkerOnThePole(t *testing.T) {
	site := Site{48.8566, 2.3522}
	f := poleFrame(t, site, 0.4, -0.3, 4)

	view, ok := Locate(f, site, FitOptions{})
	require.True(t, ok)

	// Where the marker says the pole is, read back out of the solution.
	ra, dec := f.WCS.PixToSky(view.Pole.X, view.Pole.Y)
	// And where the pole astrometrically is: declination 90 today, taken back to the J2000 the
	// solution speaks.
	poleRA, poleDec := astro.PrecessToJ2000(0, 90, f.At)
	gap := astro.AngularSeparation(ra, dec, poleRA, poleDec) * 60

	// The two are not the same point, and the difference is exactly refraction. The marker is where the
	// telescope must be AIMED, and the atmosphere lifts images: a tube pointed mechanically at the pole
	// sees the piece of sky that sits a refraction below it. At this latitude that is under an
	// arcminute — irrelevant to a mode whose accuracy is the cone error — but asserting it against
	// astro.Refraction is what proves the marker is the aiming point and not a coincidence.
	assert.InDelta(t, astro.Refraction(site.LatDeg), gap, 0.02,
		"marker to astrometric pole should be one refraction, got %.2f′", gap)
	assert.False(t, view.Pole.OffFrame)

	// Without the refraction model the two coincide, which is the same statement from the other side.
	plain, ok := Locate(f, site, FitOptions{NoRefraction: true})
	require.True(t, ok)
	ra, dec = f.WCS.PixToSky(plain.Pole.X, plain.Pole.Y)
	assert.Less(t, astro.AngularSeparation(ra, dec, poleRA, poleDec)*3600, 2.0)
}

// The distance readout is what tells a user hunting for the pole how far they have to go, so it has to
// be a real angle rather than a pixel count that means nothing at an unknown plate scale.
func TestLocate_ReportsHowFarTheFieldIsFromThePole(t *testing.T) {
	site := Site{48.8566, 2.3522}
	for _, offsetDeg := range []float64{0.2, 1.0, 3.0} {
		f := poleFrame(t, site, offsetDeg, 0, 4)
		view, ok := Locate(f, site, FitOptions{})
		require.True(t, ok)
		assert.InDelta(t, offsetDeg*60, view.Pole.OffsetArcmin, 0.5,
			"a field %g° from the pole should say so", offsetDeg)
	}

	// Far enough out and the marker leaves the sensor — the normal case when hunting, and the UI has
	// to know rather than draw a reticle nobody can see.
	far, ok := Locate(poleFrame(t, site, 3, 0, 1.06), site, FitOptions{})
	require.True(t, ok)
	assert.True(t, far.Pole.OffFrame)
	assert.InDelta(t, 180, far.Pole.OffsetArcmin, 2, "but the distance is still honest")
}

// Polaris is what the eye is actually looking for, and it is NOT the pole — it sits about three
// quarters of a degree away, which is the whole reason a polar scope has a reticle rather than
// crosshairs.
func TestLocate_MarksTheGuideStarSeparately(t *testing.T) {
	site := Site{48.8566, 2.3522}
	f := poleFrame(t, site, 0, 0, 4) // 1.5° field: wide enough to hold both

	view, ok := Locate(f, site, FitOptions{})
	require.True(t, ok)
	assert.Equal(t, "Polaris", view.StarName)
	assert.True(t, view.StarVisible)

	// How far Polaris is from the pole is not a constant — it is closing on it, from 0.74° at J2000
	// toward about 0.45° next century — so the expected value is computed rather than written down.
	starRA, starDec, _ := astro.PoleStar(true, f.At)
	want := astro.AngularSeparation(starRA, starDec, 0, 90)
	assert.Greater(t, want, 0.3, "sanity: Polaris is not AT the pole, which is why reticles exist")
	assert.Less(t, want, 1.0)

	sep := astro.AngularSeparation(view.Star.RADeg, view.Star.DecDeg, view.Pole.RADeg, view.Pole.DecDeg)
	assert.InDelta(t, want, sep, 0.03, "the two markers should be that far apart on screen")

	// Below the equator it is a different star, and the code must not quietly keep pointing at Polaris.
	south := Site{-33.87, 151.2}
	view, ok = Locate(poleFrame(t, south, 0, 0, 4), south, FitOptions{})
	require.True(t, ok)
	assert.NotEqual(t, "Polaris", view.StarName)
}

// The rough mode's whole claim: with the telescope looking down the right-ascension axis, the offset
// from the centre of the frame to the pole IS the polar error. So a frame aimed a known amount off the
// pole has to report exactly that much.
func TestRoughAxis_ReadsTheOffsetAsThePolarError(t *testing.T) {
	for _, site := range []Site{{48.8566, 2.3522}, {-33.87, 151.2}} {
		for _, c := range []struct{ altErr, azKnob float64 }{
			{0.5, 0}, {0, 0.4}, {-0.3, -0.6}, {0, 0},
		} {
			f := poleFrame(t, site, c.altErr, c.azKnob, 4)
			axis, ok := RoughAxis(f, site, FitOptions{})
			require.True(t, ok)
			got := Correct(axis, site)

			assert.InDelta(t, c.altErr, got.AltErrorDeg, 0.01, "lat %g", site.LatDeg)
			assert.InDelta(t, c.azKnob, got.AzKnobDeg, 0.01, "lat %g", site.LatDeg)
		}
	}
}

// A one-frame answer must never be presented as a measured one. It carries the assumption it rests on
// and an uncertainty that reflects the cone error it cannot see.
func TestRoughAxis_IsHonestAboutWhatItAssumed(t *testing.T) {
	site := Site{48.8566, 2.3522}
	axis, ok := RoughAxis(poleFrame(t, site, 0.4, 0.2, 4), site, FitOptions{})
	require.True(t, ok)

	assert.Contains(t, axis.Warnings, WarnAssumedOnAxis)
	assert.Equal(t, 1, axis.Samples)
	assert.Greater(t, axis.SigmaArcsec, 600.0,
		"a single frame cannot claim better than the cone error it never measured")

	// The measured route, on the same sky, claims two orders of magnitude better.
	samples, _ := sweep(site, 0.4, 0.2, 70, 60, 4, FitOptions{})
	measured, err := FitAxis(samples, site, FitOptions{})
	require.NoError(t, err)
	assert.Less(t, measured.SigmaArcsec, axis.SigmaArcsec/50)
	assert.NotContains(t, measured.Warnings, WarnAssumedOnAxis)
}

// The rough correction feeds the same live loop as the measured one, so the marker it drives the user
// toward has to be the pole itself.
func TestRoughAxis_TargetIsThePole(t *testing.T) {
	site := Site{48.8566, 2.3522}
	f := poleFrame(t, site, 0.6, -0.4, 4)

	axis, ok := RoughAxis(f, site, FitOptions{})
	require.True(t, ok)
	target, ok := Correct(axis, site).Target(f, FitOptions{})
	require.True(t, ok)

	view, ok := Locate(f, site, FitOptions{})
	require.True(t, ok)
	assert.InDelta(t, view.Pole.X, target.X, 1e-9)
	assert.InDelta(t, view.Pole.Y, target.Y, 1e-9)
}

func TestLocate_RejectsAnUnusableFrame(t *testing.T) {
	site := Site{48.8566, 2.3522}
	_, ok := Locate(Frame{}, site, FitOptions{})
	assert.False(t, ok)
	_, ok = RoughAxis(Frame{}, site, FitOptions{})
	assert.False(t, ok)
}
