package job

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/postprocess"
)

func TestRunDirFromResult(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"deepsky output_dir", `{"output_dir":"/out/M31/run1"}`, "/out/M31/run1"},
		{"planetary out_base (no output_dir)", `{"out_base":"/out/Moon/run2/Moon_stack"}`, "/out/Moon/run2"},
		{"planetary run (output_dir + out_base)", `{"output_dir":"/out/Moon/20260702_010203","out_base":"/out/Moon/20260702_010203/Moon_stack"}`, "/out/Moon/20260702_010203"},
		{"output_dir preferred over out_base", `{"output_dir":"/out/a","out_base":"/out/b/x_stack"}`, "/out/a"},
		{"neither present", `{"object":"M31"}`, ""},
		{"empty", ``, ""},
		{"invalid json", `not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, runDirFromResult([]byte(tt.raw)))
		})
	}
}

func TestIterSummary(t *testing.T) {
	rec := &postprocess.IterationRecord{
		Index: 1, Tier: "B", CombinedScore: 7.4, DetScore: 7.1, ModelScore: 7.9,
		Reasoning: "green cast reduced",
		Defects: []postprocess.Defect{
			{Kind: "green_cast", Severity: "medium"},
			{Kind: "soft_stars", Severity: "low"},
		},
	}
	s := iterSummary(rec)
	assert.Contains(t, s, "Pass 2") // Index+1 → 1-based pass number
	assert.Contains(t, s, "tier B")
	assert.Contains(t, s, "7.4/10")
	assert.Contains(t, s, "green cast reduced")
	assert.Contains(t, s, "green_cast (medium)")
	assert.Contains(t, s, "soft_stars (low)")
}
