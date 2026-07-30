package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestApplyParamPatch_UnionCanvasAliases(t *testing.T) {
	tests := []struct {
		name     string
		params   string
		wantOn   bool
		wantFill string
	}{
		{"current key", `{"union_canvas": true, "union_canvas_fill": "fill"}`, true, "fill"},
		{"legacy alias still applies", `{"mosaic": true, "mosaic_fill": "fill"}`, true, "fill"},
		{"current key wins over the alias", `{"mosaic": true, "union_canvas": false}`, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mode.For(mode.Deepsky)
			res, err := ApplyParamPatch(&p, json.RawMessage(tt.params))
			require.NoError(t, err)
			assert.Empty(t, res.Ignored, "both spellings are known keys")
			assert.Equal(t, tt.wantOn, p.Mosaic)
			assert.Equal(t, tt.wantFill, p.MosaicFill)
		})
	}
}

func TestApplyParamPatch_MosaicModeKnobs(t *testing.T) {
	p := mode.For(mode.Mosaic)
	res, err := ApplyParamPatch(&p, json.RawMessage(
		`{"overlap_expected": 0.3, "feather_frac": 2.5, "canvas_crop": "union", "photom_match": "bogus", "min_panel_frames": 99, "saturation": 0.2}`))
	require.NoError(t, err)
	assert.Empty(t, res.Ignored)
	assert.Equal(t, "C", res.Tier, "assembler knobs are tier C")
	assert.InDelta(t, 0.3, p.MosaicOverlapExpected, 1e-9)
	assert.InDelta(t, 1.0, p.MosaicFeatherFrac, 1e-9, "clamped to the range max")
	assert.Equal(t, "union", p.MosaicCanvasCrop)
	assert.Equal(t, "gain_offset", p.MosaicPhotomMatch, "invalid enum keeps the default")
	assert.Equal(t, 50, p.MosaicMinPanelFrames, "clamped")
	assert.InDelta(t, 0.2, p.Saturation, 1e-9, "deepsky finish surface applies too")

	// The mode preset must keep the union-canvas machinery off — the assembler owns placement.
	assert.False(t, p.Mosaic)
	assert.False(t, p.CoverageCrop)
}

func TestMosaicMode_FinishOnlyPatchStaysCheap(t *testing.T) {
	p := mode.For(mode.Mosaic)
	res, err := ApplyParamPatch(&p, json.RawMessage(`{"saturation": 0.25}`))
	require.NoError(t, err)
	assert.NotEqual(t, "C", res.Tier, "a finish-only patch must not force a re-assembly")
}
