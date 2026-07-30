package guide

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainAxis is a servo with every softening feature switched off, so a test can assert on one policy
// at a time instead of on their combination.
func plainAxis(t *testing.T) *Axis {
	t.Helper()
	return NewAxis(AxisRA, AxisConfig{
		Aggressiveness: 1,
		MinMoveArcsec:  0.1,
		MaxMoveArcsec:  100,
	})
}

func TestAxis_CorrectsOppositeTheError(t *testing.T) {
	a := plainAxis(t)

	corr, why := a.Next(3)
	assert.Equal(t, WhyApplied, why)
	assert.InDelta(t, -3, corr, 1e-9, "a positive error must be removed by a negative rotation")

	a.Reset()
	corr, _ = a.Next(-3)
	assert.InDelta(t, 3, corr, 1e-9)
}

func TestAxis_AppliesAggressiveness(t *testing.T) {
	tests := []struct {
		name           string
		aggressiveness float64
		err            float64
		want           float64
	}{
		{"full", 1.0, 4, -4},
		{"default-ish", 0.5, 4, -2},
		{"gentle", 0.25, 4, -1},
		{"negative error", 0.5, -4, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAxis(AxisRA, AxisConfig{
				Aggressiveness: tt.aggressiveness,
				MinMoveArcsec:  0.1,
				MaxMoveArcsec:  100,
			})
			corr, why := a.Next(tt.err)
			assert.Equal(t, WhyApplied, why)
			assert.InDelta(t, tt.want, corr, 1e-9)
		})
	}
}

func TestAxis_DeadbandWithholdsSmallErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      float64
		wantZero bool
	}{
		{"well inside", 0.05, true},
		{"just inside", 0.49, true},
		{"just outside", 0.51, false},
		{"negative inside", -0.2, true},
		{"negative outside", -2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAxis(AxisRA, AxisConfig{
				Aggressiveness: 1, MinMoveArcsec: 0.5, MaxMoveArcsec: 100,
			})
			corr, why := a.Next(tt.err)
			if tt.wantZero {
				assert.Zero(t, corr)
				assert.Equal(t, WhyDeadband, why)
				return
			}
			assert.NotZero(t, corr)
			assert.Equal(t, WhyApplied, why)
		})
	}
}

func TestAxis_ClampsLargeCorrections(t *testing.T) {
	a := NewAxis(AxisRA, AxisConfig{Aggressiveness: 1, MinMoveArcsec: 0.1, MaxMoveArcsec: 5})

	corr, why := a.Next(50)
	assert.Equal(t, WhyClamped, why)
	assert.InDelta(t, -5, corr, 1e-9, "one bad centroid must not be able to throw the mount")

	corr, why = a.Next(-50)
	assert.Equal(t, WhyClamped, why)
	assert.InDelta(t, 5, corr, 1e-9)
}

func TestAxis_HysteresisSmoothsSuccessiveCorrections(t *testing.T) {
	a := NewAxis(AxisRA, AxisConfig{
		Aggressiveness: 1, Hysteresis: 0.5, MinMoveArcsec: 0.1, MaxMoveArcsec: 100,
	})

	// Half of the fresh correction, half of the previous one — so a constant error converges on the
	// full correction rather than jumping to it.
	first, _ := a.Next(2)
	assert.InDelta(t, -1, first, 1e-9)
	second, _ := a.Next(2)
	assert.InDelta(t, -1.5, second, 1e-9)
	third, _ := a.Next(2)
	assert.InDelta(t, -1.75, third, 1e-9)

	assert.Less(t, third, second, "corrections must approach the full value, not overshoot it")
	assert.Greater(t, third, -2.0)
}

func TestAxis_ResistSwitchRefusesToReverseImmediately(t *testing.T) {
	a := NewAxis(AxisDec, AxisConfig{
		Aggressiveness: 1, MinMoveArcsec: 0.1, MaxMoveArcsec: 100, ResistSwitch: 3,
	})

	corr, why := a.Next(1)
	require.Equal(t, WhyApplied, why)
	require.InDelta(t, -1, corr, 1e-9)

	// The error reverses. Backlash makes a declination reversal expensive, so a single sample is not
	// enough evidence to spend it.
	for i := 1; i <= 2; i++ {
		corr, why = a.Next(-1)
		assert.Zero(t, corr, "reversal attempt %d should be resisted", i)
		assert.Equal(t, WhyResist, why)
	}
}

func TestAxis_ResistSwitchAllowsASustainedReversal(t *testing.T) {
	a := NewAxis(AxisDec, AxisConfig{
		Aggressiveness: 1, MinMoveArcsec: 0.1, MaxMoveArcsec: 100, ResistSwitch: 3,
	})
	require.NotZero(t, mustApply(t, a, 1))

	a.Next(-1)
	a.Next(-1)
	corr, why := a.Next(-1)
	assert.Equal(t, WhyApplied, why, "a reversal that persists is real and must be obeyed")
	assert.InDelta(t, 1, corr, 1e-9)

	// Once reversed, the new direction is free to continue without further resistance.
	corr, why = a.Next(-1)
	assert.Equal(t, WhyApplied, why)
	assert.InDelta(t, 1, corr, 1e-9)
}

func TestAxis_DeadbandDoesNotResetTheDirectionGuard(t *testing.T) {
	a := NewAxis(AxisDec, AxisConfig{
		Aggressiveness: 1, MinMoveArcsec: 0.5, MaxMoveArcsec: 100, ResistSwitch: 3,
	})
	require.NotZero(t, mustApply(t, a, 2))

	_, why := a.Next(-2)
	require.Equal(t, WhyResist, why)

	// A quiet sample in between carries no information about direction, so it must not be allowed to
	// launder a reversal past the guard.
	_, why = a.Next(0.1)
	require.Equal(t, WhyDeadband, why)

	_, why = a.Next(-2)
	assert.Equal(t, WhyResist, why, "the deadband sample must not have counted as agreement")

	_, why = a.Next(-2)
	assert.Equal(t, WhyApplied, why)
}

func TestAxis_RejectsNonFiniteError(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		a := plainAxis(t)
		corr, why := a.Next(bad)
		assert.Zero(t, corr)
		assert.Equal(t, WhyInvalid, why)
	}
}

func TestAxis_ResetForgetsPreviousState(t *testing.T) {
	a := NewAxis(AxisDec, AxisConfig{
		Aggressiveness: 1, Hysteresis: 0.5, MinMoveArcsec: 0.1, MaxMoveArcsec: 100, ResistSwitch: 3,
	})
	require.NotZero(t, mustApply(t, a, 2))

	a.Reset()

	// With no remembered direction there is nothing to switch away from, and no previous output to
	// blend in — which is exactly what a dither or a re-acquired reference needs.
	corr, why := a.Next(-2)
	assert.Equal(t, WhyApplied, why)
	assert.InDelta(t, 1, corr, 1e-9)
}

func TestNewAxis_ZeroConfigTakesPerAxisDefaults(t *testing.T) {
	ra := NewAxis(AxisRA, AxisConfig{})
	dec := NewAxis(AxisDec, AxisConfig{})

	assert.Positive(t, ra.Config().Hysteresis, "RA is driven against a smooth worm and wants smoothing")
	assert.Zero(t, ra.Config().ResistSwitch)

	assert.Zero(t, dec.Config().Hysteresis)
	assert.Positive(t, dec.Config().ResistSwitch, "Dec reverses through backlash and wants the guard")
}

func mustApply(t *testing.T, a *Axis, err float64) float64 {
	t.Helper()
	corr, why := a.Next(err)
	require.Equal(t, WhyApplied, why)
	return corr
}
