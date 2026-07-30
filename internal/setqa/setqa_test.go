package setqa

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

func analyzeFrames(filter string, n int) ([]*inspect.Frame, inspect.Set) {
	frames := make([]*inspect.Frame, n)
	for i := range frames {
		frames[i] = &inspect.Frame{
			Path: fmt.Sprintf("%s_%03d.fits", strings.ToLower(filter), i),
			Type: inspect.Light, Filter: filter, ExposureMs: 120000, Gain: 139, BinX: 1,
		}
	}
	set := inspect.Set{
		Key:                inspect.SetKey{Type: inspect.Light, Filter: filter, ExposureMs: 120000, Gain: 139, Bin: 1},
		Frames:             frames,
		Count:              n,
		TotalIntegrationMs: int64(n) * 120000,
	}
	return frames, set
}

// TestAnalyze_EndToEnd runs the full pipeline over an injected loader: an R set with a synthetic
// left halo among clean L frames must flag, sampling must respect the caps, and nothing touches
// the disk.
func TestAnalyze_EndToEnd(t *testing.T) {
	rFrames, rSet := analyzeFrames("R", 12)
	lFrames, lSet := analyzeFrames("L", 12)
	inv := &inspect.Inventory{Sets: []inspect.Set{rSet, lSet}}
	inv.Frames = append(inv.Frames, rFrames...)
	inv.Frames = append(inv.Frames, lFrames...)

	opts := DefaultOptions()
	opts.Load = func(path string) (*fits.Image, error) {
		if strings.HasPrefix(path, "r_") {
			return synthImage(testW, testH, 0.10, 0.002, 7, leftHalo(0.05, 40)), nil
		}
		return synthImage(testW, testH, 0.10, 0.002, 8, nil), nil
	}

	rep, err := Analyze(context.Background(), inv, opts)
	require.NoError(t, err)
	require.Len(t, rep.Sets, 2)
	assert.Equal(t, 1, rep.Flagged)

	byFilter := map[string]SetReport{}
	for _, s := range rep.Sets {
		byFilter[s.Key.Filter] = s
	}
	r, l := byFilter["R"], byFilter["L"]
	assert.True(t, r.Flagged)
	assert.True(t, r.Measured)
	assert.Equal(t, 8, r.Sampled) // 12 frames capped at MaxProbesPerSet
	assert.True(t, strings.HasPrefix(r.PreviewFrame, "r_"))
	assert.NotEmpty(t, r.Reasons)
	assert.False(t, l.Flagged)
	assert.Less(t, l.Score, 50.0)
}

func TestAnalyze_UnreadableFramesDegrade(t *testing.T) {
	frames, set := analyzeFrames("R", 6)
	inv := &inspect.Inventory{Sets: []inspect.Set{set}, Frames: frames}

	opts := DefaultOptions()
	opts.Load = func(path string) (*fits.Image, error) { return nil, fmt.Errorf("gone to S3") }

	rep, err := Analyze(context.Background(), inv, opts)
	require.NoError(t, err)
	require.Len(t, rep.Sets, 1)
	assert.False(t, rep.Sets[0].Measured)
	assert.False(t, rep.Sets[0].Flagged)
	assert.NotEmpty(t, rep.Warnings)
}

func TestAnalyze_NoLightSets(t *testing.T) {
	rep, err := Analyze(context.Background(), &inspect.Inventory{}, DefaultOptions())
	require.NoError(t, err)
	assert.Empty(t, rep.Sets)
	assert.Zero(t, rep.Flagged)
}
