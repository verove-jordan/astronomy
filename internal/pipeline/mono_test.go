package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

func TestEmitMonoOutputs_Gating(t *testing.T) {
	// Both flags off (and the degenerate nil cases) must be a no-op: no Siril/GIMP work and the colour
	// final's Outputs left untouched. The actual render paths need a live Siril runner and are exercised
	// end-to-end, not here.
	tests := []struct {
		name string
		opts Options
		res  *Result
	}{
		{"both flags off", Options{Preset: &mode.Preset{}}, &Result{Final: &postprocess.Result{Outputs: []string{"final.png"}}}},
		{"nil final", Options{Preset: &mode.Preset{EmitLuminanceMono: true}}, &Result{}},
		{"nil preset", Options{}, &Result{Final: &postprocess.Result{Outputs: []string{"final.png"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := 0
			if tt.res.Final != nil {
				before = len(tt.res.Final.Outputs)
			}
			emitMonoOutputs(context.Background(), tt.opts, map[string]string{"L": "aligned_L"}, tt.res, t.TempDir(), t.TempDir())
			if tt.res.Final != nil {
				assert.Len(t, tt.res.Final.Outputs, before, "outputs must be untouched")
				assert.Empty(t, tt.res.Final.MonoOutputs, "no mono outputs recorded")
			}
		})
	}
}

func TestAppendMono_OrderingAndKind(t *testing.T) {
	// The mono files are appended AFTER the colour final so final.png stays the FIRST PNG (the gallery
	// hero + Ken-Burns video both pick the first PNG); the typed entry carries the kind + file paths.
	res := &Result{Final: &postprocess.Result{Outputs: []string{"final.xcf", "final.tif", "final.png"}}}

	appendMono(res, &postprocess.MonoOutput{Png: "final_luminance.png", Tif: "final_luminance.tif"}, "luminance", "saved luminance mono")

	assert.Equal(t, []string{
		"final.xcf", "final.tif", "final.png", "final_luminance.png", "final_luminance.tif",
	}, res.Final.Outputs)

	firstPng := ""
	for _, o := range res.Final.Outputs {
		if strings.HasSuffix(o, ".png") {
			firstPng = o
			break
		}
	}
	assert.Equal(t, "final.png", firstPng, "colour final.png must remain the first PNG")

	if assert.Len(t, res.Final.MonoOutputs, 1) {
		assert.Equal(t, "luminance", res.Final.MonoOutputs[0].Kind)
		assert.Equal(t, "final_luminance.png", res.Final.MonoOutputs[0].Png)
		assert.Equal(t, "final_luminance.tif", res.Final.MonoOutputs[0].Tif)
	}
	assert.Contains(t, res.Final.Notes, "saved luminance mono")
}
