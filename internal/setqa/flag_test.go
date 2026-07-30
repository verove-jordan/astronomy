package setqa

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// borderProbe fabricates a mono frame probe where 1σ of border excess equals 1% of sky, so the
// AbsBorderPct gate tracks the sigma values chosen by each case.
func borderProbe(path string, border, grad float64) FrameProbe {
	return FrameProbe{Path: path, Channels: []ChannelProbe{{
		Background: 0.1, NoiseSigma: 0.001,
		BorderSigma: border, BorderPct: border, WorstBorder: "left",
		GradSigma: grad, GradPct: grad,
	}}}
}

func lightSet(filter string, count int, expMs int64) inspect.Set {
	return inspect.Set{
		Key:                inspect.SetKey{Type: inspect.Light, Filter: filter, ExposureMs: expMs, Gain: 139, Bin: 1},
		Count:              count,
		TotalIntegrationMs: int64(count) * expMs,
	}
}

func sameProbes(n int, border, grad float64) []FrameProbe {
	probes := make([]FrameProbe, n)
	for i := range probes {
		probes[i] = borderProbe(fmt.Sprintf("f%d.fits", i), border, grad)
	}
	return probes
}

func TestFlagSets_Rules(t *testing.T) {
	opts := DefaultOptions()

	t.Run("relative outlier among three siblings", func(t *testing.T) {
		// 4σ is below the absolute floor (6σ) AND below stack visibility (4·√5 ≈ 8.9 < 10),
		// but far above the 0.5σ siblings.
		sets := []inspect.Set{lightSet("R", 5, 120000), lightSet("R", 5, 120000), lightSet("R", 5, 120000)}
		sets[1].Key.Session = "2026-06-14" // distinct IDs
		sets[2].Key.Session = "2026-06-15"
		probes := [][]FrameProbe{sameProbes(3, 0.5, 0), sameProbes(3, 0.5, 0), sameProbes(3, 4, 0)}
		reports := flagSets(sets, probes, opts)
		assert.False(t, reports[0].Flagged)
		assert.False(t, reports[1].Flagged)
		require.True(t, reports[2].Flagged)
		require.NotEmpty(t, reports[2].Reasons)
		assert.Equal(t, "outlier_vs_siblings", reports[2].Reasons[0].Code)
		assert.Greater(t, reports[2].Score, 0.0)
		assert.Less(t, reports[2].Score, 50.0) // below the floor → below the 50 mark
	})

	t.Run("lone set over the absolute floor", func(t *testing.T) {
		sets := []inspect.Set{lightSet("Ha", 20, 300000)}
		reports := flagSets(sets, [][]FrameProbe{sameProbes(4, 12, 0)}, opts)
		require.True(t, reports[0].Flagged)
		assert.Equal(t, "border_glow", reports[0].Reasons[0].Code)
		assert.Equal(t, "left", reports[0].Reasons[0].Border)
		assert.Greater(t, reports[0].Score, 50.0)
		assert.True(t, reports[0].Impact.EmptiesFilter)
		assert.Zero(t, reports[0].Impact.SNRFactor)
	})

	t.Run("lone weak glow stays clean", func(t *testing.T) {
		// 0.4σ per frame is under the 0.5σ consistency floor — noise, not signal.
		sets := []inspect.Set{lightSet("Ha", 20, 300000)}
		reports := flagSets(sets, [][]FrameProbe{sameProbes(4, 0.4, 0)}, opts)
		assert.False(t, reports[0].Flagged)
	})

	t.Run("subtle consistent glow flags via the stack rule", func(t *testing.T) {
		// 1.5σ per frame never trips the per-frame floor, but over 130 frames it integrates to
		// 1.5·√130 ≈ 17σ in the final — the user-visible "red halo after stretch" case.
		sets := []inspect.Set{lightSet("Ha", 130, 30000)}
		reports := flagSets(sets, [][]FrameProbe{sameProbes(4, 1.5, 0)}, opts)
		require.True(t, reports[0].Flagged)
		require.NotEmpty(t, reports[0].Reasons)
		assert.Equal(t, "stack_visible", reports[0].Reasons[0].Code)
		assert.Equal(t, "left", reports[0].Reasons[0].Border)
		assert.InDelta(t, 1.5*math.Sqrt(130), reports[0].StackedSigma, 1e-9)
		assert.Greater(t, reports[0].Score, 50.0)
	})

	t.Run("two-set ratio rule", func(t *testing.T) {
		sets := []inspect.Set{lightSet("R", 6, 120000), lightSet("R", 4, 120000)}
		sets[1].Key.Session = "2026-06-14"
		probes := [][]FrameProbe{sameProbes(3, 0.6, 0), sameProbes(3, 4, 0)}
		reports := flagSets(sets, probes, opts)
		assert.False(t, reports[0].Flagged)
		require.True(t, reports[1].Flagged)
		assert.Equal(t, "outlier_vs_siblings", reports[1].Reasons[0].Code)
	})

	t.Run("tight elevated group does not flag itself", func(t *testing.T) {
		// All four sets at 4σ (above floor/2): MAD is zero, but the 50% excess gate holds.
		sets := []inspect.Set{lightSet("L", 5, 60000), lightSet("L", 5, 60000), lightSet("L", 5, 60000), lightSet("L", 5, 60000)}
		for i := 1; i < 4; i++ {
			sets[i].Key.Session = fmt.Sprintf("2026-06-%02d", 10+i)
		}
		probes := [][]FrameProbe{sameProbes(3, 4, 0), sameProbes(3, 4, 0), sameProbes(3, 4, 0), sameProbes(3, 4, 0)}
		for _, rep := range flagSets(sets, probes, opts) {
			assert.False(t, rep.Flagged)
		}
	})

	t.Run("gradient-dominated floor names strong_gradient", func(t *testing.T) {
		sets := []inspect.Set{lightSet("L", 8, 60000)}
		reports := flagSets(sets, [][]FrameProbe{sameProbes(4, 1, 16)}, opts)
		require.True(t, reports[0].Flagged)
		assert.Equal(t, "strong_gradient", reports[0].Reasons[0].Code)
	})

	t.Run("under two probes means unmeasured", func(t *testing.T) {
		sets := []inspect.Set{lightSet("R", 10, 120000)}
		reports := flagSets(sets, [][]FrameProbe{sameProbes(1, 20, 0)}, opts)
		assert.False(t, reports[0].Measured)
		assert.False(t, reports[0].Flagged)
		assert.Zero(t, reports[0].Score)
	})
}

func TestFlagSets_ImpactMath(t *testing.T) {
	// Two R sets: 60% and 40% of the filter's integration.
	sets := []inspect.Set{lightSet("R", 6, 120000), lightSet("R", 4, 120000)}
	sets[1].Key.Session = "2026-06-14"
	probes := [][]FrameProbe{sameProbes(3, 0.5, 0), sameProbes(3, 12, 0)}
	reports := flagSets(sets, probes, DefaultOptions())

	imp := reports[1].Impact
	assert.Equal(t, "R", imp.Filter)
	assert.Equal(t, 10, imp.FilterFrames)
	assert.Equal(t, int64(10*120000), imp.FilterIntegrationMs)
	assert.Equal(t, 4, imp.LostFrames)
	assert.InDelta(t, 40.0, imp.LostIntegrationPct, 0.01)
	assert.InDelta(t, math.Sqrt(0.6), imp.SNRFactor, 1e-9)
	assert.False(t, imp.EmptiesFilter)
}

func TestFlagSets_ChannelImbalanceReason(t *testing.T) {
	redHalo := FrameProbe{Path: "osc.fits", Channels: []ChannelProbe{
		{Channel: "R", Background: 0.1, BorderSigma: 12, BorderPct: 12, WorstBorder: "left"},
		{Channel: "G", Background: 0.1, BorderSigma: 0.4},
		{Channel: "B", Background: 0.1, BorderSigma: 0.5},
	}}
	sets := []inspect.Set{lightSet("", 10, 30000)}
	reports := flagSets(sets, [][]FrameProbe{{redHalo, redHalo, redHalo}}, DefaultOptions())

	require.True(t, reports[0].Flagged)
	codes := make([]string, 0, len(reports[0].Reasons))
	for _, r := range reports[0].Reasons {
		codes = append(codes, r.Code)
	}
	assert.Contains(t, codes, "border_glow")
	assert.Contains(t, codes, "channel_imbalance")
}
