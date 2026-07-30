package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// TestParseStagePreview_SessionRoundTrip: the night token encodes into the filename and parses back;
// legacy 2/3-token names keep parsing exactly as before.
func TestParseStagePreview_SessionRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		file string
		want postprocess.StagePreview
	}{
		{"legacy run-level", "300_combined.png",
			postprocess.StagePreview{Index: 300, Stage: "combined"}},
		{"legacy per-channel", "100_stacked_L.png",
			postprocess.StagePreview{Index: 100, Stage: "stacked", Filter: "L"}},
		{"per-session prenorm", "1402_prenorm_Ha_n2023-02-27.png",
			postprocess.StagePreview{Index: 1402, Stage: "prenorm", Filter: "Ha", Session: "2023-02-27"}},
		{"per-session normalized", "1403_normalized_Ha_n2023-03-15.png",
			postprocess.StagePreview{Index: 1403, Stage: "normalized", Filter: "Ha", Session: "2023-03-15"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp, ok := parseStagePreview("/runs/previews/" + tc.file)
			require.True(t, ok)
			sp.PngPath = "" // path is positional, not under test
			assert.Equal(t, tc.want, sp)
		})
	}

	t.Run("a filter that merely ends in n-digits is NOT a night", func(t *testing.T) {
		sp, ok := parseStagePreview("/p/100_stacked_n42.png")
		require.True(t, ok)
		assert.Empty(t, sp.Session)
		assert.Equal(t, "n42", sp.Filter, "only a full YYYY-MM-DD night token strips")
	})
}

// TestSessionPreviewIndex_Unique: indices never collide across parallel channels and a channel's
// groups, sort after the main strip (>= ordSession), and are deterministic.
func TestSessionPreviewIndex_Unique(t *testing.T) {
	seen := map[int]bool{}
	for chanSlot := 2; chanSlot <= 6; chanSlot++ { // realistic run-wide channel slots
		for gi := 0; gi < 4; gi++ {
			for _, normalized := range []bool{false, true} {
				idx := sessionPreviewIndex(chanSlot, gi, normalized)
				assert.GreaterOrEqual(t, idx, ordSession, "session block sorts after the main strip")
				assert.False(t, seen[idx], "collision at chanSlot=%d gi=%d normalized=%v", chanSlot, gi, normalized)
				seen[idx] = true
			}
		}
	}
}

// TestIsNightToken guards the filename decoder against filter-tag false positives.
func TestIsNightToken(t *testing.T) {
	assert.True(t, isNightToken("2023-02-27"))
	assert.False(t, isNightToken("42"))
	assert.False(t, isNightToken("2023-2-27"))
	assert.False(t, isNightToken("2023-02-27x"))
	assert.False(t, isNightToken("Ha"))
}
