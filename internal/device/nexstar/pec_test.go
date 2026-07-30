package nexstar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The pass-through framing. Both length bytes are easy to get backwards and neither failure is loud:
// the mount simply answers with a different number of bytes than the driver reads, and every command
// after it reads the previous one's reply.
func TestPassthrough_FrameLayout(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		respLen byte
		want    []byte
	}{
		{"no payload", nil, 1, []byte{'P', 1, 16, 0x18, 0, 0, 0, 1}},
		{"one byte", []byte{0x40}, 1, []byte{'P', 2, 16, 0x30, 0x40, 0, 0, 1}},
		{"two bytes", []byte{0x41, 0x7F}, 0, []byte{'P', 3, 16, 0x31, 0x41, 0x7F, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.want[3]
			got := passthrough(axisAzmRA, cmd, tt.payload, tt.respLen)
			require.Len(t, got, 8, "a pass-through frame is always eight bytes")
			assert.Equal(t, tt.want, got)
			assert.Equal(t, byte(len(tt.payload)+1), got[1],
				"the length byte counts the command plus its payload")
		})
	}
}

// The rate scale changed between mount generations and only the model byte distinguishes them.
// Guessing scales the whole correction by two.
func TestPECGeometry_BranchesOnTheModelByte(t *testing.T) {
	tests := []struct {
		name          string
		model         byte
		wantRateScale float64
		wantWorm      float64
	}{
		{"Advanced VX", 20, 1024, 7200},
		{"NexStar GPS", 1, 512, 7200},
		{"i-Series", 2, 512, 7200},
		{"CGE Pro", 8, 1024, 3600},
		{"unknown model", 200, 1024, 7200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantRateScale, pecRateScale(tt.model))
			assert.Equal(t, tt.wantWorm, pecWormArcsec(tt.model))
		})
	}
}

func TestMount_PECCaps_DerivesTheAVXWorm(t *testing.T) {
	m := testMount(t, newFakeHC())

	caps, err := m.PECCaps(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 88, caps.Bins, "the count comes from the mount, never a constant")
	// 7200 sidereal arcsec / 15.0410686 — the 180-tooth worm.
	assert.InDelta(t, 478.68, caps.WormPeriodSec, 0.01)
	assert.InDelta(t, 5.44, caps.BinSec, 0.01)
	assert.InDelta(t, 0.01469, caps.LSBArcsecPerSec, 0.00001)
}

// The whole signed range must survive the round trip. INDI writes `(v < 127) ? v : 256 - v`, which
// only lands correctly by way of C's char conversion; reproducing it in Go would corrupt negatives.
func TestMount_PECWriteCurve_RoundTripsTheFullSignedRange(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	ctx := context.Background()

	want := make([]int8, 88)
	for i := range want {
		want[i] = int8(i - 44) // −44 … +43, crossing zero
	}
	want[0], want[1], want[2], want[3] = 127, -127, -1, 0

	require.NoError(t, m.PECWriteCurve(ctx, want))
	got, err := m.PECReadCurve(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// A bin holding 35 IS '#'. Reading binary replies by scanning for the terminator would take the value
// for the end of the message, hand back nothing, and leave the real '#' in the buffer — after which
// every later command reads the previous one's reply.
func TestMount_PECCurve_SurvivesABinValueThatLooksLikeTheTerminator(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	ctx := context.Background()

	curve := make([]int8, 88)
	curve[10] = '#' // 35
	curve[11] = 42  // proves the port did not desynchronise after the '#'

	require.NoError(t, m.PECWriteCurve(ctx, curve))
	got, err := m.PECReadCurve(ctx)
	require.NoError(t, err)
	assert.Equal(t, int8(35), got[10])
	assert.Equal(t, int8(42), got[11], "the command after a '#'-valued bin still reads its own reply")

	// And the mount is still answering the right questions afterwards.
	st, err := m.State(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Advanced VX", st.Model)
}

// A table half-written by a dropped byte tracks worse than no table at all, so a bin that does not
// read back the way it was written must fail the whole write rather than be reported as success.
func TestMount_PECWriteCurve_FailsWhenABinDoesNotReadBack(t *testing.T) {
	hc := newFakeHC()
	hc.pecCorrupt = map[int]int8{7: 99}
	m := testMount(t, hc)

	err := m.PECWriteCurve(context.Background(), make([]int8, 88))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bin 7")
	assert.Contains(t, err.Error(), "read back")
}

func TestMount_PECWriteCurve_RejectsAWrongLengthCurve(t *testing.T) {
	m := testMount(t, newFakeHC())

	err := m.PECWriteCurve(context.Background(), make([]int8, 50))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "50 bins")
	assert.Contains(t, err.Error(), "88")
}

func TestMount_PECStatus_ReportsIndexAndBin(t *testing.T) {
	hc := newFakeHC()
	hc.pecBin = 17
	m := testMount(t, hc)

	st, err := m.PECStatus(context.Background())
	require.NoError(t, err)
	assert.True(t, st.Supported)
	assert.True(t, st.Indexed)
	assert.Equal(t, 17, st.CurrentBin)
	assert.False(t, st.Playing, "nothing has asked for playback yet")
}

// The bin counter is the phase reference a training run folds on, so it has to actually advance.
func TestMount_PECBin_Advances(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	ctx := context.Background()

	first, err := m.PECBin(ctx)
	require.NoError(t, err)
	second, err := m.PECBin(ctx)
	require.NoError(t, err)
	assert.Equal(t, first+1, second)
}

func TestMount_PECPlayback_TracksWhatItCommanded(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	ctx := context.Background()

	require.NoError(t, m.PECPlayback(ctx, true))
	assert.True(t, hc.pecPlaying, "the mount was told to play")
	st, err := m.PECStatus(ctx)
	require.NoError(t, err)
	assert.True(t, st.Playing)

	require.NoError(t, m.PECPlayback(ctx, false))
	assert.False(t, hc.pecPlaying)
	st, err = m.PECStatus(ctx)
	require.NoError(t, err)
	assert.False(t, st.Playing)
}

// A hand-controller record session is invisible over the wire and would overwrite the table with
// whatever the mount sees while we measure, destroying both the run and the user's existing curve.
func TestMount_PECRecordStop_IsSendable(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.PECRecordStop(context.Background()))
	assert.True(t, hc.pecRecStopped)
}

func TestMount_PECSeekIndex_ReturnsWhenTheIndexIsFound(t *testing.T) {
	hc := newFakeHC()
	hc.pecIndexed = true
	m := testMount(t, hc)

	require.NoError(t, m.PECSeekIndex(context.Background()))
	st, err := m.PECStatus(context.Background())
	require.NoError(t, err)
	assert.False(t, st.Seeking)
}

// "AVX stuck on Seeking PEC Index" is a documented real failure. A hunting mount cannot be used for
// anything else, so the seek gets a deadline and leaves the mount stopped rather than searching.
func TestMount_PECSeekIndex_GivesUpAndStops(t *testing.T) {
	oldTimeout, oldPoll := pecSeekTimeout, pecSeekPoll
	pecSeekTimeout, pecSeekPoll = 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { pecSeekTimeout, pecSeekPoll = oldTimeout, oldPoll })

	hc := newFakeHC()
	hc.pecIndexed = false // never finds it
	m := testMount(t, hc)

	err := m.PECSeekIndex(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not find its PEC index")

	_, stopped := hc.sentPrefixed('M')
	assert.True(t, stopped, "a mount left hunting forever is worse than one that gave up")

	st, err := m.PECStatus(context.Background())
	require.NoError(t, err)
	assert.False(t, st.Seeking)
}

func TestMount_PEC_RequiresAConnection(t *testing.T) {
	m := New("/dev/fake", func(string) (Port, error) { return newFakeHC(), nil })
	ctx := context.Background()

	_, err := m.PECCaps(ctx)
	assert.ErrorIs(t, err, device.ErrNotConnected)
	_, err = m.PECReadCurve(ctx)
	assert.ErrorIs(t, err, device.ErrNotConnected)
	assert.ErrorIs(t, m.PECWriteCurve(ctx, make([]int8, 88)), device.ErrNotConnected)
	assert.ErrorIs(t, m.PECPlayback(ctx, true), device.ErrNotConnected)
}
