package capture

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/platesolve"
)

// A solver standing in for Siril: it reports where the SIMULATED mount is actually pointing, which
// is exactly what a real plate solve of that frame would say. That makes the loop's convergence
// testable without a telescope or a catalogue.
type mountSolver struct {
	client *Client
	calls  int
	fail   error
}

func (m *mountSolver) Solve(ctx context.Context, _ string, _ platesolve.Hint) (platesolve.Result, error) {
	m.calls++
	if m.fail != nil {
		return platesolve.Result{}, m.fail
	}
	st, err := m.client.Mount(ctx)
	if err != nil {
		return platesolve.Result{}, err
	}
	return platesolve.Result{
		RADeg: st.Mount.RADeg, DecDeg: st.Mount.DecDeg, ScaleArcsecPx: 1.06,
	}, nil
}

func centeringRig(t *testing.T) (*Runner, *Client, *mountSolver) {
	t.Helper()
	runner, _ := testRig(t)
	client := runner.client
	ctx := context.Background()
	_, err := client.do2(ctx)
	require.NoError(t, err)
	return runner, client, &mountSolver{client: client}
}

// do2 connects the simulated mount; a tiny helper so the rig reads cleanly.
func (c *Client) do2(ctx context.Context) (MountState, error) {
	var out MountState
	err := c.do(ctx, "POST", "/mount/connect", map[string]any{"driver": "sim"}, &out)
	return out, err
}

// The point of centring: a GoTo lands a minute or two off, and measuring closes the gap. The
// simulated mount misses deterministically, so this asserts real convergence rather than luck.
func TestCenter_ConvergesOnTheTarget(t *testing.T) {
	runner, client, solver := centeringRig(t)
	ctx := context.Background()
	// Near the zenith for the test site, whenever the suite happens to run: the device server refuses
	// GoTo below its altitude floor, and rightly so — a fixed target would make this test pass or fail
	// with the time of day rather than with the code.
	targetRA, targetDec := zenithTarget()

	require.NoError(t, client.Goto(ctx, targetRA, targetDec))
	require.NoError(t, runner.waitForSlew(ctx))

	res, err := runner.Center(ctx, solver, targetRA, targetDec, CenterOptions{
		ExposureUs: 20_000, ToleranceArcsec: 20, MaxIterations: 4,
	})
	require.NoError(t, err)

	require.NotEmpty(t, res.Attempts)
	assert.True(t, res.Centered, "centring must converge: %+v", res.Attempts)
	assert.LessOrEqual(t, res.FinalArcsec, 20.0)
	assert.Greater(t, res.Attempts[0].ErrorArcsec, 20.0,
		"the first frame should show the GoTo error that makes centring worth doing")
	assert.True(t, res.Attempts[0].Synced, "the first pass tells the mount where it really is")
	assert.InDelta(t, 1.06, res.ScaleArcsecPx, 1e-9)
}

// zenithTarget is the sky position overhead at the test rig's site right now: RA = local sidereal
// time, declination = latitude.
func zenithTarget() (raDeg, decDeg float64) {
	const lat, lon = 48.85, 2.35
	return astro.LST(time.Now().UTC(), lon), lat
}

func TestCenter_StopsWhenAlreadyOnTarget(t *testing.T) {
	runner, client, solver := centeringRig(t)
	ctx := context.Background()

	st, err := client.Mount(ctx)
	require.NoError(t, err)
	// Ask to centre on exactly where the mount already points.
	res, err := runner.Center(ctx, solver, st.Mount.RADeg, st.Mount.DecDeg, CenterOptions{
		ExposureUs: 20_000, ToleranceArcsec: 30,
	})
	require.NoError(t, err)
	assert.True(t, res.Centered)
	assert.Len(t, res.Attempts, 1, "one frame is enough when nothing needs correcting")
	assert.False(t, res.Attempts[0].Synced, "an on-target mount must not be re-synced")
}

// A solve that fails is normal (cloud, a sparse field). It must stop the loop with a clear reason
// rather than slewing on bad data.
func TestCenter_ReportsASolveFailure(t *testing.T) {
	runner, _, solver := centeringRig(t)
	solver.fail = fmt.Errorf("no stars detected")

	ra, dec := zenithTarget()
	_, err := runner.Center(context.Background(), solver, ra, dec, CenterOptions{
		ExposureUs: 20_000, MaxIterations: 2,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no stars detected")
	assert.Equal(t, 1, solver.calls, "a failed solve must not be retried blindly")
}

func TestCenter_RequiresASolver(t *testing.T) {
	runner, _ := testRig(t)
	_, err := runner.Center(context.Background(), nil, 10, 10, CenterOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plate solving is unavailable")
}

func TestCenterOptions_Defaults(t *testing.T) {
	o := CenterOptions{}.withDefaults()
	assert.Positive(t, o.ExposureUs)
	assert.Equal(t, 2, o.Bin, "binning speeds up both the download and the solve")
	assert.Equal(t, 30.0, o.ToleranceArcsec)
	assert.Equal(t, 3, o.MaxIterations)
}

func TestWaitForSlew_TimesOutRatherThanHanging(t *testing.T) {
	runner, _ := testRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// No mount connected: the wait must return an error, not block a night.
	err := runner.waitForSlew(ctx)
	assert.Error(t, err)
}
