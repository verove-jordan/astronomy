package polaralign

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fit is tested against a forward model rather than against fixtures: plant a polar axis, sweep a
// synthetic telescope around it, hand the resulting plate solves back, and require the original axis
// to come out. That is the only kind of test that can catch a sign error, because a sign error still
// produces a beautifully self-consistent wrong answer.

var testEpoch = time.Date(2026, 8, 4, 22, 30, 0, 0, time.UTC)

// sweep builds the samples a telescope would produce turning about a misaligned polar axis.
//
// altErr/azErr are the misalignment in DEGREES, radius is how far the tube sits off the polar axis
// (i.e. roughly 90° − declination), and the mount is turned by arcDeg in total across n frames.
func sweep(site Site, altErrDeg, azErrDeg, radiusDeg, arcDeg float64, n int, opt FitOptions) ([]Sample, hVec3) {
	axis := horizonVec(math.Abs(site.LatDeg)+altErrDeg, poleAzimuth(site.LatDeg)+azErrDeg)

	// Start the tube one radius away from the axis, offset toward the east so the circle is not
	// degenerate with the meridian plane.
	start := rotateAboutH(axis, perpAxis(axis), radiusDeg)

	samples := make([]Sample, n)
	for i := range samples {
		frac := 0.0
		if n > 1 {
			frac = float64(i) / float64(n-1)
		}
		at := testEpoch.Add(time.Duration(frac*600) * time.Second)
		dir := rotateAboutH(start, axis, arcDeg*frac)
		ra, dec := skyFromDir(dir, site, at, opt)
		samples[i] = Sample{RADeg: ra, DecDeg: dec, At: at}
	}
	return samples, axis
}

// perpAxis returns some unit vector perpendicular to v, for tilting off it.
func perpAxis(v hVec3) hVec3 {
	seed := hVec3{U: 1}
	if math.Abs(v.U) > 0.9 {
		seed = hVec3{N: 1}
	}
	p, _ := v.cross(seed).unit()
	return p
}

func TestFitAxis_RecoversKnownAxis(t *testing.T) {
	cases := []struct {
		name             string
		site             Site
		altErr, azErr    float64
		radius, arc      float64
		opt              FitOptions
		toleranceArcsec  float64
		wantRadiusApprox float64
	}{
		{"paris, half a degree out", Site{48.8566, 2.3522}, 0.5, -0.3, 70, 30, FitOptions{}, 5, 70},
		{"paris, no refraction model", Site{48.8566, 2.3522}, 0.5, -0.3, 70, 30, FitOptions{NoRefraction: true}, 5, 70},
		{"tiny error", Site{48.8566, 2.3522}, 0.01, 0.02, 65, 25, FitOptions{}, 5, 65},
		{"big error", Site{48.8566, 2.3522}, -2.5, 3.0, 60, 40, FitOptions{}, 5, 60},
		{"southern hemisphere", Site{-33.87, 151.2}, -0.4, 0.6, 55, 30, FitOptions{}, 5, 55},
		{"near the equator", Site{4.5, -74.1}, 0.3, -0.2, 50, 35, FitOptions{}, 5, 50},
		{"short but legal arc", Site{48.8566, 2.3522}, 0.4, 0.4, 70, 8, FitOptions{}, 5, 70},
		{"many samples", Site{60.1, 24.9}, 0.2, -0.7, 45, 30, FitOptions{}, 5, 45},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := 3
			if c.name == "many samples" {
				n = 7
			}
			samples, want := sweep(c.site, c.altErr, c.azErr, c.radius, c.arc, n, c.opt)

			got, err := FitAxis(samples, c.site, c.opt)
			require.NoError(t, err)

			off := angleBetween(want, got.vec()) * 3600
			assert.Less(t, off, c.toleranceArcsec, "recovered axis is %.1f″ from the planted one", off)
			assert.InDelta(t, c.wantRadiusApprox, got.RadiusDeg, 0.5)
			assert.InDelta(t, c.arc, got.ArcDeg, 0.5)
			assert.Less(t, got.ResidualArcsec, 1.0)
			assert.Equal(t, n, got.Samples)
		})
	}
}

// A weak arc still fits, but must say so — the number is real and its precision is not.
func TestFitAxis_Warnings(t *testing.T) {
	site := Site{48.8566, 2.3522}

	weak, _ := sweep(site, 0.5, 0.5, 70, 8, 3, FitOptions{})
	got, err := FitAxis(weak, site, FitOptions{})
	require.NoError(t, err)
	assert.Contains(t, got.Warnings, WarnWeakArc)
	assert.Contains(t, got.Warnings, WarnNoResidual, "three frames check nothing")

	strong, _ := sweep(site, 0.5, 0.5, 70, 60, 4, FitOptions{})
	got, err = FitAxis(strong, site, FitOptions{})
	require.NoError(t, err)
	assert.Empty(t, got.Warnings)

	tight, _ := sweep(site, 0.5, 0.5, 20, 60, 4, FitOptions{})
	got, err = FitAxis(tight, site, FitOptions{})
	require.NoError(t, err)
	assert.Contains(t, got.Warnings, WarnNearPole)
}

// The residual is the only thing standing between a knocked tripod and a confident wrong answer — and
// it can only do that job from four frames on, because three points lie on exactly one circle no
// matter where they are.
func TestFitAxis_RejectsAFrameThatDoesNotBelong(t *testing.T) {
	site := Site{48.8566, 2.3522}

	samples, _ := sweep(site, 0.5, -0.4, 70, 60, 5, FitOptions{})
	samples[2].DecDeg += 0.2 // the declination axis crept, or something was bumped

	_, err := FitAxis(samples, site, FitOptions{})
	assert.ErrorIs(t, err, ErrInconsistent)

	// The same corruption in a three-frame set cannot be detected, and the fit says so rather than
	// pretending otherwise.
	three, _ := sweep(site, 0.5, -0.4, 70, 60, 3, FitOptions{})
	three[1].DecDeg += 0.2
	got, err := FitAxis(three, site, FitOptions{})
	require.NoError(t, err)
	assert.Contains(t, got.Warnings, WarnNoResidual)
	assert.InDelta(t, 0, got.ResidualArcsec, 1e-6, "three points always fit a circle exactly")
}

func TestFitAxis_Guards(t *testing.T) {
	site := Site{48.8566, 2.3522}

	two, _ := sweep(site, 0.5, 0.5, 70, 30, 2, FitOptions{})
	_, err := FitAxis(two, site, FitOptions{})
	assert.ErrorIs(t, err, ErrTooFewSamples)

	barely, _ := sweep(site, 0.5, 0.5, 70, 1, 3, FitOptions{})
	_, err = FitAxis(barely, site, FitOptions{})
	assert.ErrorIs(t, err, ErrArcTooSmall)

	atPole, _ := sweep(site, 0.5, 0.5, 1, 30, 3, FitOptions{})
	_, err = FitAxis(atPole, site, FitOptions{})
	assert.ErrorIs(t, err, ErrDegenerate, "a field on the axis traces no circle")

	same, _ := sweep(site, 0.5, 0.5, 70, 30, 3, FitOptions{})
	same[1], same[2] = same[0], same[0]
	_, err = FitAxis(same, site, FitOptions{})
	assert.ErrorIs(t, err, ErrDegenerate, "three identical frames determine nothing")
}

// noisyFit runs the forward model with plate-solve noise and reports the RMS and 95th-percentile axis
// error in arcminutes.
func noisyFit(t *testing.T, site Site, arcDeg, sigmaArcsec float64, n, trials int) (rms, p95 float64) {
	t.Helper()
	rng := rand.New(rand.NewSource(20260804))
	errs := make([]float64, 0, trials)
	for trial := 0; trial < trials; trial++ {
		samples, want := sweep(site, 0.5, -0.4, 70, arcDeg, n, FitOptions{})
		for i := range samples {
			samples[i].DecDeg += rng.NormFloat64() * sigmaArcsec / 3600
			samples[i].RADeg += rng.NormFloat64() * sigmaArcsec / 3600 /
				math.Cos(samples[i].DecDeg*deg2rad)
		}
		got, err := FitAxis(samples, site, FitOptions{})
		require.NoError(t, err)
		errs = append(errs, angleBetween(want, got.vec())*60)
	}
	sort.Float64s(errs)
	var sum float64
	for _, e := range errs {
		sum += e * e
	}
	return math.Sqrt(sum / float64(len(errs))), errs[len(errs)*95/100]
}

// The recommendation the UI gives — turn the axis about sixty degrees — is a measurement, not a guess,
// and this is the measurement. At 60° a one-arcsecond solve lands the axis inside an arcminute
// essentially always; at the 30° that feels sufficient it does not.
func TestFitAxis_SixtyDegreesIsWhatSubArcminuteCosts(t *testing.T) {
	site := Site{48.8566, 2.3522}

	rms, p95 := noisyFit(t, site, wantArcDeg, 1, 3, 400)
	assert.Less(t, p95, 0.5, "at %g° the 95th percentile was %.2f′ (rms %.2f′)", float64(wantArcDeg), p95, rms)

	// And the reason the number is 60 and not 30: the same solve noise over half the arc is four times
	// worse, so 30° leaves a one-in-twenty chance of missing the arcminute we are chasing.
	_, p95Short := noisyFit(t, site, 30, 1, 3, 400)
	assert.Greater(t, p95Short, 1.0, "30° should NOT reach an arcminute at the 95th percentile")
}

// Why the UI asks for a longer turn rather than more frames: the error falls as the SQUARE of the arc,
// so doubling the rotation is worth four times as much as any number of extra exposures.
func TestFitAxis_ErrorScalesWithTheSquareOfTheArc(t *testing.T) {
	site := Site{48.8566, 2.3522}
	short, _ := noisyFit(t, site, 30, 1, 3, 400)
	long, _ := noisyFit(t, site, 60, 1, 3, 400)
	assert.InDelta(t, 4.0, short/long, 0.6, "doubling the arc should cut the error about fourfold")

	// Doubling the FRAMES over the same arc is worth about a fifth, against the fourfold above: the two
	// ENDS carry nearly all the curvature information, so the middle ones are almost free of it.
	three, _ := noisyFit(t, site, 30, 1, 3, 400)
	six, _ := noisyFit(t, site, 30, 1, 6, 400)
	assert.Greater(t, six/three, 0.7, "more frames over the same arc barely help")
	assert.Less(t, six/three, 1.0)
}

// SigmaArcsec is what the UI shows to justify trusting the result, so it has to be honest: the scatter
// of the real thing must match what the fit predicted from its own geometry.
func TestFitAxis_SigmaMatchesTheActualScatter(t *testing.T) {
	site := Site{48.8566, 2.3522}
	for _, arc := range []float64{30, 60} {
		samples, _ := sweep(site, 0.5, -0.4, 70, arc, 3, FitOptions{})
		clean, err := FitAxis(samples, site, FitOptions{})
		require.NoError(t, err)

		rms, _ := noisyFit(t, site, arc, assumedSolveArcsec, 3, 400)
		// The reported sigma covers the WORST-constrained direction, so the true scatter — which mixes
		// that with a much better direction — must sit below it but within a factor of two.
		predicted := clean.SigmaArcsec / 60
		assert.Less(t, rms, predicted*1.2, "arc %g: measured %.3f′ vs predicted %.3f′", arc, rms, predicted)
		assert.Greater(t, rms, predicted*0.5, "arc %g: measured %.3f′ vs predicted %.3f′", arc, rms, predicted)
	}
}

// sampleDir and skyFromDir are used in opposite directions by the fit and by the target marker, so a
// mismatch would be invisible in either one alone.
func TestSampleDir_RoundTripsThroughSky(t *testing.T) {
	site := Site{48.8566, 2.3522}
	for _, opt := range []FitOptions{{}, {NoRefraction: true}} {
		for _, dir := range []hVec3{
			horizonVec(49, 0), horizonVec(30, 120), horizonVec(70, 250), horizonVec(12, 300),
		} {
			ra, dec := skyFromDir(dir, site, testEpoch, opt)
			back := sampleDir(Sample{RADeg: ra, DecDeg: dec, At: testEpoch}, site, opt)
			assert.Less(t, angleBetween(dir, back)*3600, 0.01,
				"round trip lost %.4f″", angleBetween(dir, back)*3600)
		}
	}
}

// Refraction is not decoration: leaving it out has to visibly move the answer, or the option is a lie.
func TestRefraction_ShiftsTheAnswer(t *testing.T) {
	site := Site{48.8566, 2.3522}
	// Build the samples WITH refraction, then fit them both ways.
	samples, want := sweep(site, 0.5, -0.3, 70, 30, 3, FitOptions{})

	with, err := FitAxis(samples, site, FitOptions{})
	require.NoError(t, err)
	without, err := FitAxis(samples, site, FitOptions{NoRefraction: true})
	require.NoError(t, err)

	assert.Less(t, angleBetween(want, with.vec())*3600, 5.0)
	assert.Greater(t, angleBetween(want, without.vec())*60, 0.2,
		"ignoring refraction should cost a visible fraction of an arcminute")
}

func TestJacobiEigen3_DiagonalizesASymmetricMatrix(t *testing.T) {
	m := [3][3]float64{{4, 1, -2}, {1, 2, 0}, {-2, 0, 3}}
	vals, vecs := jacobiEigen3(m)
	for k := 0; k < 3; k++ {
		v := [3]float64{vecs[0][k], vecs[1][k], vecs[2][k]}
		for i := 0; i < 3; i++ {
			var mv float64
			for j := 0; j < 3; j++ {
				mv += m[i][j] * v[j]
			}
			assert.InDelta(t, vals[k]*v[i], mv, 1e-9, "eigenpair %d row %d", k, i)
		}
	}
}
