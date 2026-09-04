package capture

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/devsrv"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// memRecorder is an in-memory Recorder, so a whole session can be exercised without a database.
type memRecorder struct {
	mu       sync.Mutex
	sessions int
	frames   []FrameRecord
	last     Progress
}

func (m *memRecorder) CreateSession(context.Context, Request, int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions++
	return int64(m.sessions), nil
}

func (m *memRecorder) UpdateSession(_ context.Context, _ int64, _ Status, p Progress) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last = p
	return nil
}

func (m *memRecorder) RecordFrame(_ context.Context, _ int64, f FrameRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frames = append(m.frames, f)
	return nil
}

func (m *memRecorder) frameCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.frames)
}

// testRig starts a real device server against the simulator and returns a sequencer wired to it.
func testRig(t *testing.T) (*Runner, *memRecorder) {
	t.Helper()
	cfg := &config.Config{
		FocalLenMM: 740, PixelSizeUm: 3.8, SensorWpx: 128, SensorHpx: 128,
		ApertureMM: 100, LatDeg: 48.85, LonDeg: 2.35,
	}
	dev := devsrv.New(cfg)
	ts := httptest.NewServer(dev.Handler())
	t.Cleanup(func() {
		ts.Close()
		dev.Close()
	})
	client := NewClient(strings.TrimPrefix(ts.URL, "http://"))
	ctx := context.Background()
	_, err := client.ConnectCamera(ctx, "sim")
	require.NoError(t, err)
	_, err = client.ConnectWheel(ctx, "sim", nil)
	require.NoError(t, err)

	rec := &memRecorder{}
	return NewRunner(client, rec), rec
}

func waitForStatus(t *testing.T, r *Runner, want Status, within time.Duration) Progress {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		p := r.Progress()
		if p.Status == want {
			return p
		}
		require.True(t, time.Now().Before(deadline),
			"status %q never reached (still %q: %s %s)", want, p.Status, p.Message, p.Error)
		time.Sleep(20 * time.Millisecond)
	}
}

// The core promise of the auto-run: declare the plan once, and every frame lands correctly named,
// correctly filtered and classifiable by the processing pipeline.
func TestRunner_ShootsASequenceEndToEnd(t *testing.T) {
	runner, rec := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31", FocalMM: 740,
		Sequence: Sequence{
			Steps: []Step{
				{Filter: "L", Count: 2, ExposureUs: 20_000, Gain: 139, Bin: 1, Type: "light"},
				{Filter: "Ha", Count: 1, ExposureUs: 20_000, Gain: 200, Bin: 1, Type: "light"},
			},
		},
	})
	require.NoError(t, err)

	final := waitForStatus(t, runner, StatusCompleted, 20*time.Second)
	assert.Equal(t, 3, final.FrameIndex)
	assert.Equal(t, 3, final.TotalFrames)
	assert.Equal(t, 2, final.Captured["L"])
	assert.Equal(t, 1, final.Captured["Ha"])
	assert.Equal(t, 3, rec.frameCount())

	inv, err := inspect.Scan(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, inv.Frames, 3, "every captured frame must be visible to the scanner")
	filters := map[string]int{}
	for _, fr := range inv.Frames {
		assert.Equal(t, inspect.Light, fr.Type)
		assert.Equal(t, "M31", fr.Object)
		filters[fr.Filter]++
	}
	assert.Equal(t, 2, filters["L"])
	assert.Equal(t, 1, filters["Ha"], "the wheel must actually have moved between steps")
}

func TestRunner_MosaicPanelFramesLandInTheTileFolder(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31", Panel: "p03",
		Sequence: Sequence{Steps: []Step{
			{Filter: "L", Count: 1, ExposureUs: 20_000, Gain: 139, Bin: 1, Type: "light"},
		}},
	})
	require.NoError(t, err)
	final := waitForStatus(t, runner, StatusCompleted, 20*time.Second)

	assert.Contains(t, final.LastPath, "/p03/",
		"a mosaic tile's frames must land in the folder the stacker segments on")
}

func TestRunner_InterleavesWhenAsked(t *testing.T) {
	seq := Sequence{
		Interleave: true,
		Steps: []Step{
			{Filter: "L", Count: 2, ExposureUs: 1000},
			{Filter: "R", Count: 2, ExposureUs: 1000},
		},
	}
	order := seq.order()
	got := make([]string, len(order))
	for i, s := range order {
		got[i] = s.Filter
	}
	assert.Equal(t, []string{"L", "R", "L", "R"}, got,
		"interleaving spreads every channel across the night, so clouds cost colour evenly")

	seq.Interleave = false
	order = seq.order()
	got = got[:0]
	for _, s := range order {
		got = append(got, s.Filter)
	}
	assert.Equal(t, []string{"L", "L", "R", "R"}, got)
}

func TestRunner_PauseAndResume(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31",
		Sequence: Sequence{Steps: []Step{
			{Filter: "L", Count: 6, ExposureUs: 50_000, Gain: 139, Bin: 1, Type: "light"},
		}},
	})
	require.NoError(t, err)

	runner.Pause()
	paused := waitForStatus(t, runner, StatusPaused, 10*time.Second)
	assert.Less(t, paused.FrameIndex, 6, "pausing must stop before the sequence finishes")

	// Nothing more is captured while paused.
	before := runner.Progress().FrameIndex
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, before, runner.Progress().FrameIndex)

	runner.Resume()
	final := waitForStatus(t, runner, StatusCompleted, 30*time.Second)
	assert.Equal(t, 6, final.FrameIndex, "resuming must finish the plan, not restart it")
}

func TestRunner_Abort(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31",
		Sequence: Sequence{Steps: []Step{
			{Filter: "L", Count: 20, ExposureUs: 100_000, Gain: 139, Bin: 1, Type: "light"},
		}},
	})
	require.NoError(t, err)
	time.Sleep(150 * time.Millisecond)

	runner.Abort()
	final := waitForStatus(t, runner, StatusAborted, 10*time.Second)
	assert.Less(t, final.FrameIndex, 20)
}

func TestRunner_RefusesASecondSession(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()
	req := Request{
		Root: dir, Object: "M31",
		Sequence: Sequence{Steps: []Step{
			{Filter: "L", Count: 5, ExposureUs: 100_000, Gain: 139, Bin: 1, Type: "light"},
		}},
	}
	_, err := runner.Start(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { runner.Abort() })

	_, err = runner.Start(context.Background(), req)
	assert.ErrorIs(t, err, ErrSessionRunning,
		"two sequences fighting over one filter wheel would ruin both")
}

func TestRunner_RejectsAnUnknownFilter(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M31",
		Sequence: Sequence{Steps: []Step{
			{Filter: "Zz", Count: 1, ExposureUs: 20_000, Gain: 139, Bin: 1, Type: "light"},
		}},
	})
	require.NoError(t, err)

	final := waitForStatus(t, runner, StatusFailed, 15*time.Second)
	assert.Contains(t, final.Error, "not in the wheel",
		"a typo'd filter must fail loudly, not silently shoot through whatever is in the beam")
}

// The filter must survive in THREE independent places — the folder, the file name and the FITS
// header — so a later processing run can never be wrong about which sub is which. This is the
// promise for a swapped 5-slot wheel, where nothing else records what was fitted that night.
func TestRunner_WritesTheFilterIntoTheFolderTheFileAndTheHeader(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()

	_, err := runner.Start(context.Background(), Request{
		Root: dir, Object: "M27",
		Sequence: Sequence{Steps: []Step{
			{Filter: "SII", Count: 1, ExposureUs: 20_000, Gain: 200, Bin: 1, Type: "light"},
			{Filter: "OIII", Count: 1, ExposureUs: 20_000, Gain: 200, Bin: 1, Type: "light"},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, waitForStatus(t, runner, StatusCompleted, 20*time.Second).Status)

	inv, err := inspect.Scan(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, inv.Frames, 2)

	byFilter := map[string]*inspect.Frame{}
	for _, fr := range inv.Frames {
		byFilter[fr.Filter] = fr
	}
	for _, want := range []string{"SII", "OIII"} {
		fr, ok := byFilter[want]
		require.True(t, ok, "%s frame must be scanned back under its own filter", want)
		assert.Contains(t, filepath.Dir(fr.Path), string(filepath.Separator)+want,
			"the filter must be a folder segment")
		assert.Contains(t, filepath.Base(fr.Path), "filter-"+want,
			"the filter must be in the file name")
	}
}

// A 5-slot wheel gets its filters swapped between sessions, and people label the slots however they
// like ("S2", "sulfur"). A sequence written in canonical names must still find them, or the run dies
// mid-night on a name mismatch that means nothing optically.
func TestRunner_ResolvesFilterAliasesAgainstTheWheelLabels(t *testing.T) {
	runner, _ := testRig(t)
	dir := t.TempDir()
	// Re-label the simulated wheel the way a user might have typed it.
	_, err := runner.client.ConnectWheel(context.Background(), "sim",
		[]string{"Lum", "red", "green", "blue", "H-alpha", "O3", "S2"})
	require.NoError(t, err)

	_, err = runner.Start(context.Background(), Request{
		Root: dir, Object: "M27",
		Sequence: Sequence{Steps: []Step{
			{Filter: "SII", Count: 1, ExposureUs: 20_000, Gain: 200, Bin: 1, Type: "light"},
		}},
	})
	require.NoError(t, err)
	final := waitForStatus(t, runner, StatusCompleted, 20*time.Second)
	assert.Equal(t, 1, final.FrameIndex,
		`a step asking for "SII" must resolve the slot labelled "S2"`)
}

// Where the run points has to reach the frame header. Without OBJCTRA/OBJCTDEC a later plate solve
// has no hint and falls back to a blind all-sky search — the difference between seconds and minutes
// per frame — and the coordinates are already sitting on the Request.
func TestSaveRequestFor_CarriesTheTargetCoordinates(t *testing.T) {
	r := &Runner{}
	step := Step{Filter: "L", Count: 1, ExposureUs: 20_000, Gain: 100, Bin: 1, Type: "light"}
	at := time.Date(2026, 8, 2, 22, 30, 0, 0, time.UTC)

	t.Run("a pointed run writes them", func(t *testing.T) {
		state := &runState{req: Request{Root: t.TempDir(), Object: "M31", RADeg: 10.6847, DecDeg: 41.2687}}

		got := r.saveRequestFor(state, step, at)

		assert.True(t, got.HasCoord)
		assert.InDelta(t, 10.6847, got.RADeg, 1e-9)
		assert.InDelta(t, 41.2687, got.DecDeg, 1e-9)
	})

	// A session started without coordinates must leave the cards OFF rather than writing 0h00m +00°00'.
	// A solver trusts a present hint, so a bogus one is worse than none: it searches the wrong sky.
	t.Run("an unpointed run writes no coordinates at all", func(t *testing.T) {
		state := &runState{req: Request{Root: t.TempDir(), Object: "M31"}}

		got := r.saveRequestFor(state, step, at)

		assert.False(t, got.HasCoord)
		assert.Zero(t, got.RADeg)
		assert.Zero(t, got.DecDeg)
	})

	// RA 0 is a real place (the vernal equinox), so a declination alone must still count as pointed.
	t.Run("a zero right ascension is still a pointing", func(t *testing.T) {
		state := &runState{req: Request{Root: t.TempDir(), DecDeg: 89.2641}}

		got := r.saveRequestFor(state, step, at)

		assert.True(t, got.HasCoord)
		assert.Zero(t, got.RADeg)
	})
}

// Flats are per-filter, darks are not — the folder layout must reflect that, because it is what
// makes a per-filter master flat possible while darks stay one filter-agnostic set.
func TestFrameFolder_Layout(t *testing.T) {
	tests := []struct {
		name string
		step Step
		want string
	}{
		{"light groups by filter", Step{Type: "light", Filter: "SII"}, "SII"},
		{"empty type is a light", Step{Filter: "Ha"}, "Ha"},
		{"mono light has no filter segment", Step{Type: "light"}, ""},
		{"flat is per-filter under flats", Step{Type: "flat", Filter: "OIII"}, filepath.Join("flats", "OIII")},
		{"flat with no filter", Step{Type: "flat"}, "flats"},
		{"dark is filter-agnostic", Step{Type: "dark", Filter: "SII"}, "darks"},
		{"bias is filter-agnostic", Step{Type: "bias"}, "bias"},
		{"darkflat is filter-agnostic", Step{Type: "darkflat"}, "darkflats"},
		{"a separator in the filter cannot escape the folder", Step{Type: "light", Filter: "../../etc"}, "etc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, frameFolder(tt.step))
		})
	}
}

func TestSequence_ValidateRejectsNonsense(t *testing.T) {
	tests := []struct {
		name string
		seq  Sequence
		want string
	}{
		{"no steps", Sequence{}, "at least one step"},
		{"zero count", Sequence{Steps: []Step{{Filter: "L", ExposureUs: 1000}}}, "count must be positive"},
		{"zero exposure", Sequence{Steps: []Step{{Filter: "L", Count: 1}}}, "exposure must be positive"},
		{"light with no filter", Sequence{Steps: []Step{{Count: 1, ExposureUs: 1000}}}, "needs a filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.seq.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	// A dark needs no filter — nothing is in the beam that matters.
	require.NoError(t, Sequence{Steps: []Step{
		{Count: 1, ExposureUs: 1000, Type: "dark"},
	}}.Validate())
}

// recordingRunner is a Runner wired to a recorder that remembers the last status it was told to
// store, so a test can ask what actually reached the database rather than what the runner believes.
type sessionRecorder struct {
	memRecorder
	mu       sync.Mutex
	statuses []Status
	fail     error
}

func (s *sessionRecorder) CreateSession(context.Context, Request, int) (int64, error) {
	return 7, nil
}

func (s *sessionRecorder) UpdateSession(_ context.Context, _ int64, status Status, _ Progress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.statuses = append(s.statuses, status)
	return nil
}

func (s *sessionRecorder) stored() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Status(nil), s.statuses...)
}

// Stop must close a session that has already failed.
//
// It did not: Abort only ever acted when the status was running or paused, and by the time a run has
// failed the loop has exited and r.cancel is nil — so nothing wrote the row, the logbook kept a
// phantom "running" night, and the only cure was restarting the engine. MEASURED twice in one
// evening on real sessions (10 and 12).
func TestRunner_AbortClosesASessionThatAlreadyFailed(t *testing.T) {
	rec := &sessionRecorder{}
	r := NewRunner(nil, rec)
	r.progress = Progress{SessionID: 42, Status: StatusFailed, Error: "the camera stopped answering"}

	got := r.Abort()

	assert.Equal(t, StatusFailed, got.Status, "a failed run did not become an aborted one")
	assert.Equal(t, []Status{StatusFailed}, rec.stored(),
		"the terminal state must reach the database, or the row stays live forever")
}

// A runner that never started anything has no row to close.
func TestRunner_AbortWritesNothingWithoutASession(t *testing.T) {
	rec := &sessionRecorder{}
	r := NewRunner(nil, rec)

	r.Abort()

	assert.Empty(t, rec.stored())
}

// When the write fails there must be something to read. It used to be discarded outright, which is
// why a session that never closed left no trace of why.
func TestRunner_ASessionThatCannotBeClosedSaysSo(t *testing.T) {
	rec := &sessionRecorder{fail: errors.New("connection reset by peer")}
	r := NewRunner(nil, rec)
	r.progress = Progress{SessionID: 42, Status: StatusFailed}

	r.Abort()

	assert.Contains(t, r.Progress().Message, "could not be closed")
	assert.Contains(t, r.Progress().Message, "connection reset by peer")
}
