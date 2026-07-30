package pipeline

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// capCalibSets bounds the frames feeding each calibration master (hundreds of 30 MB lunar cal TIFFs
// would convert to tens of GiB): caps must be deterministic and evenly spaced, keep set bookkeeping
// consistent, and leave light sets and small cal sets untouched.
func TestCapCalibSets(t *testing.T) {
	frames := func(n int, prefix string) []*inspect.Frame {
		out := make([]*inspect.Frame, n)
		for i := range out {
			out[i] = &inspect.Frame{Path: fmt.Sprintf("%s_%04d.tif", prefix, i), ExposureMs: 10}
		}
		return out
	}
	inv := &inspect.Inventory{Sets: []inspect.Set{
		{Key: inspect.SetKey{Type: inspect.Light, Filter: "L"}, Frames: frames(200, "light"), Count: 200},
		{Key: inspect.SetKey{Type: inspect.Flat, Filter: "L"}, Frames: frames(230, "flat"), Count: 230},
		{Key: inspect.SetKey{Type: inspect.Dark}, Frames: frames(40, "dark"), Count: 40},
	}}

	got := capCalibSets(inv, 64)

	require.Len(t, got.Sets, 3)
	assert.Len(t, got.Sets[0].Frames, 200, "light sets pass through untouched")
	assert.Len(t, got.Sets[1].Frames, 64, "large flat set capped")
	assert.Equal(t, 64, got.Sets[1].Count, "Count must follow the cap")
	assert.Equal(t, int64(64*10), got.Sets[1].TotalIntegrationMs)
	assert.Len(t, got.Sets[2].Frames, 40, "small cal sets pass through untouched")

	// Deterministic even spacing over the whole set: first frame kept, last picks near the tail.
	assert.Equal(t, "flat_0000.tif", got.Sets[1].Frames[0].Path)
	assert.Equal(t, got.Sets[1].Frames, capCalibSets(inv, 64).Sets[1].Frames, "same input → same subset")
	last := got.Sets[1].Frames[63].Path
	assert.Greater(t, last, "flat_0220.tif", "subset must span to the tail of the set, got %s", last)

	// The original inventory must not be mutated (the run keeps matching against the full sets).
	assert.Len(t, inv.Sets[1].Frames, 230)
	assert.Equal(t, 230, inv.Sets[1].Count)
}
