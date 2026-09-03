package nexstar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// replyGuide answers the autoguide-rate commands. It lives beside the guide tests rather than in
// replyPEC, which documents itself as answering the periodic-error commands and only those.
func (f *fakeHC) replyGuide(p []byte) ([]byte, bool) {
	// Which motor the frame is aimed at matters here: the rate is stored per axis, and SetGuideRate
	// only keeps the two in step because it writes both.
	switch p[3] {
	case mcSetAutoguideRate:
		if p[2] == axisAltDec {
			f.guideRateDec = p[4]
		} else {
			f.guideRate = p[4]
		}
		return []byte("#"), true
	case mcGetAutoguideRate:
		if p[2] == axisAltDec {
			return []byte{f.guideRateDec, '#'}, true
		}
		return []byte{f.guideRate, '#'}, true
	}
	return nil, false
}

// rateFrames returns the pass-through frames that set a variable rate, which is how a guide pulse
// appears on the wire: one to start the axis and one to stop it.
func rateFrames(hc *fakeHC) []string {
	var out []string
	for _, c := range hc.commands() {
		if len(c) >= 8 && c[0] == 'P' && (c[3] == 6 || c[3] == 7) {
			out = append(out, c)
		}
	}
	return out
}

func TestPulseGuide_StartsAndAlwaysStopsTheAxis(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisRA, 8, minPulseDuration))

	frames := rateFrames(hc)
	require.Len(t, frames, 2, "a pulse is exactly one start and one stop")

	start := []byte(frames[0])
	assert.Equal(t, byte(16), start[2], "azimuth/RA motor")
	assert.Equal(t, byte(6), start[3], "positive rate")
	assert.Equal(t, 8*4, int(start[4])<<8|int(start[5]), "the protocol wants the rate in quarter-arcsec/s")

	stop := []byte(frames[1])
	assert.Equal(t, byte(16), stop[2])
	assert.Zero(t, int(stop[4])<<8|int(stop[5]), "an unstopped pulse is a mount that walks away")
}

func TestPulseGuide_NegativeRateReversesTheDirection(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisDec, -8, minPulseDuration))

	frames := rateFrames(hc)
	require.Len(t, frames, 2)
	assert.Equal(t, byte(17), []byte(frames[0])[2], "altitude/declination motor")
	assert.Equal(t, byte(7), []byte(frames[0])[3], "negative rate")
}

func TestPulseGuide_StopsTheAxisEvenWhenCancelled(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// A cancelled guide session must leave the mount tracking, not slewing — so the stop frame is not
	// conditional on the context.
	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisRA, 8, 5*time.Second))

	frames := rateFrames(hc)
	require.Len(t, frames, 2)
	assert.Zero(t, int([]byte(frames[1])[4])<<8|int([]byte(frames[1])[5]))
}

func TestPulseGuide_ShortRequestsKeepTheAngleAndSlowDown(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	// 8″/s for 25 ms is 0.2″. The link cannot time 25 ms accurately, so the driver re-expresses it as
	// the same 0.2″ spread over the minimum pulse: a quarter of the rate over four times the time.
	require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisRA, 8, 25*time.Millisecond))

	frames := rateFrames(hc)
	require.Len(t, frames, 2)
	start := []byte(frames[0])
	gotRate := float64(int(start[4])<<8|int(start[5])) / 4
	assert.InDelta(t, 2.0, gotRate, 0.01, "the rate is scaled down")
	assert.InDelta(t, 0.2, gotRate*minPulseDuration.Seconds(), 0.01, "but the commanded angle is preserved")
}

func TestPulseGuide_IgnoresNothingRequests(t *testing.T) {
	tests := []struct {
		name string
		rate float64
		d    time.Duration
	}{
		{"zero duration", 8, 0},
		{"negative duration", 8, -time.Second},
		{"zero rate", 0, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := newFakeHC()
			m := testMount(t, hc)

			require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisRA, tt.rate, tt.d))
			assert.Empty(t, rateFrames(hc), "nothing was asked for, so nothing should reach the motor")
		})
	}
}

func TestPulseGuide_RefusesAnAbsurdDuration(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	err := m.PulseGuide(context.Background(), device.GuideAxisRA, 8, time.Hour)

	require.Error(t, err, "an hour-long guide pulse is a caller bug, and the failure mode is a tripod strike")
	assert.Empty(t, rateFrames(hc), "and it must be refused before anything reaches the motor")
}

func TestPulseGuide_RequiresAConnection(t *testing.T) {
	m := New("/dev/nowhere", func(string) (Port, error) { return nil, assert.AnError })

	err := m.PulseGuide(context.Background(), device.GuideAxisRA, 8, minPulseDuration)
	assert.ErrorIs(t, err, device.ErrNotConnected)
}

func TestGuideRate_ReadsTheMountsOwnSetting(t *testing.T) {
	hc := newFakeHC()
	hc.guideRate = 128
	m := testMount(t, hc)

	rate, err := m.GuideRate(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 0.5, rate, 1e-9, "128/256 is half sidereal")
}

func TestGuideRate_SurvivesAValueThatIsAlsoTheTerminator(t *testing.T) {
	hc := newFakeHC()
	// 35 is a perfectly ordinary rate — and it is also '#'. A reply read by scanning for the
	// terminator would return an empty body here and leave the real '#' in the buffer, after which
	// every later command reads the previous one's reply. This is the same trap the PEC table hit.
	hc.guideRate = 35
	m := testMount(t, hc)

	rate, err := m.GuideRate(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 35.0/256.0, rate, 1e-9)

	// And the port must still be in step afterwards.
	state, err := m.State(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 41, state.DecDeg, 0.5, "a desynchronised port would report nonsense here")
}

func TestSetGuideRate_WritesBothMotors(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.SetGuideRate(context.Background(), 0.75))

	var motors []byte
	for _, c := range hc.commands() {
		if len(c) >= 8 && c[0] == 'P' && c[3] == mcSetAutoguideRate {
			motors = append(motors, c[2])
			assert.Equal(t, byte(192), c[4], "0.75 × 256")
		}
	}
	assert.Equal(t, []byte{16, 17}, motors, "both motors, so the two axes cannot drift apart")
}

func TestSetGuideRate_ClampsOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name     string
		fraction float64
		want     byte
	}{
		{"below zero", -1, 0},
		{"zero", 0, 0},
		{"full sidereal does not overflow the byte", 1, 255},
		{"above one", 4, 255},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := newFakeHC()
			m := testMount(t, hc)

			require.NoError(t, m.SetGuideRate(context.Background(), tt.fraction))
			assert.Equal(t, tt.want, hc.guideRate)
		})
	}
}

func TestSetGuideRateCommand_EncodesAsFractionOfSidereal(t *testing.T) {
	cmd := setGuideRateCommand(axisAzmRA, 0.5)
	require.Len(t, cmd, 8)
	assert.Equal(t, byte('P'), cmd[0])
	assert.Equal(t, byte(2), cmd[1], "one command byte plus one payload byte")
	assert.Equal(t, byte(16), cmd[2])
	assert.Equal(t, byte(mcSetAutoguideRate), cmd[3])
	assert.Equal(t, byte(128), cmd[4])
	assert.Zero(t, cmd[7], "no reply expected")

	read := getGuideRateCommand(axisAltDec)
	assert.Equal(t, byte(17), read[2])
	assert.Equal(t, byte(mcGetAutoguideRate), read[3])
	assert.Equal(t, byte(1), read[7], "exactly one reply byte, read by length")
}

// A guide pulse turns the axis with the same variable-rate slew a nudge uses, and that stops the
// drive. Nudge has always put it back; this did not, and guiding is the worse place to lose it — the
// pulses land DURING an exposure, dozens of times in one, so an unrestored drive means the rest of
// that sub is taken with right ascension standing still.
func TestPulseGuide_PutsTheDriveBackAfterwards(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisRA, 8, 150*time.Millisecond))

	sent, ok := hc.sentPrefixed('T')
	require.True(t, ok, "the drive must be put back after a guide pulse")
	assert.Equal(t, byte(TrackingEQNorth), sent[1], "restored to the mode it was in, not a guess")

	// And after the axis has stopped, or it re-asserts tracking and then cancels it again.
	lastRate, lastTrack := -1, -1
	for i, c := range hc.commands() {
		if len(c) == 8 && c[0] == 'P' && (c[3] == 6 || c[3] == 7) {
			lastRate = i
		}
		if len(c) >= 2 && c[0] == 'T' {
			lastTrack = i
		}
	}
	assert.Greater(t, lastTrack, lastRate, "tracking is restored after the axis has stopped")
}

// A drive that was deliberately off must stay off. PEC training stops it on purpose and then guides
// nothing; re-asserting a mode we never saw would start a mount under someone.
func TestPulseGuide_LeavesAStoppedDriveStopped(t *testing.T) {
	hc := newFakeHC()
	hc.trackMode = TrackingOff
	m := testMount(t, hc)

	require.NoError(t, m.PulseGuide(context.Background(), device.GuideAxisDec, 8, 150*time.Millisecond))

	_, ok := hc.sentPrefixed('T')
	assert.False(t, ok, "no drive command may be sent when tracking was already off")
}

// The mode is remembered, not asked for. Guiding fires constantly, so a `t` query per pulse would
// put two extra round trips on the link for every correction of the night.
func TestPulseGuide_DoesNotQueryTheDriveModePerPulse(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	ctx := context.Background()

	// One State() read is what a mount panel does every second anyway; it populates the cache.
	_, err := m.State(ctx)
	require.NoError(t, err)
	before := countPrefixed(hc, 't')

	for i := 0; i < 3; i++ {
		require.NoError(t, m.PulseGuide(ctx, device.GuideAxisRA, 8, 150*time.Millisecond))
	}

	assert.Equal(t, before, countPrefixed(hc, 't'), "no pulse may re-ask what the drive mode is")
	assert.Equal(t, 3, countPrefixed(hc, 'T'), "but every pulse must put the drive back")
}

// With nothing cached yet — the first pulse of a session — it pays for one reading rather than
// guessing. Restoring a mode that was never seen could start a mount under someone.
func TestPulseGuide_AsksOnceWhenItHasNeverSeenTheMode(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	ctx := context.Background()

	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisRA, 8, 150*time.Millisecond))
	require.NoError(t, m.PulseGuide(ctx, device.GuideAxisRA, 8, 150*time.Millisecond))

	assert.Equal(t, 1, countPrefixed(hc, 't'), "asked once, then remembered")
}

func countPrefixed(hc *fakeHC, prefix byte) int {
	n := 0
	for _, c := range hc.commands() {
		if len(c) > 0 && c[0] == prefix {
			n++
		}
	}
	return n
}

// A pulse rejected before it moves anything must not cost two frames on a 9600-baud link.
func TestPulseGuide_RejectedPulseTouchesNothing(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	before := len(hc.commands())

	require.Error(t, m.PulseGuide(context.Background(), device.GuideAxisRA, 8, time.Hour))

	assert.Equal(t, before, len(hc.commands()), "a refused pulse sends nothing at all")
}
