package capture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Managing a running sequence: what it is set to, where it writes, and what pause, resume, stop and
// finish-the-rest actually do to it. These run against the real device server and the simulated
// camera, so what is asserted is the FRAMES ON DISK — the only record that cannot be optimistic.

// tallies condenses what a run actually recorded into the shape Remaining subtracts.
func (m *memRecorder) tallies() []FrameTally {
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := map[[2]string]int{}
	for _, f := range m.frames {
		counts[[2]string{f.Filter, f.Type}]++
	}
	out := make([]FrameTally, 0, len(counts))
	for k, n := range counts {
		out = append(out, FrameTally{Filter: k[0], Type: k[1], Count: n})
	}
	return out
}

// scanByFilter groups everything the scanner can see under a root, per filter.
func scanByFilter(t *testing.T, root string) map[string][]*inspect.Frame {
	t.Helper()
	inv, err := inspect.Scan(context.Background(), root)
	require.NoError(t, err)
	out := map[string][]*inspect.Frame{}
	for _, fr := range inv.Frames {
		out[fr.Filter] = append(out[fr.Filter], fr)
	}
	return out
}

// Every setting in a step has to reach the sensor and then the file. A sequence that silently shot
// the previous step's gain would produce frames that cannot be stacked together, and nothing about
// the run would look wrong at the time.
func TestRunner_EverySettingReachesTheFramesOnDisk(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	steps := []Step{
		{Filter: "L", Count: 2, ExposureUs: 40_000, Gain: 100, Offset: 30, Bin: 1, Type: "light"},
		{Filter: "Ha", Count: 2, ExposureUs: 90_000, Gain: 210, Offset: 60, Bin: 1, Type: "light"},
	}
	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "NGC7000", FocalMM: 740,
		Sequence: Sequence{Steps: steps},
	})
	require.NoError(t, err)
	require.Equal(t, 4, waitForStatus(t, runner, StatusCompleted, 30*time.Second).FrameIndex)

	byFilter := scanByFilter(t, dir)
	for _, step := range steps {
		frames := byFilter[step.Filter]
		require.Len(t, frames, step.Count, "filter %s", step.Filter)
		for _, fr := range frames {
			assert.Equal(t, inspect.Light, fr.Type, "%s: frame type", step.Filter)
			assert.Equal(t, "NGC7000", fr.Object, "%s: object", step.Filter)
			assert.Equal(t, step.ExposureUs/1000, fr.ExposureMs, "%s: exposure", step.Filter)
			assert.Equal(t, step.Gain, fr.Gain, "%s: gain", step.Filter)
			assert.Equal(t, step.Offset, fr.Offset, "%s: offset", step.Filter)
			assert.Equal(t, step.Bin, fr.BinX, "%s: binning", step.Filter)
		}
	}
}

// Where the frames land is part of the plan, not an implementation detail: the stacker finds a
// night by its folders, and a panel's frames in the wrong tile silently join the wrong mosaic.
func TestRunner_SaveLocationFollowsTheRequest(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()
	tile := 3

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31", Panel: "p04", TileIndex: &tile,
		Sequence: Sequence{Steps: []Step{
			{Filter: "G", Count: 1, ExposureUs: 40_000, Gain: 100, Bin: 1, Type: "light"},
			{Count: 1, ExposureUs: 40_000, Gain: 100, Bin: 1, Type: "dark"},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, waitForStatus(t, runner, StatusCompleted, 30*time.Second).FrameIndex)

	// A light goes to <root>/<panel>/<filter>; calibration to its own folder, never mixed in with
	// the lights it will later be subtracted from.
	assertOneFile(t, filepath.Join(dir, "p04", "G"))
	assertOneFile(t, filepath.Join(dir, "p04", "darks"))
}

func assertOneFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "expected a folder at %s", dir)
	assert.Len(t, entries, 1, "expected exactly one frame in %s", dir)
}

// The whole control surface of a running night, in the order somebody actually uses it.
func TestRunner_PauseResumeStopLifecycle(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31",
		Sequence: Sequence{Steps: []Step{
			{Filter: "L", Count: 30, ExposureUs: 60_000, Gain: 139, Bin: 1, Type: "light"},
		}},
	})
	require.NoError(t, err)

	// PAUSE stops after the frame in flight, never mid-exposure: a half-integrated frame is wasted.
	runner.Pause()
	paused := waitForStatus(t, runner, StatusPaused, 15*time.Second)
	require.Less(t, paused.FrameIndex, 30)
	settled := runner.Progress().FrameIndex
	time.Sleep(500 * time.Millisecond)
	require.Equal(t, settled, runner.Progress().FrameIndex, "a paused run takes no frames")

	// RESUME continues the same plan rather than restarting it.
	runner.Resume()
	require.Eventually(t, func() bool { return runner.Progress().FrameIndex > settled },
		15*time.Second, 20*time.Millisecond, "resuming must carry on capturing")
	assert.Equal(t, StatusRunning, runner.Progress().Status)

	// STOP ends it, and the count stays where it stopped rather than resetting.
	runner.Abort()
	final := waitForStatus(t, runner, StatusAborted, 15*time.Second)
	assert.Less(t, final.FrameIndex, 30)
	assert.GreaterOrEqual(t, final.FrameIndex, settled, "aborting must not lose frames already taken")

	// And the frames it did take are all on disk and readable.
	inv, err := inspect.Scan(context.Background(), dir)
	require.NoError(t, err)
	assert.Len(t, inv.Frames, final.FrameIndex)
}

// Stopping a night and finishing it later is one flow, and this is it end to end: shoot part of a
// plan, stop, work out what is owed from what was RECORDED, and shoot exactly that. The totals must
// come out at the plan — no channel short, none shot twice.
func TestRunner_ResumeFinishesExactlyWhatIsOwed(t *testing.T) {
	runner, rec := testRig(t)
	dir := t.TempDir()

	plan := Sequence{Interleave: true, Steps: []Step{
		{Filter: "L", Count: 4, ExposureUs: 40_000, Gain: 139, Bin: 1, Type: "light"},
		{Filter: "Ha", Count: 4, ExposureUs: 40_000, Gain: 139, Bin: 1, Type: "light"},
	}}
	req := Request{Root: dir, Object: "M31", Sequence: plan}

	_, err := runner.Start(context.Background(), req)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return runner.Progress().FrameIndex >= 3 },
		30*time.Second, 20*time.Millisecond, "the night has to get going before it is stopped")
	runner.Abort()
	stopped := waitForStatus(t, runner, StatusAborted, 15*time.Second)
	require.Less(t, stopped.FrameIndex, 8, "the point of this test is a night that did NOT finish")

	// What the journal would compute: the plan, minus what actually landed.
	remaining := Remaining(plan, rec.tallies())
	require.Positive(t, remaining.TotalFrames())

	resumed := req
	resumed.Sequence = remaining
	_, err = runner.Start(context.Background(), resumed)
	require.NoError(t, err)
	require.Equal(t, remaining.TotalFrames(),
		waitForStatus(t, runner, StatusCompleted, 60*time.Second).FrameIndex)

	byFilter := scanByFilter(t, dir)
	assert.Len(t, byFilter["L"], 4, "L must end up at the planned count, not short and not double")
	assert.Len(t, byFilter["Ha"], 4)
}

// Resuming a night that is already finished must take no frames at all. The sequencer refuses an
// empty sequence, which is what stops a stray click costing an hour of sky.
func TestRunner_ResumeOfAFinishedNightIsRefused(t *testing.T) {
	runner, rec := testRig(t)
	dir := t.TempDir()

	plan := Sequence{Steps: []Step{
		{Filter: "L", Count: 2, ExposureUs: 40_000, Gain: 139, Bin: 1, Type: "light"},
	}}
	_, err := runner.Start(context.Background(), Request{Root: dir, Object: "M31", Sequence: plan})
	require.NoError(t, err)
	waitForStatus(t, runner, StatusCompleted, 30*time.Second)

	remaining := Remaining(plan, rec.tallies())
	assert.Zero(t, remaining.TotalFrames())

	_, err = runner.Start(context.Background(), Request{Root: dir, Sequence: remaining})
	assert.Error(t, err, "a finished night has nothing to resume")
}

// Stopping a session and starting the next one immediately must not let the old run's dying words
// land on the new one.
//
// Abort only CANCELS: the loop still has to notice, abandon the frame in flight and write its
// terminal row, and the last thing it does is publish "aborted". Starting inside that window — which
// is exactly what "stop, then finish the rest" does — used to hand the brand-new session the old
// one's status, so a run that was capturing perfectly well reported itself stopped a second after it
// began. Every observation of it was then wrong: the panel, the logbook row, the ETA.
func TestRunner_ANewSessionIsNotClobberedByTheOneItReplaced(t *testing.T) {
	runner, _ := testRig(t)

	first := Request{
		Root: t.TempDir(), Object: "M31",
		Sequence: Sequence{Steps: []Step{
			{Filter: "L", Count: 40, ExposureUs: 80_000, Gain: 139, Bin: 1, Type: "light"},
		}},
	}
	_, err := runner.Start(context.Background(), first)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return runner.Progress().FrameIndex >= 1 },
		20*time.Second, 20*time.Millisecond)

	// Stop and restart with no pause in between, the way the button does.
	runner.Abort()
	second := first
	second.Root = t.TempDir()
	second.Sequence.Steps[0].Count = 2
	_, err = runner.Start(context.Background(), second)
	require.NoError(t, err)

	final := waitForStatus(t, runner, StatusCompleted, 30*time.Second)
	assert.Equal(t, 2, final.FrameIndex, "the new session must run its own plan to completion")

	// And the frames belong to the new run's folder, not the abandoned one.
	inv, err := inspect.Scan(context.Background(), second.Root)
	require.NoError(t, err)
	assert.Len(t, inv.Frames, 2)
}

// Gain ZERO is a real setting, and the one this app ships as its default — gain trades full-well
// depth for read noise that long subs make irrelevant. Sending gain and offset only when positive
// meant a step asking for 0 could never pull the camera down from the previous step's value: those
// frames were shot at a gain the plan never asked for, and only the header would ever have said so.
func TestRunner_AStepAskingForGainZeroGetsGainZero(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31",
		Sequence: Sequence{Steps: []Step{
			{Filter: "L", Count: 1, ExposureUs: 40_000, Gain: 200, Offset: 60, Bin: 1, Type: "light"},
			{Filter: "Ha", Count: 1, ExposureUs: 40_000, Gain: 0, Offset: 0, Bin: 1, Type: "light"},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, waitForStatus(t, runner, StatusCompleted, 30*time.Second).FrameIndex)

	byFilter := scanByFilter(t, dir)
	require.Len(t, byFilter["Ha"], 1)
	assert.Equal(t, int64(0), byFilter["Ha"][0].Gain,
		"the second step asked for gain 0 and must not inherit the first step's 200")
	assert.Equal(t, int64(0), byFilter["Ha"][0].Offset)
	require.Len(t, byFilter["L"], 1)
	assert.Equal(t, int64(200), byFilter["L"][0].Gain)
}

// Binning was validated and then never applied: the camera kept whatever the live view had left it
// at while the FILENAME was built from the step's bin, so a bin-2 step produced bin-1 frames labelled
// bin 2. A stacker believes the label, which makes that worse than not supporting binning at all.
func TestRunner_BinningReachesTheCameraAndTheFrameAgrees(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31",
		Sequence: Sequence{Steps: []Step{
			{Filter: "L", Count: 1, ExposureUs: 40_000, Gain: 100, Bin: 2, Type: "light"},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, waitForStatus(t, runner, StatusCompleted, 30*time.Second).FrameIndex)

	byFilter := scanByFilter(t, dir)
	require.Len(t, byFilter["L"], 1)
	frame := byFilter["L"][0]
	assert.Equal(t, 2, frame.BinX, "the header must record the binning the camera actually used")
	assert.Contains(t, frame.Path, "Bin2", "and the filename must agree with it")
}
