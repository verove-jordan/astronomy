package sim

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// guideWorld builds a world with nothing moving but the guide pulses: no periodic error, no jitter,
// and a frozen clock, so a test measures only what it commanded. Rendering is never invoked here, so
// the sensor size is irrelevant and these tests cost microseconds.
func guideWorld(t *testing.T, cfg Config) (*World, *Mount) {
	t.Helper()
	cfg.PEAmplitude = -1
	cfg.PEJitterArcsec = -1
	cfg.HotPixels = -1
	if cfg.SensorW == 0 {
		cfg.SensorW, cfg.SensorH = 64, 64
	}
	w := NewWorld(cfg)
	frozen := time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC)
	w.SetClock(func() time.Time { return frozen })

	m := NewMount(w)
	require.NoError(t, m.Connect(context.Background()))
	return w, m
}

// guidePointing reads the simulated mount's current position.
func guidePointing(t *testing.T, m *Mount) (raDeg, decDeg float64) {
	t.Helper()
	st, err := m.State(context.Background())
	require.NoError(t, err)
	return st.RADeg, st.DecDeg
}

func TestPulseGuide_RightAscensionMovesTheAxisNotTheSky(t *testing.T) {
	// The whole cos(dec) question, pinned down. A rotation of the RA axis by A arcseconds changes the
	// RA COORDINATE by A arcseconds at every declination — what shrinks with declination is the
	// distance the star travels across the sky, which is A·cos(dec).
	//
	// A simulator that skipped the conversion would move the coordinate by A/cos(dec) instead, and
	// would then happily agree with a guider that had made the same mistake. At 60° that is a factor
	// of two.
	tests := []struct {
		name   string
		decDeg float64
	}{
		{"celestial equator", 0},
		{"mid declination", 41.2687},
		{"high declination", 60},
		{"southern", -35},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, m := guideWorld(t, Config{
				StartRADeg: 100, StartDecDeg: tt.decDeg, DecBacklashArcsec: -1,
			})
			ra0, dec0 := guidePointing(t, m)

			// 10 arcseconds of axis rotation: 2″/s for 5 s.
			require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisRA, 2, 5*time.Second))

			ra1, dec1 := guidePointing(t, m)
			assert.InDelta(t, 10.0, (ra1-ra0)*3600, 0.05,
				"the RA coordinate must advance by the axis rotation, independent of declination")
			assert.InDelta(t, dec0, dec1, 1e-6, "an RA pulse must not move declination")
		})
	}
}

func TestPulseGuide_DeclinationMovesOneForOne(t *testing.T) {
	_, m := guideWorld(t, Config{StartRADeg: 100, StartDecDeg: 41, DecBacklashArcsec: -1})
	ra0, dec0 := guidePointing(t, m)

	require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisDec, 2, 5*time.Second))

	ra1, dec1 := guidePointing(t, m)
	assert.InDelta(t, 10.0, (dec1-dec0)*3600, 0.05, "the declination axis needs no cos(dec) undoing")
	assert.InDelta(t, ra0, ra1, 1e-6)
}

func TestPulseGuide_NegativeRateMovesTheOtherWay(t *testing.T) {
	_, m := guideWorld(t, Config{StartRADeg: 100, StartDecDeg: 0, DecBacklashArcsec: -1})
	ra0, _ := guidePointing(t, m)

	require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisRA, -2, 5*time.Second))

	ra1, _ := guidePointing(t, m)
	assert.InDelta(t, -10.0, (ra1-ra0)*3600, 0.05)
}

func TestPulseGuide_DeclinationBacklashSwallowsTheFirstReversal(t *testing.T) {
	_, m := guideWorld(t, Config{StartRADeg: 100, StartDecDeg: 41, DecBacklashArcsec: 4})
	ctx := context.Background()

	// Establishing a direction costs nothing: the gears are already engaged that way.
	_, dec0 := guidePointing(t, m)
	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisDec, 10, time.Second))
	_, dec1 := guidePointing(t, m)
	assert.InDelta(t, 10.0, (dec1-dec0)*3600, 0.05)

	// Reversing winds out four arcseconds of slack before the telescope follows, so only six of the
	// ten commanded arrive.
	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisDec, -10, time.Second))
	_, dec2 := guidePointing(t, m)
	assert.InDelta(t, -6.0, (dec2-dec1)*3600, 0.05, "the reversal spends the take-up first")

	// Continuing in the same direction is now free again.
	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisDec, -10, time.Second))
	_, dec3 := guidePointing(t, m)
	assert.InDelta(t, -10.0, (dec3-dec2)*3600, 0.05, "the slack is already wound out")
}

func TestPulseGuide_AlternatingSmallCorrectionsGetNowhere(t *testing.T) {
	// This is the failure the servo's resist-switch guard exists to prevent, made visible. Corrections
	// smaller than the backlash, alternating in direction, are absorbed entirely: the motor turns, the
	// night passes, and the telescope does not move.
	_, m := guideWorld(t, Config{StartRADeg: 100, StartDecDeg: 41, DecBacklashArcsec: 4})
	ctx := context.Background()

	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisDec, 2, time.Second))
	_, decAfterFirst := guidePointing(t, m)

	for i := 0; i < 10; i++ {
		rate := 2.0
		if i%2 == 0 {
			rate = -2.0
		}
		require.NoError(t, m.PulseGuide(ctx, device.GuideAxisDec, rate, time.Second))
	}

	_, decEnd := guidePointing(t, m)
	assert.InDelta(t, decAfterFirst, decEnd, 1e-6,
		"ten alternating sub-backlash corrections must achieve nothing at all")
}

func TestPulseGuide_LargeReversalsStillMakeProgress(t *testing.T) {
	_, m := guideWorld(t, Config{StartRADeg: 100, StartDecDeg: 41, DecBacklashArcsec: 4})
	ctx := context.Background()

	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisDec, 20, time.Second))
	_, start := guidePointing(t, m)
	for i := 0; i < 4; i++ {
		require.NoError(t, m.PulseGuide(ctx, device.GuideAxisDec, -20, time.Second))
	}
	_, end := guidePointing(t, m)

	// The first reversal loses the take-up; the three after it do not.
	assert.InDelta(t, -(16.0 + 3*20.0), (end-start)*3600, 0.2)
}

func TestPulseGuide_IgnoresNothingRequests(t *testing.T) {
	_, m := guideWorld(t, Config{StartRADeg: 100, StartDecDeg: 41, DecBacklashArcsec: -1})
	ra0, dec0 := guidePointing(t, m)

	ctx := context.Background()
	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisRA, 8, 0))
	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisRA, 0, time.Second))
	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisDec, 8, -time.Second))

	ra1, dec1 := guidePointing(t, m)
	assert.InDelta(t, ra0, ra1, 1e-9)
	assert.InDelta(t, dec0, dec1, 1e-9)
}

func TestPulseGuide_RequiresAConnection(t *testing.T) {
	w := NewWorld(Config{})
	m := NewMount(w)

	err := m.PulseGuide(context.Background(), device.GuideAxisRA, 8, time.Second)
	assert.ErrorIs(t, err, device.ErrNotConnected)
}

func TestGuideRate_DefaultsAndRoundTrips(t *testing.T) {
	_, m := guideWorld(t, Config{})
	ctx := context.Background()

	rate, err := m.GuideRate(ctx)
	require.NoError(t, err)
	assert.InDelta(t, simGuideRate, rate, 1e-9, "an unconfigured mount reports its default, not zero")

	require.NoError(t, m.SetGuideRate(ctx, 0.75))
	rate, err = m.GuideRate(ctx)
	require.NoError(t, err)
	assert.InDelta(t, 0.75, rate, 1e-9)
}

func TestSetGuideRate_ClampsAndQuantises(t *testing.T) {
	tests := []struct {
		name     string
		fraction float64
		want     float64
	}{
		{"below zero clamps", -1, 0},
		{"above one clamps", 5, 1},
		// The wire protocol carries the rate in 1/256ths, so the simulator rounds the same way — a
		// value that survives here cannot be a surprise on real hardware.
		{"quantised to the wire resolution", 0.5001, 128.0 / 256.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, m := guideWorld(t, Config{})
			ctx := context.Background()

			require.NoError(t, m.SetGuideRate(ctx, tt.fraction))
			got, err := m.GuideRate(ctx)
			require.NoError(t, err)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestPulseGuide_ConformsToTheGuideMountInterface(t *testing.T) {
	// Compile-time conformance is asserted in guide.go; this proves the type assertion a caller
	// actually performs succeeds too, since that is what decides whether guiding is offered at all.
	_, m := guideWorld(t, Config{})
	gm, ok := device.Mount(m).(device.GuideMount)
	require.True(t, ok, "the simulator must offer guiding, or the loop cannot be tested without hardware")
	assert.NotNil(t, gm)
}
