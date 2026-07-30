package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyAlignPolicy covers the per-channel degradation contract: a failed accent channel drops out,
// a failed L drops to an RGB-only combine, and a failed R/G/B primary is the ONLY case that reverts
// the whole set to the unaligned masters. Every decision leaves a warning.
func TestApplyAlignPolicy(t *testing.T) {
	unaligned := map[string]string{
		"L": "master_L", "R": "master_R", "G": "master_G", "B": "master_B", "Ha": "master_Ha",
	}
	tests := []struct {
		name    string
		aligned map[string]string
		failed  []string
		want    map[string]string
		warn    string
	}{
		{
			"failed accent is omitted",
			map[string]string{"L": "aligned_L", "R": "aligned_R", "G": "aligned_G", "B": "aligned_B"},
			[]string{"Ha"},
			map[string]string{"L": "aligned_L", "R": "aligned_R", "G": "aligned_G", "B": "aligned_B"},
			"omitted from the composite",
		},
		{
			"failed L drops to RGB-only",
			map[string]string{"R": "aligned_R", "G": "aligned_G", "B": "aligned_B"},
			[]string{"L"},
			map[string]string{"R": "aligned_R", "G": "aligned_G", "B": "aligned_B"},
			"without luminance",
		},
		{
			"failed primary reverts the whole set",
			map[string]string{"L": "aligned_L", "R": "aligned_R", "B": "aligned_B"},
			[]string{"G"},
			unaligned,
			"combining unaligned channels",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := &Result{}
			got := applyAlignPolicy(Options{}, tt.aligned, unaligned, tt.failed, res)
			assert.Equal(t, tt.want, got)
			if assert.NotEmpty(t, res.Warnings) {
				assert.Contains(t, res.Warnings[len(res.Warnings)-1], tt.warn)
			}
		})
	}
}

// TestCollectAligned_SplitsRegisteredFromFailed checks the r_ch_N harvest: channels with a registered
// output are copied to outDir as aligned_<tag>.fits, the rest are reported as failed (no all-or-nothing).
func TestCollectAligned_SplitsRegisteredFromFailed(t *testing.T) {
	alignDir, outDir := t.TempDir(), t.TempDir()
	// ordered = [L, R]: only L's registered frame exists.
	require.NoError(t, os.WriteFile(filepath.Join(alignDir, "r_ch_00001.fits"), []byte("fits"), 0o644))

	res := &Result{}
	aligned, failed := collectAligned(alignDir, outDir, []string{"L", "R"}, res)

	assert.Equal(t, map[string]string{"L": "aligned_L"}, aligned)
	assert.Equal(t, []string{"R"}, failed)
	assert.FileExists(t, filepath.Join(outDir, "aligned_L.fits"))
	assert.Empty(t, res.Warnings, "a missing registration is policy's business, not a warning here")
}
