package nexstar

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The safety table, as executable documentation.
//
// Every row is a decision about what a SECOND copy of a frame would do to a telescope. Getting one
// of them wrong is not a dropped feature — it is a mount that restarts a slew, corrupts its pointing
// model, or never stops.

func TestClassify_ByWhatASecondCopyWouldDo(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
		want  retrySafety
	}{
		// Reads: a duplicate costs twenty milliseconds of wire time and nothing else.
		{"precise position", []byte("e"), retryAfterResync},
		{"position", []byte("E"), retryAfterResync},
		{"altitude and azimuth", []byte("z"), retryAfterResync},
		{"goto in progress", []byte("L"), retryAfterResync},
		{"aligned", []byte("J"), retryAfterResync},
		{"tracking mode", []byte("t"), retryAfterResync},
		{"pier side", []byte("p"), retryAfterResync},
		{"model", []byte("m"), retryAfterResync},
		{"firmware", []byte("V"), retryAfterResync},
		{"site", []byte("w"), retryAfterResync},
		{"clock", []byte("h"), retryAfterResync},
		{"echo", []byte{'K', 'q'}, retryAfterResync},
		{"read a PEC bin", passthrough(axisAzmRA, mcPECReadData, []byte{pecBinOffset + 3}, 1), retryAfterResync},
		{"read the worm's bin", passthrough(axisAzmRA, mcPECBin, nil, 1), retryAfterResync},
		{"read the index flag", passthrough(axisAzmRA, mcAtIndex, nil, 1), retryAfterResync},
		{"read the autoguide rate", getGuideRateCommand(axisAzmRA), retryAfterResync},

		// Stops: a duplicate is a no-op, and NOT retrying is the hazard.
		{"cancel goto", []byte("M"), retryAlways},
		{"fixed-rate stop", fixedRateCommand(axisAzmRA, 0, true), retryAlways},
		{"fixed-rate stop, other direction", fixedRateCommand(axisAltDec, 0, false), retryAlways},
		{"variable-rate stop", slewRateCommand(axisAzmRA, 0), retryAlways},

		// Anything that moves the mount or writes state that outlives the session.
		{"precise goto", []byte("r34AB0500,12CE0500"), retryNever},
		{"goto", []byte("R34AB,12CE"), retryNever},
		{"precise sync", []byte("s34AB0500,12CE0500"), retryNever},
		{"sync", []byte("S34AB,12CE"), retryNever},
		{"set tracking", []byte{'T', TrackingEQNorth}, retryNever},
		{"set the clock", []byte{'H', 21, 0, 0, 7, 30, 26, 254, 1}, retryNever},
		{"set the site", []byte{'W', 48, 51, 24, 0, 2, 21, 3, 0}, retryNever},
		{"hibernate", []byte("x"), retryNever},
		{"fixed-rate slew", fixedRateCommand(axisAzmRA, 5, true), retryNever},
		{"variable-rate slew", slewRateCommand(axisAltDec, 8), retryNever},
		{"write a PEC bin", passthrough(axisAzmRA, mcPECWriteData, []byte{pecBinOffset, 3}, 0), retryNever},
		{"seek the PEC index", passthrough(axisAzmRA, mcSeekIndex, nil, 0), retryNever},
		{"start PEC playback", passthrough(axisAzmRA, mcPECPlayback, []byte{1}, 0), retryNever},
		{"set the autoguide rate", setGuideRateCommand(axisAzmRA, 0.5), retryNever},

		// The default has to be the safe one.
		{"an empty frame", nil, retryNever},
		{"a command nobody has classified yet", []byte("Q"), retryNever},
		{"a truncated pass-through", []byte{'P', 2, 16}, retryNever},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classify(tt.frame))
		})
	}
}

func TestMount_GotoRADec_IsNeverRetried(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{only: 'r', drop: true})
	err := m.GotoRADec(context.Background(), 83.8, -5.4)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownOutcome,
		"a timed-out GoTo has an UNKNOWN outcome; calling it a failure is how a user aborts a slew that is already happening")
	assert.Equal(t, 1, fp.sentCount('r'),
		"a second GoTo can send a settled tube back across the sky mid-exposure")
}

func TestMount_Sync_IsNeverRetried(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{only: 's', drop: true})
	err := m.Sync(context.Background(), 83.8, -5.4)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownOutcome)
	assert.Equal(t, 1, fp.sentCount('s'),
		"sync applies to where the mount is NOW; two syncs at two positions corrupt the pointing model")
}

func TestMount_SetTracking_IsNeverRetried(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{only: 'T', drop: true})
	err := m.SetTracking(context.Background(), true, "sidereal")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownOutcome)
	assert.Equal(t, 1, fp.sentCount('T'),
		"a retry after an unknown outcome can switch tracking back on during a slew")
}

func TestMount_Jog_StartIsNeverRetried(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{only: 'P', drop: true})
	err := m.Jog(context.Background(), device.DirNorth, 5)

	require.Error(t, err)
	assert.Equal(t, 1, fp.sentCount('P'),
		"a retried slew can leave an axis running if the caller's stop is consumed by the resynchronisation")
}

func TestMount_Abort_IsRetriedUntilItLands(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{only: 'M', drop: true}, fault{only: 'M', drop: true})
	require.NoError(t, m.Abort(context.Background()),
		"the STOP button must keep asking; a duplicate cancel does nothing at all")
	assert.Equal(t, stopAttempts, fp.sentCount('M'))
}

func TestMount_Jog_StopIsRetriedUntilItLands(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{only: 'P', drop: true}, fault{only: 'P', drop: true})
	require.NoError(t, m.Jog(context.Background(), device.DirNorth, 0))

	stops := 0
	for _, w := range fp.sent() {
		if len(w) > 0 && w[0] == 'P' && classify(w) == retryAlways {
			stops++
		}
	}
	assert.Equal(t, stopAttempts, stops, "a dropped stop leaves the mount slewing, so it is asked again")
}

func TestMount_State_ReadIsRetriedOnce(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{only: 'e', drop: true})
	st, err := m.State(context.Background())

	require.NoError(t, err)
	assert.InDelta(t, 10.0, st.RADeg, 0.5)
	assert.Equal(t, retriesAfterResync, fp.sentCount('e'))
}
