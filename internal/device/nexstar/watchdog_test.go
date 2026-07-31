package nexstar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The deadman, and the bug it replaces.
//
// A motor controller does not stop because a USB cable was pulled or a browser tab was closed. Both
// nudgeAxis and PulseGuide used to check `if m.port == nil { return nil }` before sending their stop
// — reporting SUCCESS having left the axis turning. These tests pin the replacement.

// stopFrames counts the rate-0 frames sent for an axis. classify already knows which frames those
// are, so the test cannot drift from the safety table.
func stopFrames(fp *faultyPort, axis int) int {
	n := 0
	for _, w := range fp.sent() {
		if len(w) >= 8 && w[0] == 'P' && int(w[2]) == axis && classify(w) == retryAlways {
			n++
		}
	}
	return n
}

func TestMount_Jog_StopsItselfWhenNoRenewalArrives(t *testing.T) {
	hc := newFakeHC()
	m, fp, clk := faultyRig(t, hc)

	require.NoError(t, m.Jog(context.Background(), device.DirNorth, 5))
	require.Equal(t, 1, m.stopsPending(), "a moving axis must be armed the moment it starts")

	// Not yet: the caller still has time to renew.
	m.expireStops()
	assert.Equal(t, 0, stopFrames(fp, axisAltDec))
	assert.Equal(t, 1, m.stopsPending())

	// Past the deadline — this is the browser tab that closed mid-jog.
	clk.advance(jogDeadman + time.Second)
	m.expireStops()
	assert.Equal(t, 1, stopFrames(fp, axisAltDec), "an unrenewed jog must stop itself")
	assert.Equal(t, 0, m.stopsPending())
}

func TestMount_Jog_RenewalKeepsTheAxisMoving(t *testing.T) {
	hc := newFakeHC()
	m, fp, clk := faultyRig(t, hc)

	require.NoError(t, m.Jog(context.Background(), device.DirEast, 3))
	clk.advance(jogDeadman - time.Second)
	require.NoError(t, m.Jog(context.Background(), device.DirEast, 3)) // the UI's renewal

	clk.advance(jogDeadman - time.Second)
	m.expireStops()
	assert.Equal(t, 0, stopFrames(fp, axisAzmRA), "a renewed jog must not be cut short")
	assert.Equal(t, 1, m.stopsPending())
}

func TestMount_Jog_DeadmanCoversEachAxisIndependently(t *testing.T) {
	hc := newFakeHC()
	m, fp, clk := faultyRig(t, hc)

	require.NoError(t, m.Jog(context.Background(), device.DirNorth, 4))
	clk.advance(2 * time.Second)
	require.NoError(t, m.Jog(context.Background(), device.DirEast, 4))
	require.Equal(t, 2, m.stopsPending())

	// Enough for the first axis's deadline, not the second's.
	clk.advance(jogDeadman - time.Second)
	m.expireStops()
	assert.Equal(t, 1, stopFrames(fp, axisAltDec))
	assert.Equal(t, 0, stopFrames(fp, axisAzmRA))
	assert.Equal(t, 1, m.stopsPending())
}

func TestMount_Jog_StopDisarmsTheDeadman(t *testing.T) {
	hc := newFakeHC()
	m, fp, clk := faultyRig(t, hc)

	require.NoError(t, m.Jog(context.Background(), device.DirWest, 6))
	require.NoError(t, m.Jog(context.Background(), device.DirWest, 0))
	assert.Equal(t, 0, m.stopsPending())

	before := stopFrames(fp, axisAzmRA)
	clk.advance(time.Hour)
	m.expireStops()
	assert.Equal(t, before, stopFrames(fp, axisAzmRA), "a stopped axis must not be stopped again forever")
}

func TestMount_Nudge_KeepsTheStopArmedWhenTheLinkDiesMidMove(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	// Pull the cable the instant the axis is asked to move — the moment that used to be reported as a
	// successful nudge with the motor still turning.
	fp.onWrite(func(frame []byte) {
		if len(frame) >= 8 && frame[0] == 'P' && classify(frame) == retryNever {
			fp.kill()
		}
	})

	err := m.nudgeAxis(context.Background(), axisAzmRA, 1)
	require.Error(t, err, "a nudge that could not stop the axis is not a success")
	assert.Equal(t, 1, m.stopsPending(),
		"the stop must survive the link so the reconnect can send it")
}

func TestMount_PulseGuide_KeepsTheStopArmedWhenTheLinkDiesMidPulse(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.onWrite(func(frame []byte) {
		if len(frame) >= 8 && frame[0] == 'P' && classify(frame) == retryNever {
			fp.kill()
		}
	})

	err := m.PulseGuide(context.Background(), device.GuideAxisRA, 8, minPulseDuration)
	require.Error(t, err)
	assert.Equal(t, 1, m.stopsPending(),
		"guiding runs all night, so it is the likeliest command to be in flight when a cable is nudged")
}

func TestMount_Watchdog_StopIsRetriedThroughAResynchronisation(t *testing.T) {
	hc := newFakeHC()
	m, fp, clk := faultyRig(t, hc)

	require.NoError(t, m.Jog(context.Background(), device.DirSouth, 2))
	fp.inject(fault{only: 'P', drop: true}) // the first stop goes missing

	clk.advance(jogDeadman + time.Second)
	m.expireStops()

	assert.GreaterOrEqual(t, stopFrames(fp, axisAltDec), 2, "a dropped stop is asked again")
	assert.Equal(t, 0, m.stopsPending())
}

func TestMount_SetDeadman_ShortensTheRenewalWindow(t *testing.T) {
	hc := newFakeHC()
	m, fp, clk := faultyRig(t, hc)

	m.SetDeadman(time.Second)
	require.NoError(t, m.Jog(context.Background(), device.DirNorth, 9))

	clk.advance(1500 * time.Millisecond)
	m.expireStops()
	assert.Equal(t, 1, stopFrames(fp, axisAltDec))

	m.SetDeadman(0)
	assert.Equal(t, jogDeadman, m.deadmanLocked(), "zero restores the default rather than disabling it")
}
