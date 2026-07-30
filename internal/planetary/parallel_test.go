package planetary

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

func TestForEachFrame_BoundedFanOut(t *testing.T) {
	const n, workers = 40, 3
	var inFlight, peak, calls atomic.Int64
	err := forEachFrame(context.Background(), n, workers, func(i int) error {
		cur := inFlight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		inFlight.Add(-1)
		calls.Add(1)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int64(n), calls.Load(), "every index must run exactly once")
	assert.LessOrEqual(t, peak.Load(), int64(workers), "fan-out must respect the worker bound")
}

func TestForEachFrame_ErrorStops(t *testing.T) {
	boom := errors.New("boom")
	var calls atomic.Int64
	err := forEachFrame(context.Background(), 100, 2, func(i int) error {
		calls.Add(1)
		if i == 3 {
			return boom
		}
		time.Sleep(time.Millisecond)
		return nil
	})
	require.ErrorIs(t, err, boom)
	assert.Less(t, calls.Load(), int64(100), "the first error must short-circuit the remaining work")
}

func TestForEachFrame_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := forEachFrame(ctx, 10, 4, func(i int) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
}

// orderedFrames must decode in parallel but hand frames to the consumer in strict index order (that
// order is what keeps the stack's float sums bit-identical), deliver nil for unreadable paths, and keep
// at most workers+2 decoded frames alive.
func TestOrderedFrames_StrictOrderAndNilForUnreadable(t *testing.T) {
	dir := t.TempDir()
	const n = 12
	paths := make([]string, n)
	for i := range paths {
		if i == 4 || i == 9 {
			paths[i] = filepath.Join(dir, fmt.Sprintf("missing_%02d.fits", i))
			continue
		}
		im := fits.NewImage(2, 2, 1)
		im.Pix[0][0] = float32(i)
		paths[i] = filepath.Join(dir, fmt.Sprintf("f_%02d.fits", i))
		require.NoError(t, im.WriteFITS(paths[i]))
	}

	var got []int
	var nils []int
	err := orderedFrames(context.Background(), paths, 3, func(i int, im *fits.Image) {
		got = append(got, i)
		if im == nil {
			nils = append(nils, i)
			return
		}
		assert.Equal(t, float32(i), im.Pix[0][0], "frame %d content", i)
	})
	require.NoError(t, err)
	want := make([]int, n)
	for i := range want {
		want[i] = i
	}
	assert.Equal(t, want, got, "consumption must be strict index order")
	assert.Equal(t, []int{4, 9}, nils, "unreadable frames arrive as nil")
}

func TestOrderedFrames_ContextCancel(t *testing.T) {
	dir := t.TempDir()
	im := fits.NewImage(2, 2, 1)
	p := filepath.Join(dir, "f.fits")
	require.NoError(t, im.WriteFITS(p))
	paths := []string{p, p, p, p}

	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	err := orderedFrames(ctx, paths, 2, func(i int, im *fits.Image) {
		once.Do(cancel) // cancel mid-stream: the pipeline must stop, not hang
	})
	require.ErrorIs(t, err, context.Canceled)
}

// The per-frame calibration must be independent: one bad frame passes through on its ORIGINAL path with
// a single aggregated note, while every other frame calibrates — positional out[i]↔frames[i] holds.
func TestCalibrateChannel_PerFrameFailurePassesThrough(t *testing.T) {
	f := newCalibFixture(t)
	missing := filepath.Join(f.chDir, "vid_99999.fits") // never written → read fails
	frames := []string{f.frames[0], missing, f.frames[1]}
	out, notes, err := calibrateChannel(context.Background(), frames, "L", f.inv(), f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assertCalibrated(t, out[0])
	assert.Equal(t, missing, out[1], "the failed frame keeps its original path")
	assertCalibrated(t, out[2])
	assert.True(t, anyNoteContains(notes, "calibration failed for 1/3"), "notes: %v", notes)
	assert.True(t, anyNoteContains(notes, "calibrated L"), "the channel still reports its calibration: %v", notes)
}

// classifyFolderChannels must merge every Import-selected root: lights in one folder, darks/flats in
// siblings (job #244's exact layout) — the cal frames must land in the inventory for the masters build
// while the light groups stay identical to a single-root scan.
func TestClassifyFolderChannels_MultiRoot(t *testing.T) {
	lightsDir, darksDir := t.TempDir(), t.TempDir()
	writeFrame := func(dir, name string, sidecar string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("tiff"), 0o644))
		require.NoError(t, os.WriteFile(p+".txt", []byte(sidecar), 0o644))
		return p
	}
	// Two filters' worth of lights via filename filter tokens (colour path needs ≥2 distinct filters).
	writeFrame(lightsDir, "filter_L_0001.tif", "EFW Slot = 1(Alias: L)\nExposure = 10ms\nGain = 0\n")
	writeFrame(lightsDir, "filter_R_0001.tif", "EFW Slot = 2(Alias: R)\nExposure = 10ms\nGain = 0\n")
	darkSub := filepath.Join(darksDir, "darks")
	require.NoError(t, os.MkdirAll(darkSub, 0o755))
	writeFrame(darkSub, "d_0001.tif", "Exposure = 10ms\nGain = 0\n")

	in, err := classifyFolderChannels(context.Background(), []string{lightsDir, darksDir})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"L", "R"}, in.order)
	require.NotNil(t, in.inv)
	darks := in.inv.SetsOfType(inspect.Dark)
	require.Len(t, darks, 1, "the sibling root's dark set must be in the merged inventory")
	assert.Equal(t, 1, darks[0].Count)
}
