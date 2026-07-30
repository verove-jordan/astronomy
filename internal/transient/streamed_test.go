package transient

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// writeMemFrames writes a memFrame sequence to dir and returns the paths (streamed variant works
// on files).
func writeMemFrames(t *testing.T, dir string, n int, amp func(i int) float32) []string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("r_light_%05d.fits", i+1))
		require.NoError(t, memFrame(i, amp(i)).WriteFITS(p))
		paths[i] = p
	}
	return paths
}

// TestMaskCrossFrameStreamed_MatchesInMemoryOnFullBasis: with the basis covering every frame the
// streamed pass must reproduce the in-memory pass bit for bit — same summary, same pixels.
func TestMaskCrossFrameStreamed_MatchesInMemoryOnFullBasis(t *testing.T) {
	oneTrail := func(i int) float32 {
		if i == 0 {
			return 0.6
		}
		return 0
	}
	dir := t.TempDir()
	streamedPaths := writeMemFrames(t, filepath.Join(dir, "s"), 8, oneTrail)

	mem := make([]*fits.Image, 8)
	for i := range mem {
		mem[i] = memFrame(i, oneTrail(i))
	}
	memRep, err := MaskCrossFrameValidated(mem, 3.0, nil)
	require.NoError(t, err)

	strRep, err := MaskCrossFrameStreamed(streamedPaths, 3.0, 100, nil)
	require.NoError(t, err)

	assert.Equal(t, memRep.Summary(), strRep.Summary(), "full-basis streamed == in-memory")
	got, err := fits.ReadImage(streamedPaths[0])
	require.NoError(t, err)
	assert.Equal(t, mem[0].Pix[0], got.Pix[0], "the cleaned trail frame is pixel-identical")
}

// TestMaskCrossFrameStreamed_SmallBasisCleansTrail: a 5-frame basis over 10 frames still detects
// and paints the one-frame streak — bounded memory does not cost the mask its job.
func TestMaskCrossFrameStreamed_SmallBasisCleansTrail(t *testing.T) {
	dir := t.TempDir()
	paths := writeMemFrames(t, dir, 10, func(i int) float32 {
		if i == 3 { // NOT in the evenIndices(10,5) basis {0,2,4,6,8} — the streamed-only path
			return 0.6
		}
		return 0
	})
	rep, err := MaskCrossFrameStreamed(paths, 3.0, 5, nil)
	require.NoError(t, err)
	s := rep.Summary()
	assert.GreaterOrEqual(t, s.Segments, 1, "the one-frame streak is accepted vs the basis")
	assert.Equal(t, 0, s.Rejected)
	got, err := fits.ReadImage(paths[3])
	require.NoError(t, err)
	assert.Less(t, maxAlongDiagonal(got), float32(0.2), "streak painted down to ~background")
	assert.InDelta(t, starVal, got.Pix[0][starY*seqW+starX], 0.05, "static star preserved")
}

// TestMaskCrossFrameStreamed_SubsetBasisRejectsFixedPattern: the fixed-pattern discriminator holds
// against a subset basis. The basis must keep enough witnesses that the elevated-fraction estimate
// doesn't quantize below the 30% threshold for a borderline pattern (4/10 hot ≈ 33%): with 8 of 10
// frames resident every hot candidate still reads ≥3/7 elevated. The pipeline's budget-derived
// basis is dozens of frames, far from this resolution limit.
func TestMaskCrossFrameStreamed_SubsetBasisRejectsFixedPattern(t *testing.T) {
	dir := t.TempDir()
	paths := writeMemFrames(t, dir, 10, func(i int) float32 {
		if i < 4 {
			return 0.6
		}
		return 0
	})
	rep, err := MaskCrossFrameStreamed(paths, 3.0, 8, nil)
	require.NoError(t, err)
	s := rep.Summary()
	assert.Positive(t, s.Rejected, "the recurring streak is rejected as fixed pattern")
	assert.Equal(t, 0, s.Segments)
}

// TestMaskCrossFrameStreamed_RecurringPassOnSubsetBasis: the recurring-corridor pass works from the
// basis frames alone — the shared track is detected on the BASIS mean residual, and frames outside
// the basis (here 4 and 9, dropped by evenIndices(10,8)) still get their own marching stretch
// repaired, because the lit-window test runs on every streamed frame's own residual.
func TestMaskCrossFrameStreamed_RecurringPassOnSubsetBasis(t *testing.T) {
	dir := t.TempDir()
	paths := writeMemFrames(t, dir, 10, func(int) float32 { return 0 })
	for i := 0; i < 10; i++ { // re-write with marching spans (writeMemFrames only knows full streaks)
		im, err := fits.ReadImage(paths[i])
		require.NoError(t, err)
		a0 := 20 + 27*float64(i)
		addStreakSpan(im.Pix[0], seqW, seqH, 0.6, a0, a0+30)
		require.NoError(t, im.OverwriteData(paths[i]))
	}
	rep, err := MaskCrossFrameStreamed(paths, 3.0, 8, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, rep.Summary().Recurring, 1, "the shared track is found on the basis mean")
	got, err := fits.ReadImage(paths[4]) // NOT in evenIndices(10,8) = {0,1,2,3,5,6,7,8}
	require.NoError(t, err)
	mid := int(20 + 27*4 + 15)
	assert.Less(t, got.Pix[0][mid*seqW+mid], float32(0.2), "a non-basis frame's stretch is painted down")
	assert.InDelta(t, starVal, got.Pix[0][starY*seqW+starX], 0.05, "static star preserved")
}

func TestEvenIndices(t *testing.T) {
	assert.Equal(t, []int{0, 1, 2}, evenIndices(3, 5), "k >= n keeps every index")
	assert.Equal(t, []int{0, 2, 4, 6, 8}, evenIndices(10, 5))
	assert.Len(t, evenIndices(129, 33), 33)
}

func TestMemBudget_EnvOverride(t *testing.T) {
	t.Setenv("ASTRO_TRAIL_MASK_MEM_GB", "2")
	assert.Equal(t, int64(2<<30), MemBudget())
}
