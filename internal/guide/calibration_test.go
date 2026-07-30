package guide

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// squareCal is a clean synthetic mapping: RA along +x, Dec along +y, 2″ per pixel on both axes,
// calibrated on the celestial equator.
func squareCal() Calibration {
	return Calibration{
		RAArcsecPerPx: 2, DecArcsecPerPx: 2,
		RAUnitX: 1, RAUnitY: 0,
		DecUnitX: 0, DecUnitY: 1,
		DecAtCalibDeg: 0,
		Orthogonality: 1,
		RADriftPx:     10, DecDriftPx: 10,
	}
}

// observe generates what a calibration run would have measured, given a true mapping: a commanded
// rotation of cmd arcseconds displaces the star cmd/scale pixels along that axis's direction.
func observe(c Calibration, axis AxisID, cmd float64) CalibObservation {
	ux, uy, scale := c.RAUnitX, c.RAUnitY, c.RAArcsecPerPx
	if axis == AxisDec {
		ux, uy, scale = c.DecUnitX, c.DecUnitY, c.DecArcsecPerPx
	}
	px := cmd / scale
	return CalibObservation{Axis: axis, CommandArcsec: cmd, DX: px * ux, DY: px * uy}
}

func obsFor(c Calibration, cmds ...float64) []CalibObservation {
	var out []CalibObservation
	for _, axis := range []AxisID{AxisRA, AxisDec} {
		for _, cmd := range cmds {
			out = append(out, observe(c, axis, cmd))
		}
	}
	return out
}

func TestFitCalibration_RecoversAKnownMapping(t *testing.T) {
	want := squareCal()

	got, err := FitCalibration(obsFor(want, 10, 20, 30), 0)
	require.NoError(t, err)

	assert.InDelta(t, want.RAArcsecPerPx, got.RAArcsecPerPx, 1e-9)
	assert.InDelta(t, want.DecArcsecPerPx, got.DecArcsecPerPx, 1e-9)
	assert.InDelta(t, 1, got.RAUnitX, 1e-9)
	assert.InDelta(t, 0, got.RAUnitY, 1e-9)
	assert.InDelta(t, 0, got.DecUnitX, 1e-9)
	assert.InDelta(t, 1, got.DecUnitY, 1e-9)
	assert.InDelta(t, 1, got.Orthogonality, 1e-9)
	assert.True(t, got.Valid())
}

func TestFitCalibration_RecoversARotatedMapping(t *testing.T) {
	// A camera rotated 30° in the focal plane. Nothing downstream should care, as long as the fit
	// finds the rotation rather than assuming the axes lie along the pixel grid.
	const rot = 30 * math.Pi / 180
	want := Calibration{
		RAArcsecPerPx: 3, DecArcsecPerPx: 3,
		RAUnitX: math.Cos(rot), RAUnitY: math.Sin(rot),
		DecUnitX: -math.Sin(rot), DecUnitY: math.Cos(rot),
	}

	got, err := FitCalibration(obsFor(want, 15, 30), 0)
	require.NoError(t, err)

	assert.InDelta(t, 3, got.RAArcsecPerPx, 1e-9)
	assert.InDelta(t, want.RAUnitX, got.RAUnitX, 1e-9)
	assert.InDelta(t, want.RAUnitY, got.RAUnitY, 1e-9)
	assert.InDelta(t, 1, got.Orthogonality, 1e-9)
}

func TestFitCalibration_RejectsTinyMovement(t *testing.T) {
	c := squareCal()
	// 4 arcseconds at 2″/px is 2 pixels: below the floor, where centroid noise is a large fraction of
	// the signal and the fitted scale is confidently wrong.
	_, err := FitCalibration(obsFor(c, 2, 4), 0)
	require.ErrorIs(t, err, ErrCalibrationTooSmall)
}

func TestFitCalibration_RejectsParallelAxes(t *testing.T) {
	// A declination axis that barely differs in direction from RA. This is what an axis that never
	// actually moved looks like, and inverting it would amplify a small offset into an enormous one.
	nearlyParallel := Calibration{
		RAArcsecPerPx: 2, DecArcsecPerPx: 2,
		RAUnitX: 1, RAUnitY: 0,
		DecUnitX: 0.995, DecUnitY: 0.0998,
	}

	_, err := FitCalibration(obsFor(nearlyParallel, 20, 40), 0)
	require.ErrorIs(t, err, ErrCalibrationDegenerate)
}

func TestFitCalibration_NeedsTwoMovedSamplesPerAxis(t *testing.T) {
	c := squareCal()
	one := []CalibObservation{observe(c, AxisRA, 30), observe(c, AxisDec, 30)}

	_, err := FitCalibration(one, 0)
	require.ErrorIs(t, err, ErrCalibrationTooSmall)
}

func TestCalibration_AxesInvertsTheForwardMapping(t *testing.T) {
	tests := []struct {
		name            string
		dx, dy          float64
		wantRA, wantDec float64
	}{
		{"pure x", 1, 0, 2, 0},
		{"pure y", 0, 1, 0, 2},
		{"both", 3, -4, 6, -8},
		{"origin", 0, 0, 0, 0},
	}
	c := squareCal()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ra, dec, ok := c.Axes(tt.dx, tt.dy)
			require.True(t, ok)
			assert.InDelta(t, tt.wantRA, ra, 1e-9)
			assert.InDelta(t, tt.wantDec, dec, 1e-9)
		})
	}
}

func TestCalibration_AxesSeparatesNonPerpendicularAxes(t *testing.T) {
	// The axes are 30° from square, which real mounts are. A pure declination move must come back as
	// declination error and nothing else.
	//
	// This is the whole reason Axes solves the 2×2 system instead of projecting onto each axis: a
	// projection would report a large spurious RA error here, and the servo would faithfully correct
	// for something that never happened.
	c := Calibration{
		RAArcsecPerPx: 2, DecArcsecPerPx: 2,
		RAUnitX: 1, RAUnitY: 0,
		DecUnitX: 0.5, DecUnitY: math.Sqrt(3) / 2,
	}
	c.Orthogonality = math.Abs(c.RAUnitX*c.DecUnitY - c.RAUnitY*c.DecUnitX)
	require.True(t, c.Valid())

	// 20″ of declination rotation alone.
	const cmd = 20.0
	o := observe(c, AxisDec, cmd)

	ra, dec, ok := c.Axes(o.DX, o.DY)
	require.True(t, ok)
	assert.InDelta(t, 0, ra, 1e-9, "a pure Dec move must not leak into the RA channel")
	assert.InDelta(t, cmd, dec, 1e-9)

	// And the naive projection this replaced would indeed have been badly wrong, which is what makes
	// the assertion above worth making.
	projected := c.RAArcsecPerPx * (o.DX*c.RAUnitX + o.DY*c.RAUnitY)
	assert.InDelta(t, 10, projected, 1e-9)
}

func TestCalibration_AtDecScalesRightAscensionOnly(t *testing.T) {
	c := squareCal()

	at60 := c.AtDec(60)
	assert.InDelta(t, 4, at60.RAArcsecPerPx, 1e-9,
		"the same axis rotation sweeps half as much sky at 60°, so a pixel is worth twice the rotation")
	assert.InDelta(t, 2, at60.DecArcsecPerPx, 1e-9, "the declination axis is unaffected by declination")

	assert.InDelta(t, 2, c.AtDec(0).RAArcsecPerPx, 1e-9, "no change at the calibration declination")
}

func TestCalibration_AtDecIsCappedNearThePole(t *testing.T) {
	c := squareCal()

	near := c.AtDec(89.9)
	assert.InDelta(t, 2*maxCosDecBoost, near.RAArcsecPerPx, 1e-9,
		"cos(dec) heads for zero at the pole; uncapped this would turn a pixel into an unbounded command")

	// The cap applies in both directions, so a calibration taken near the pole and used at the equator
	// is bounded too.
	polar := squareCal()
	polar.DecAtCalibDeg = 89.9
	assert.InDelta(t, 2/maxCosDecBoost, polar.AtDec(0).RAArcsecPerPx, 1e-9)
}

func TestCalibration_ValidRejectsAnUnfittedMapping(t *testing.T) {
	assert.False(t, Calibration{}.Valid())

	degenerate := squareCal()
	degenerate.Orthogonality = 0.01
	assert.False(t, degenerate.Valid())

	noScale := squareCal()
	noScale.RAArcsecPerPx = 0
	assert.False(t, noScale.Valid())
}

func TestCalibration_AxesRefusesADegenerateMapping(t *testing.T) {
	_, _, ok := Calibration{}.Axes(1, 1)
	assert.False(t, ok, "an unfitted calibration must refuse rather than return a plausible number")
}
