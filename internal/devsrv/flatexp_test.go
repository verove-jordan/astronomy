package devsrv

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ratio step is the heart of the search: the sensor is linear, so one measurement predicts the
// exposure that hits the target. These cases pin the behaviour that a real panel would otherwise
// only reveal at 1am.
func TestNextFlatExposure(t *testing.T) {
	const target = 32000.0

	// Half the target signal → twice the exposure.
	next, err := nextFlatExposure(100_000, 16000, target, false)
	require.NoError(t, err)
	assert.Equal(t, int64(200_000), next)

	// Twice the target → half the exposure.
	next, err = nextFlatExposure(100_000, 64000, target, false)
	require.NoError(t, err)
	assert.Equal(t, int64(50_000), next)

	// A clipped frame's median is a floor, not a measurement, so the ratio would be meaningless —
	// back off by a fixed factor instead.
	next, err = nextFlatExposure(100_000, 65535, target, true)
	require.NoError(t, err)
	assert.Equal(t, int64(25_000), next, "clipping must trigger a fixed back-off, not a ratio")

	// Essentially no signal: the cap is probably still on. A ratio would ask for an absurd exposure.
	next, err = nextFlatExposure(100_000, 0, target, false)
	require.NoError(t, err)
	assert.Equal(t, int64(800_000), next, "a bounded step up, not target/0")

	// A wild reading must not send the next exposure somewhere that takes minutes to read back.
	next, err = nextFlatExposure(1_000_000, 1.5, target, false)
	require.NoError(t, err)
	assert.Equal(t, int64(10_000_000), next, "the jump is capped at 10×")
}

// flatServer is a device server whose simulated telescope has a flat panel over the aperture, at a
// brightness that reaches half full well in a fraction of a second — so the ramp converges in a test
// rather than in real observing time.
func flatServer(t *testing.T, aduPerSec float64) *Server {
	t.Helper()
	srv := videoServer(t, true)
	require.NotNil(t, srv.world, "the simulated driver must be in use")
	srv.world.SetFlatPanel(aduPerSec)
	return srv
}

// A panel so bright that even the shortest exposure clips has no software answer — say so rather
// than looping.
func TestNextFlatExposure_RefusesTheImpossible(t *testing.T) {
	_, err := nextFlatExposure(1, 65535, 32000, true)
	assert.ErrorContains(t, err, "too bright")
}

// End to end against the simulated sensor: starting far from the target, the ramp must converge on
// an unclipped exposure near half full well.
func TestMeasureFlatExposure_ConvergesOnTheTarget(t *testing.T) {
	// 320000 ADU/s at gain 100 → half full well in about 0.1 s.
	srv := flatServer(t, 320_000)

	res, err := srv.measureFlatExposure(context.Background(), FlatExposureRequest{
		Gain: 100, StartUs: 1000, MaxTries: 8, // deliberately far too short a starting guess
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Attempts)

	assert.True(t, res.Converged,
		"the ramp must find a workable exposure: %+v", res.Attempts)
	assert.InEpsilon(t, FlatTargetADU, res.MedianADU, 0.10,
		"the chosen exposure must land within 10 %% of half full well")
	assert.False(t, res.Attempts[len(res.Attempts)-1].Clipped,
		"the accepted frame must not be clipping")
	assert.Positive(t, res.ExposureUs)
}

// Converging from ABOVE matters too — a bright panel through luminance overshoots on the first try.
func TestMeasureFlatExposure_ConvergesFromOverexposure(t *testing.T) {
	srv := flatServer(t, 320_000)

	res, err := srv.measureFlatExposure(context.Background(), FlatExposureRequest{
		Gain: 100, StartUs: 2_000_000, MaxTries: 8, // 20× too long: saturated to begin with
	})
	require.NoError(t, err)
	assert.True(t, res.Converged, "must come back down from clipping: %+v", res.Attempts)
	assert.False(t, res.Attempts[len(res.Attempts)-1].Clipped)
}

func TestMeasureFlatExposure_NeedsACamera(t *testing.T) {
	srv := videoServer(t, false)
	_, err := srv.measureFlatExposure(context.Background(), FlatExposureRequest{})
	assert.Error(t, err)
}

// The result must never claim success it did not achieve — a wrong flat exposure silently ruins
// every flat taken after it.
func TestMeasureFlatExposure_ReportsFailureHonestly(t *testing.T) {
	srv := flatServer(t, 320_000)

	res, err := srv.measureFlatExposure(context.Background(), FlatExposureRequest{
		Gain: 100, StartUs: 1000, MaxTries: 1, // one try cannot converge from this far away
	})
	require.NoError(t, err)
	assert.False(t, res.Converged)
	assert.NotEmpty(t, res.Message, "a non-converged result must explain itself")
}
