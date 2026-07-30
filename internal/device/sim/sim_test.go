package sim

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
)

// Compile-time proof that the simulator really is a drop-in for the hardware drivers.
var (
	_ device.Camera      = (*Camera)(nil)
	_ device.FilterWheel = (*Wheel)(nil)
	_ device.Mount       = (*Mount)(nil)
)

// smallWorld keeps test frames cheap (a full 16 Mpx render is a second of CPU) while preserving the
// real plate scale, so angular reasoning still holds. A 256 px field at 1.06″/px is only 4.5′ across
// — far too small to be sure a catalogue star falls in it — so a bright synthetic star is planted at
// the pointing centre and the tests measure THAT.
const (
	testRADeg  = 10.6847
	testDecDeg = 41.2687
)

func smallWorld(t *testing.T) *World {
	t.Helper()
	return NewWorld(Config{
		SensorW: 256, SensorH: 256, HotPixels: -1, PEAmplitude: -1, // a clean sensor, no PE
		StartRADeg: testRADeg, StartDecDeg: testDecDeg,
		SlewRateDegPerSec: 90, // keep cross-sky GoTo tests to a second, not twenty
		SyntheticStars: []SyntheticStar{
			{RADeg: testRADeg, DecDeg: testDecDeg, Mag: 6},
		},
	})
}

func expose(t *testing.T, cam *Camera, exposureUs int64) *device.Frame {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, cam.SetControl(device.ControlExposure, exposureUs, false))
	require.NoError(t, cam.StartExposure(ctx, false))
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := cam.ExposureState()
		require.NoError(t, err)
		if st == device.ExposureSuccess {
			break
		}
		require.NotEqual(t, device.ExposureFailed, st)
		require.True(t, time.Now().Before(deadline), "exposure never completed")
		time.Sleep(5 * time.Millisecond)
	}
	frame, err := cam.Download(ctx)
	require.NoError(t, err)
	return frame
}

func TestCamera_ExposureLifecycle(t *testing.T) {
	cam := NewCamera(smallWorld(t))
	ctx := context.Background()

	_, err := cam.Download(ctx)
	require.ErrorIs(t, err, device.ErrNotConnected, "a disconnected camera must refuse, not panic")

	require.NoError(t, cam.Connect(ctx))
	frame := expose(t, cam, 50_000)

	assert.Equal(t, 256, frame.Width)
	assert.Equal(t, 256, frame.Height)
	require.Len(t, frame.Pix, 256*256)
	assert.Equal(t, int64(50_000), frame.ExposureUs)
	assert.True(t, frame.HasTemp)
	assert.False(t, frame.StartedAt.IsZero())

	st, err := cam.ExposureState()
	require.NoError(t, err)
	assert.Equal(t, device.ExposureIdle, st, "downloading consumes the frame")
}

func TestCamera_RendersRealStars(t *testing.T) {
	cam := NewCamera(smallWorld(t))
	require.NoError(t, cam.Connect(context.Background()))

	frame := expose(t, cam, 2_000_000)

	// The field around M31 has catalogued stars, so the frame must have real structure: a peak well
	// above the background, not just noise.
	var sum float64
	peak := uint16(0)
	for _, v := range frame.Pix {
		sum += float64(v)
		if v > peak {
			peak = v
		}
	}
	mean := sum / float64(len(frame.Pix))
	assert.Greater(t, float64(peak), mean*5, "a star field must contain peaks well above the sky")
	assert.Greater(t, mean, 0.0)
}

// starWidth measures how much of the sensor a star covers: the number of pixels above 30 % of its
// peak. Defocus conserves flux while spreading it, so this area grows monotonically — and unlike a
// flux-weighted moment it is immune to the background noise that dominates once a defocused peak
// falls close to the sky.
func starWidth(frame *device.Frame) float64 {
	peak := uint16(0)
	for _, v := range frame.Pix {
		if v > peak {
			peak = v
		}
	}
	var bg float64
	n := 0
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			bg += float64(frame.Pix[y*frame.Width+x])
			n++
		}
	}
	bg /= float64(n)
	threshold := bg + 0.3*(float64(peak)-bg)
	if float64(peak)-bg < 50 {
		return 0 // no star worth measuring
	}
	area := 0
	for _, v := range frame.Pix {
		if float64(v) >= threshold {
			area++
		}
	}
	return float64(area)
}

// The physics that makes the focus meter testable: pushing the focuser away from focus must spread
// the light by the blur-circle relation, monotonically.
func TestCamera_DefocusSpreadsStars(t *testing.T) {
	world := smallWorld(t)
	cam := NewCamera(world)
	require.NoError(t, cam.Connect(context.Background()))

	world.SetFocusOffset(0)
	sharp := starWidth(expose(t, cam, 2_000_000))
	world.SetFocusOffset(150)
	soft := starWidth(expose(t, cam, 2_000_000))
	world.SetFocusOffset(400)
	softer := starWidth(expose(t, cam, 2_000_000))

	require.Greater(t, sharp, 0.0)
	assert.Greater(t, soft, sharp, "150 µm of defocus must visibly spread the stars")
	assert.Greater(t, softer, soft, "more defocus must spread them further")
}

func TestWheel_MoveIsAsynchronousAndAffectsThroughput(t *testing.T) {
	world := smallWorld(t)
	wheel := NewWheel(world)
	cam := NewCamera(world)
	ctx := context.Background()
	require.NoError(t, wheel.Connect(ctx))
	require.NoError(t, cam.Connect(ctx))

	st, err := wheel.State()
	require.NoError(t, err)
	assert.Equal(t, 7, st.Slots, "the simulated wheel carries the full canonical set incl. OIII/SII")
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha", "OIII", "SII"}, st.Names)
	assert.Equal(t, 1, st.Position)

	require.NoError(t, wheel.SetPosition(5)) // L → Ha, four slots of travel
	st, err = wheel.State()
	require.NoError(t, err)
	assert.True(t, st.Moving, "the wheel must report moving straight after the command")
	assert.Equal(t, 0, st.Position, "position is unknown while moving")

	require.ErrorIs(t, wheel.SetPosition(2), device.ErrBusy,
		"a second move while turning must be refused")

	require.NoError(t, wheel.WaitSettled(ctx))
	st, err = wheel.State()
	require.NoError(t, err)
	assert.False(t, st.Moving)
	assert.Equal(t, 5, st.Position)
	assert.Equal(t, "Ha", st.Names[4])

	// Narrowband passes far less light than luminance — the reason Ha subs are longer.
	haFrame := expose(t, cam, 2_000_000)
	require.NoError(t, wheel.SetPosition(1))
	require.NoError(t, wheel.WaitSettled(ctx))
	lumFrame := expose(t, cam, 2_000_000)
	assert.Greater(t, starWidthPeak(lumFrame), starWidthPeak(haFrame),
		"luminance must collect more signal than Ha at the same exposure")
}

func starWidthPeak(frame *device.Frame) float64 {
	peak := uint16(0)
	for _, v := range frame.Pix {
		if v > peak {
			peak = v
		}
	}
	return float64(peak)
}

func TestMount_GotoLandsOffAndSyncCorrectsIt(t *testing.T) {
	world := smallWorld(t)
	mount := NewMount(world)
	ctx := context.Background()
	require.NoError(t, mount.Connect(ctx))

	const targetRA, targetDec = 314.75, 44.31 // NGC 7000
	require.NoError(t, mount.GotoRADec(ctx, targetRA, targetDec))

	st, err := mount.State(ctx)
	require.NoError(t, err)
	assert.True(t, st.Slewing, "a slew across the sky must take time")

	waitSettled(t, mount)
	st, err = mount.State(ctx)
	require.NoError(t, err)
	missArcsec := astro.AngularSeparation(st.RADeg, st.DecDeg, targetRA, targetDec) * 3600
	assert.Greater(t, missArcsec, 10.0, "a real GoTo lands off; that is why centring exists")
	assert.Less(t, missArcsec, 200.0)

	// Plate-solve centring: tell the mount where it REALLY is, then re-slew.
	require.NoError(t, mount.Sync(ctx, st.RADeg, st.DecDeg))
	require.NoError(t, mount.GotoRADec(ctx, targetRA, targetDec))
	waitSettled(t, mount)
	st, err = mount.State(ctx)
	require.NoError(t, err)
	after := astro.AngularSeparation(st.RADeg, st.DecDeg, targetRA, targetDec) * 3600
	assert.Less(t, after, missArcsec/2, "sync + re-slew must converge on the target")
}

func TestMount_RefusesGotoWhenNotAligned(t *testing.T) {
	mount := NewMount(smallWorld(t))
	ctx := context.Background()
	require.NoError(t, mount.Connect(ctx))
	mount.SetAligned(false)

	err := mount.GotoRADec(ctx, 100, 20)
	require.Error(t, err, "an unaligned GoTo can drive the tube into the tripod")
	assert.Contains(t, err.Error(), "not aligned")
}

func TestMount_NudgeMovesByTheRequestedAmount(t *testing.T) {
	mount := NewMount(smallWorld(t))
	ctx := context.Background()
	require.NoError(t, mount.Connect(ctx))
	require.NoError(t, mount.SetTracking(ctx, true, "sidereal"))

	before, err := mount.State(ctx)
	require.NoError(t, err)
	require.NoError(t, mount.Nudge(ctx, 30, 0))
	after, err := mount.State(ctx)
	require.NoError(t, err)

	moved := astro.AngularSeparation(before.RADeg, before.DecDeg, after.RADeg, after.DecDeg) * 3600
	assert.InDelta(t, 30, moved, 2, "a 30″ dither must move about 30″")
}

func TestMount_AbortStopsMidSlew(t *testing.T) {
	mount := NewMount(smallWorld(t))
	ctx := context.Background()
	require.NoError(t, mount.Connect(ctx))

	require.NoError(t, mount.GotoRADec(ctx, 200, -10))
	require.NoError(t, mount.Abort(ctx))
	st, err := mount.State(ctx)
	require.NoError(t, err)
	assert.False(t, st.Slewing, "abort must stop the slew immediately")
	assert.Greater(t, astro.AngularSeparation(st.RADeg, st.DecDeg, 200, -10), 1.0,
		"aborting mid-slew must not silently arrive at the target")
}

func TestCamera_CoolerRampsTowardTheSetpoint(t *testing.T) {
	cam := NewCamera(smallWorld(t))
	require.NoError(t, cam.Connect(context.Background()))
	require.NoError(t, cam.SetControl(device.ControlTargetTemp, -15, false))
	require.NoError(t, cam.SetControl(device.ControlCoolerOn, 1, false))

	first, ok := cam.Control(device.ControlTemperature)
	require.True(t, ok)
	var last device.Control
	for i := 0; i < 30; i++ {
		last, _ = cam.Control(device.ControlTemperature)
	}
	assert.Less(t, last.Value, first.Value, "the sensor must cool toward the setpoint over time")

	power, ok := cam.Control(device.ControlCoolerPower)
	require.True(t, ok)
	assert.Positive(t, power.Value, "a running cooler draws power")
}

func TestCamera_SetROIAppliesHardwareShapeRules(t *testing.T) {
	cam := NewCamera(smallWorld(t))
	require.NoError(t, cam.Connect(context.Background()))

	got, err := cam.SetROI(device.ROI{Width: 101, Height: 101, Bin: 1})
	require.NoError(t, err)
	assert.Zero(t, got.Width%8, "ZWO hardware needs width to be a multiple of 8")
	assert.Zero(t, got.Height%2, "…and height a multiple of 2")

	got, err = cam.SetROI(device.ROI{Bin: 2})
	require.NoError(t, err)
	assert.Equal(t, 128, got.Width, "binning halves the readable frame")
	assert.Equal(t, 128, got.Height)
}

func TestCamera_VideoAndStillAreMutuallyExclusive(t *testing.T) {
	cam := NewCamera(smallWorld(t))
	ctx := context.Background()
	require.NoError(t, cam.Connect(ctx))
	require.NoError(t, cam.SetControl(device.ControlExposure, 20_000, false))
	require.NoError(t, cam.StartVideo(ctx))

	require.ErrorIs(t, cam.StartExposure(ctx, false), device.ErrBusy)

	frame, err := cam.NextFrame(ctx, time.Second)
	require.NoError(t, err)
	assert.Equal(t, 256, frame.Width)

	require.NoError(t, cam.StopVideo())
	require.NoError(t, cam.StartExposure(ctx, false))
}

func waitSettled(t *testing.T, mount *Mount) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := mount.State(context.Background())
		require.NoError(t, err)
		if !st.Slewing {
			return
		}
		require.True(t, time.Now().Before(deadline), "slew never finished")
		time.Sleep(20 * time.Millisecond)
	}
}
