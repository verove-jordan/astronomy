package scene3d

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The arm loci have to land on real measurements, and the one that matters most is the sign of the
// azimuth: get it backwards and the Galaxy still looks like a Galaxy, with every arm mirrored.
//
// W3(OH) is a maser-measured star-forming region in the Perseus arm at l = 133.95°, b = 1.06°, with a
// VLBI parallax distance of 1.95 kpc (Xu et al. 2006, Science 311, 54). It must sit ON the Perseus
// ridge, well inside the arm's own fitted width.
func TestArmLocus_PlacesTheW3MaserInPerseus(t *testing.T) {
	// l = 133.95°, b = 1.06°, d = 1.95 kpc, in heliocentric galactic parsecs.
	const l, b, d = 133.95, 1.06, 1950.0
	x := d * math.Cos(b*degToRad) * math.Cos(l*degToRad)
	y := d * math.Cos(b*degToRad) * math.Sin(l*degToRad)
	z := d * math.Sin(b*degToRad)

	key, offset := nearestArmOffsetKpc(x, y, z)
	require.Equal(t, "perseus", key, "W3 is a Perseus-arm source")

	var perseus arm
	for _, a := range arms {
		if a.key == "perseus" {
			perseus = a
		}
	}
	assert.Less(t, math.Abs(offset), perseus.widthKpc,
		"W3 must land inside Perseus' own fitted width, not %.2f kpc away", offset)

	// And the guard that actually catches the mirrored frame: flipping the sign of y throws W3 several
	// times further off the ridge, so the test above is not passing by accident.
	_, flipped := nearestArmOffsetKpc(x, -y, z)
	assert.Greater(t, math.Abs(flipped), 3*math.Abs(offset),
		"the mirrored frame must be visibly wrong (%.2f kpc off vs %.2f), else this test proves nothing",
		flipped, offset)
}

func TestArmLocus_PassesThroughItsOwnKink(t *testing.T) {
	for _, a := range arms {
		assert.InDelta(t, a.rKinkKpc, armLocus(a, a.betaKink), 1e-9,
			"%s: the locus must equal r_kink at beta_kink", a.key)
	}
}

// The Sun sits just INSIDE the Local Spur's ridge — a few hundred parsec short of it, not on it and
// not outside it. That is the accepted picture, and it is the second-cheapest check that the whole
// coordinate convention hangs together.
func TestArmLocus_PutsTheSunJustInsideTheLocalArm(t *testing.T) {
	key, offset := nearestArmOffsetKpc(0, 0, 0)
	require.Equal(t, "local", key)

	var local arm
	for _, a := range arms {
		if a.key == "local" {
			local = a
		}
	}
	ridge := armLocus(local, 0)
	assert.InDelta(t, 8.53, ridge, 0.05)
	assert.Greater(t, ridge-RSunKpc, 0.2, "the Sun is inside the ridge, not on it")
	assert.Less(t, ridge-RSunKpc, 0.6)
	assert.Less(t, offset, 0.0, "a negative offset is what 'inside the ridge' means")
}

// Sagittarius–Carina is the next arm inward, the one that makes the summer Milky Way bright. It has to
// land about 1.3 kpc closer to the centre than the Sun.
func TestArmLocus_PutsSagittariusInsideTheSun(t *testing.T) {
	var sgr arm
	for _, a := range arms {
		if a.key == "sagittarius" {
			sgr = a
		}
	}
	assert.InDelta(t, 6.87, armLocus(sgr, 0), 0.05)
	assert.InDelta(t, 1.28, RSunKpc-armLocus(sgr, 0), 0.05)
}

// Where the user's own two fields fall. Nothing in the model was fitted to them, so these are facts it
// has to reproduce rather than checks of the code against itself.
func TestNearestArm_PlacesTheRealFields(t *testing.T) {
	t.Run("the Orion Nebula field is on the Local Arm, below the plane", func(t *testing.T) {
		// l, b from the plate solution of output/M42/20260723_180917; the nebula is about 400 pc away.
		x, y, z := galacticToCartesianPc(209.0723, -19.3833, 400)
		key, offset := nearestArmOffsetKpc(x, y, z)
		assert.Equal(t, "local", key)
		assert.Less(t, math.Abs(offset), 0.2, "comfortably within the arm")
		_, _, zk := heliocentricToGalactocentric(x, y, z)
		assert.Less(t, zk, 0.0, "Orion is below the galactic plane — a known fact about the region")
	})

	t.Run("the M51 field points well off the plane", func(t *testing.T) {
		// Its own stars reach 1841 pc, and the field points near the north galactic pole — which is why
		// naming an arm for it would be a confident answer to a question that does not apply.
		x, y, z := galacticToCartesianPc(104.8012, 68.5220, 1841)
		_, _, zk := heliocentricToGalactocentric(x, y, z)
		assert.Greater(t, zk, 1.5)
		assert.Greater(t, zk/thinScaleHeightKpc, 5.0, "five scale heights out of the disc")
	})
}

// The bug this pins: swept with the wrong pitch, a low-pitch arm closes into a circle and the map fills
// with concentric bands instead of spiral arms.
func TestArms_AreArcsNotRings(t *testing.T) {
	for _, a := range arms {
		require.Greater(t, a.betaMax, a.betaMin, "%s", a.key)
		assert.Less(t, a.betaMax-a.betaMin, 180.0,
			"%s: nothing is FITTED over more than half a turn; past that the locus is a continuation", a.key)

		// Across the range the sampler draws, the locus must run essentially one way. Not strictly: Reid
		// et al. fitted Norma's inner pitch at −1°, which really does open very slightly outward, so the
		// locus reverses there — by four tenths of a per cent over thirteen degrees, which is a property
		// of the published fit and not something to hide. What must not happen is a FOLD, an arm that
		// visibly turns back along itself.
		lo, hi := armSweep(a)
		var rising, falling float64
		prev := armLocus(a, lo)
		for b := lo + 1; b <= hi; b++ {
			r := armLocus(a, b)
			if d := r - prev; d > 0 {
				rising += d
			} else {
				falling -= d
			}
			prev = r
		}
		reversal := math.Min(rising, falling) / a.rKinkKpc
		assert.Less(t, reversal, 0.05,
			"%s folds back over %.1f%% of its radius", a.key, 100*reversal)
	}
}

func TestHeliocentricToGalactocentric_PlacesTheCentreAndAnticentre(t *testing.T) {
	// Looking toward l = 0 at exactly the Sun's radius lands on the centre.
	x, y, z := galacticToCartesianPc(0, 0, RSunKpc*1000)
	r, _, _ := heliocentricToGalactocentric(x, y, z)
	assert.InDelta(t, 0, r, 1e-6)

	// The anticentre is twice as far out on the SAME ray from the centre: azimuth is measured around the
	// centre, so it is "opposite" as seen from Earth, not as seen from the centre.
	x, y, z = galacticToCartesianPc(180, 0, RSunKpc*1000)
	r, beta, _ := heliocentricToGalactocentric(x, y, z)
	assert.InDelta(t, 2*RSunKpc, r, 1e-6)
	assert.InDelta(t, 0, beta, 1e-6)
}

// galacticToCartesianPc is the test's own l/b/distance to heliocentric galactic cartesian parsecs. The
// package never needs it — the cloud is generated in this frame — but a test that starts from a real
// field's galactic position does.
func galacticToCartesianPc(lDeg, bDeg, distPc float64) (x, y, z float64) {
	l, b := lDeg*degToRad, bDeg*degToRad
	return distPc * math.Cos(b) * math.Cos(l),
		distPc * math.Cos(b) * math.Sin(l),
		distPc * math.Sin(b)
}

func TestGalactocentric_RoundTrips(t *testing.T) {
	cases := []struct{ r, beta, z float64 }{
		{RSunKpc, 0, 0},
		{4.5, 137, 0.22},
		{12, -95, -1.4},
		{0.3, 200, 0.05},
	}
	for _, c := range cases {
		x, y, z := galactocentricToHeliocentric(c.r, c.beta, c.z)
		r, beta, zk := heliocentricToGalactocentric(x*1000, y*1000, z*1000)
		assert.InDelta(t, c.r, r, 1e-9)
		assert.InDelta(t, c.z, zk, 1e-9)
		// Azimuth is only defined modulo a turn, and only when the radius is not zero.
		assert.InDelta(t, 0, math.Mod(math.Abs(beta-c.beta), 360), 1e-6)
	}
}

// The Sun is above the plane, not in it — a small thing that silently biases an edge-on view if it is
// dropped.
func TestGalactocentricToHeliocentric_CarriesTheSunsHeight(t *testing.T) {
	_, _, z := galactocentricToHeliocentric(RSunKpc, 0, 0)
	assert.InDelta(t, -ZSunPc/1000, z, 1e-12, "the midplane is BELOW the Sun")
}

// Beyond the fit the arm is continued, not cut — but it must be visibly weaker there, or the picture
// would present an extrapolation as confidently as a measurement.
func TestArmWeight_FallsToTheContinuationOutsideTheFit(t *testing.T) {
	a := arms[0]
	assert.Equal(t, 1.0, armWeight(a, (a.betaMin+a.betaMax)/2))
	assert.Equal(t, 1.0, armWeight(a, a.betaMin))
	assert.Equal(t, 1.0, armWeight(a, a.betaMax))
	// Halfway down the ramp, halfway between the two weights.
	assert.InDelta(t, (1+armExtWeight)/2, armWeight(a, a.betaMax+armExtRampDeg/2.0), 1e-9)
	assert.InDelta(t, armExtWeight, armWeight(a, a.betaMax+armExtRampDeg), 1e-9)
	assert.InDelta(t, armExtWeight, armWeight(a, a.betaMin-armExtRampDeg-50), 1e-9)
	assert.Less(t, armExtWeight, 1.0, "the continuation must be weaker than the fit")
}

// The continuation has to reach all the way round, and it has to stay a spiral doing it. Norma's inner
// pitch of −1° is the trap: continued with that, its "spiral" closes into a circle.
func TestArmSweep_ContinuesRoundTheDiscAsASpiral(t *testing.T) {
	for _, a := range arms {
		lo, hi := armSweep(a)
		assert.LessOrEqual(t, lo, a.betaMin, "%s must reach at least its measured range", a.key)
		assert.GreaterOrEqual(t, hi, a.betaMax)
		assert.Greater(t, hi-lo, 180.0,
			"%s is drawn over only %.0f° — an arm over a third of the Galaxy reads as broken", a.key, hi-lo)

		// Across the whole sweep the radius must change by a real factor, which is what tells a spiral
		// from a ring.
		rLo, rHi := armLocus(a, lo), armLocus(a, hi)
		ratio := math.Max(rLo, rHi) / math.Min(rLo, rHi)
		assert.Greater(t, ratio, 2.0, "%s spans a radius ratio of only %.2f — that is a ring", a.key, ratio)

		// And it must stay inside the drawn disc at both ends.
		for _, r := range []float64{rLo, rHi} {
			assert.GreaterOrEqual(t, r, armInnerCutKpc-1e-9, "%s", a.key)
			assert.LessOrEqual(t, r, DiscEdgeKpc+1e-9, "%s", a.key)
		}
	}
}

// The continuation must not introduce a step: the locus is one continuous curve, whatever the pitch
// does at the boundaries of the fit.
func TestArmLocus_IsContinuousAtEverySegmentBoundary(t *testing.T) {
	const eps = 1e-7
	for _, a := range arms {
		for _, b := range []float64{a.betaMin, a.betaKink, a.betaMax} {
			assert.InDelta(t, armLocus(a, b-eps), armLocus(a, b+eps), 1e-6,
				"%s steps at beta %.0f", a.key, b)
		}
	}
}

func TestDiscEdgeFade_IsOneInsideAndZeroOutside(t *testing.T) {
	assert.Equal(t, 1.0, discEdgeFade(0))
	assert.Equal(t, 1.0, discEdgeFade(DiscEdgeKpc-DiscFadeKpc))
	assert.Equal(t, 0.0, discEdgeFade(DiscEdgeKpc))
	assert.Equal(t, 0.0, discEdgeFade(DiscEdgeKpc+5))
	assert.InDelta(t, 0.5, discEdgeFade(DiscEdgeKpc-DiscFadeKpc/2), 1e-9)
}
