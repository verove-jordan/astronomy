package polaralign

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// bench is a synthetic mount for the adjust loop: a polar axis, a telescope pointed somewhere, and the
// ability to turn the bolts and let time pass, producing the plate solution a camera would have.
type bench struct {
	site     Site
	axis     hVec3 // the polar axis right now
	centre   hVec3 // where the telescope points right now, over the ground
	at       time.Time
	tracking bool
	scale    float64
	w, h     int
}

func newBench(site Site, altErrDeg, azKnobDeg, coneDeg float64, tracking bool) *bench {
	a := axisAt(site, altErrDeg, azKnobDeg).vec()
	return &bench{
		site: site, axis: a,
		centre:   rotateAboutH(a, perpAxis(a), coneDeg),
		at:       testEpoch,
		tracking: tracking,
		scale:    1.06, w: 4656, h: 3520,
	}
}

// turnBolts applies an adjustment: the whole mount rotates, carrying both the axis and the telescope.
// It takes the rotation itself rather than a pair of errors, because the two are NOT the same thing —
// the altitude stage has to turn the opposite way in the southern hemisphere, where the pole is behind
// you and rotateEast lowers what it raises in the north.
func (b *bench) turnBolts(r rot) {
	b.axis = r.apply(b.axis)
	b.centre = r.apply(b.centre)
}

// wait lets time pass. With the drive running the telescope is carried round the mount's own axis, so
// it holds its place in the sky; with it stopped the sky slides past.
//
// The axis has to be taken NORTH-END-FIRST here. The sky turns one way for everybody — its rotation
// vector points at the south celestial pole wherever you stand — so using the mount's axis as it points
// (at the south pole, below the equator) tracks backwards at twice the rate.
func (b *bench) wait(d time.Duration) {
	if b.tracking {
		b.centre = rotateAboutH(b.centre, b.northAxis(), -siderealDegPerSec*d.Seconds())
	}
	b.at = b.at.Add(d)
}

func (b *bench) northAxis() hVec3 {
	if b.site.LatDeg < 0 {
		return hVec3{N: -b.axis.N, E: -b.axis.E, U: -b.axis.U}
	}
	return b.axis
}

// frame is the plate solution a camera bolted to this mount would produce.
func (b *bench) frame(t *testing.T) Frame {
	t.Helper()
	ra, dec := skyFromDir(b.centre, b.site, b.at, FitOptions{})
	s := b.scale / 3600
	wcs, ok := fits.NewTanWCS(ra, dec, float64(b.w)/2+1, float64(b.h)/2+1,
		[2][2]float64{{-s, 0}, {0, s}})
	require.True(t, ok)
	return Frame{WCS: wcs, WidthPx: b.w, HeightPx: b.h, At: b.at}
}

// measure runs the real fit over a synthetic RA sweep of this bench, so the adjust tests start from
// the same Correction the session would.
func (b *bench) measure(t *testing.T) Correction {
	t.Helper()
	samples := make([]Sample, 4)
	for i := range samples {
		at := b.at.Add(time.Duration(i*120) * time.Second)
		dir := rotateAboutH(b.centre, b.axis, float64(i)*20)
		ra, dec := skyFromDir(dir, b.site, at, FitOptions{})
		samples[i] = Sample{RADeg: ra, DecDeg: dec, At: at}
	}
	a, err := FitAxis(samples, b.site, FitOptions{})
	require.NoError(t, err)
	return Correct(a, b.site)
}

// The property the whole live loop rests on: the target is a fixed point of sky. Turning the bolts
// moves the telescope AND the axis, and the place the centre has to end up does not move at all.
func TestLive_TargetIsFixedWhileTheBoltsTurn(t *testing.T) {
	b := newBench(Site{48.8566, 2.3522}, 0.6, -0.4, 70, true)
	c := b.measure(t)

	live, err := NewLive(c, b.frame(t), true, FitOptions{})
	require.NoError(t, err)

	first, err := live.Update(b.frame(t))
	require.NoError(t, err)

	// Walk the user in one stage at a time, the way anyone actually does it: some altitude, then some
	// azimuth, with time passing in between.
	full := c.rotation()
	for _, step := range []rot{
		{tiltDeg: full.tiltDeg / 3}, {azDeg: full.azDeg / 3},
		{tiltDeg: full.tiltDeg / 3}, {azDeg: full.azDeg / 3},
		{tiltDeg: full.tiltDeg / 3, azDeg: full.azDeg / 3},
	} {
		b.turnBolts(step)
		b.wait(20 * time.Second)

		got, err := live.Update(b.frame(t))
		require.NoError(t, err)
		assert.Less(t, astro.AngularSeparation(
			first.Target.RADeg, first.Target.DecDeg, got.Target.RADeg, got.Target.DecDeg)*3600,
			1.0, "the target must not move as the user works")
		assert.False(t, got.Suspect)
	}

	// And having turned very nearly the whole correction, the marker is on the crosshairs.
	final, err := live.Update(b.frame(t))
	require.NoError(t, err)
	assert.Less(t, final.RemainingArcmin, 3.0,
		"after turning the bolts as instructed the remaining error should be small")
	assert.Less(t, final.Target.OffsetPx, first.Target.OffsetPx/5)
}

// Turning exactly the correction that was reported must finish the job.
func TestLive_TurningTheFullCorrectionAligns(t *testing.T) {
	for _, site := range []Site{{48.8566, 2.3522}, {-33.87, 151.2}} {
		for _, tracking := range []bool{true, false} {
			b := newBench(site, 0.5, -0.35, 65, tracking)
			c := b.measure(t)
			live, err := NewLive(c, b.frame(t), tracking, FitOptions{})
			require.NoError(t, err)

			b.turnBolts(c.rotation())
			b.wait(45 * time.Second)

			got, err := live.Update(b.frame(t))
			require.NoError(t, err)
			assert.Less(t, got.RemainingArcmin, 0.5,
				"lat %g tracking %v: %.2f′ left after the full correction", site.LatDeg, tracking, got.RemainingArcmin)
			assert.Equal(t, QualityExcellent, got.Quality)
			assert.Less(t, got.Target.OffsetPx, 30.0)
		}
	}
}

// With the drive stopped the sky slides past the telescope at fifteen arcseconds a second. The target
// is fixed over the GROUND then, not over the sky, and forgetting that would make the marker appear to
// crawl across the frame while nobody touched anything.
func TestLive_UntrackedSkyDriftDoesNotMoveTheTarget(t *testing.T) {
	b := newBench(Site{48.8566, 2.3522}, 0.5, -0.3, 60, false)
	c := b.measure(t)
	live, err := NewLive(c, b.frame(t), false, FitOptions{})
	require.NoError(t, err)

	first, err := live.Update(b.frame(t))
	require.NoError(t, err)

	b.wait(3 * time.Minute) // 45′ of sky slides by
	drifted, err := live.Update(b.frame(t))
	require.NoError(t, err)

	// Nothing was adjusted, so nothing about the alignment changed.
	assert.InDelta(t, first.RemainingArcmin, drifted.RemainingArcmin, 0.2,
		"drifting past the target is not progress")
	// The target has moved across the SKY, because it is pinned to the ground.
	assert.Greater(t, astro.AngularSeparation(
		first.Target.RADeg, first.Target.DecDeg, drifted.Target.RADeg, drifted.Target.DecDeg)*60,
		30.0)
	// And it stays in nearly the same place in the FRAME, because the frame drifted with it.
	assert.InDelta(t, first.Target.OffsetPx, drifted.Target.OffsetPx, first.Target.OffsetPx*0.05)
}

// A kicked tripod or an accidental slew makes the measurement stale, and a marker computed from a
// stale measurement points somewhere arbitrary. Say so rather than lead the user astray.
func TestLive_FlagsAMountThatMoved(t *testing.T) {
	b := newBench(Site{48.8566, 2.3522}, 0.5, -0.3, 60, true)
	c := b.measure(t)
	live, err := NewLive(c, b.frame(t), true, FitOptions{})
	require.NoError(t, err)

	_, err = live.Update(b.frame(t))
	require.NoError(t, err)

	// A five-degree lurch: no hand on a bolt does that.
	b.centre = rotateAboutH(b.centre, perpAxis(b.centre), 5)
	got, err := live.Update(b.frame(t))
	require.NoError(t, err)
	assert.True(t, got.Suspect)
}

// Turning a bolt the WRONG way has to read as getting worse, not as noise.
func TestLive_WrongWayIncreasesTheRemainingError(t *testing.T) {
	b := newBench(Site{48.8566, 2.3522}, 0.5, 0, 65, true)
	c := b.measure(t)
	live, err := NewLive(c, b.frame(t), true, FitOptions{})
	require.NoError(t, err)

	before, err := live.Update(b.frame(t))
	require.NoError(t, err)

	// The report said lower the axis; raise it instead.
	b.turnBolts(rot{tiltDeg: -c.rotation().tiltDeg})
	after, err := live.Update(b.frame(t))
	require.NoError(t, err)

	assert.Greater(t, after.RemainingArcmin, before.RemainingArcmin*1.2)
	assert.Greater(t, after.Target.OffsetPx, before.Target.OffsetPx)
}

// The remaining-error estimate is a scaling, so it has to be right at both ends and monotone between.
func TestLive_RemainingErrorTracksTheTurn(t *testing.T) {
	b := newBench(Site{48.8566, 2.3522}, 0.8, -0.6, 70, true)
	c := b.measure(t)
	live, err := NewLive(c, b.frame(t), true, FitOptions{})
	require.NoError(t, err)

	start, err := live.Update(b.frame(t))
	require.NoError(t, err)
	assert.InDelta(t, c.TotalArcmin, start.RemainingArcmin, 0.05, "it starts at the measured error")

	prev := start.RemainingArcmin
	for i := 0; i < 4; i++ {
		b.turnBolts(c.rotation().scaled(0.25))
		got, err := live.Update(b.frame(t))
		require.NoError(t, err)
		assert.Less(t, got.RemainingArcmin, prev, "step %d did not improve", i+1)
		prev = got.RemainingArcmin
	}
	assert.Less(t, prev, 0.5, "it ends at zero")
}

// The local copy of the sidereal rate must not drift from the one the hardware layer uses.
func TestLive_SiderealRateMatchesTheHardwareLayer(t *testing.T) {
	assert.Equal(t, device.SiderealArcsecPerSec/3600, siderealDegPerSec)
	// And a sanity check on the number itself: a sidereal day is a touch under 24 hours.
	assert.InDelta(t, 86164.1, 360/siderealDegPerSec, 1.0)
}
