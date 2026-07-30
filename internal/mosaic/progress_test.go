package mosaic

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

func light(root, panel, filter string, expMs int64, dateMs int64, night, name string) inspect.Frame {
	return inspect.Frame{
		Path:       filepath.Join(root, panel, name),
		Type:       inspect.Light,
		Filter:     filter,
		ExposureMs: expMs,
		DateObsMs:  dateMs,
		Session:    night,
	}
}

func TestCountCaptured(t *testing.T) {
	root := filepath.Join("input", "M31")
	frames := []inspect.Frame{
		light(root, "p01", "L", 120000, 1000, "2026-07-20", "a.fit"),
		light(root, "p01", "L", 120000, 3000, "2026-07-20", "b.fit"),
		light(root, "p01", "L", 120000, 5000, "2026-07-21", "c.fit"), // second night
		light(root, "p01", "R", 60000, 2000, "2026-07-20", "d.fit"),
		light(root, "p02", "L", 120000, 9000, "2026-07-21", "e.fit"),
		// A dark in a panel folder must not count as capture progress.
		{Path: filepath.Join(root, "p02", "dark.fit"), Type: inspect.Dark, Filter: "L"},
		// A light outside any panel folder belongs to a single-pointing capture.
		{Path: filepath.Join(root, "single", "f.fit"), Type: inspect.Light, Filter: "L"},
	}

	got := CountCaptured(frames, root)

	require.Contains(t, got, "p01")
	require.Contains(t, got, "p02")
	assert.NotContains(t, got, "single", "lights outside a panel folder are not mosaic progress")

	assert.Equal(t, 3, got["p01"]["L"].Frames)
	assert.InDelta(t, 360, got["p01"]["L"].Seconds, 1e-9)
	assert.Equal(t, int64(5000), got["p01"]["L"].LastMs)
	assert.Equal(t, 2, got["p01"]["L"].Nights)
	assert.Equal(t, 1, got["p01"]["R"].Frames)
	assert.Equal(t, 1, got["p02"]["L"].Frames)
	assert.Equal(t, 1, got["p02"]["L"].Nights)
}

func TestCountCaptured_UnknownFilterIsStillCounted(t *testing.T) {
	root := "input"
	frames := []inspect.Frame{light(root, "p03", "", 30000, 0, "", "x.fit")}

	got := CountCaptured(frames, root)
	require.Contains(t, got, "p03")
	assert.Equal(t, 1, got["p03"][UnknownFilter].Frames)
	assert.Zero(t, got["p03"][UnknownFilter].Nights, "an undated frame contributes no night")
}

func TestTileDoneAndRemaining(t *testing.T) {
	targets := []CaptureTarget{
		{Filter: "L", Frames: 20},
		{Filter: "R", Frames: 10},
		{Filter: "G", Frames: 0}, // no goal — never blocks completion
	}
	tests := []struct {
		name     string
		progress map[string]FilterProgress
		wantDone bool
		wantLeft []string
	}{
		{
			name:     "nothing shot",
			progress: map[string]FilterProgress{},
			wantDone: false, wantLeft: []string{"L", "R"},
		},
		{
			name:     "partway",
			progress: map[string]FilterProgress{"L": {Frames: 20}, "R": {Frames: 3}},
			wantDone: false, wantLeft: []string{"R"},
		},
		{
			name:     "met",
			progress: map[string]FilterProgress{"L": {Frames: 20}, "R": {Frames: 10}},
			wantDone: true, wantLeft: nil,
		},
		{
			name:     "over-shot still counts as done",
			progress: map[string]FilterProgress{"L": {Frames: 41}, "R": {Frames: 12}},
			wantDone: true, wantLeft: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantDone, TileDone(tt.progress, targets))
			assert.Equal(t, tt.wantLeft, RemainingFilters(tt.progress, targets))
		})
	}

	t.Run("no targets means completion cannot be inferred", func(t *testing.T) {
		assert.False(t, TileDone(map[string]FilterProgress{"L": {Frames: 999}}, nil))
	})
}

func TestSortedPanels(t *testing.T) {
	p := TileProgress{"p10": nil, "p02": nil, "p01": nil}
	assert.Equal(t, []string{"p01", "p02", "p10"}, SortedPanels(p))
}
