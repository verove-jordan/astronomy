package gimp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComposeScript_HaLRGB(t *testing.T) {
	in := Inputs{Base: "/s/base.tif", Lum: "/s/lum.tif", Ha: "/s/ha.tif", Color: true}
	res := &Result{Xcf: "/o/final.xcf", Tif: "/o/final.tif", Png: "/o/final.png"}
	s := composeScript(in, []float64{0, 0, 0.5, 0.55, 1, 1}, 0.30, 0.15, res)

	assert.Contains(t, s, `(gimp-file-load RUN-NONINTERACTIVE "/s/base.tif"`)
	assert.Contains(t, s, `(gimp-file-load-layer RUN-NONINTERACTIVE image "/s/lum.tif"`)
	assert.Contains(t, s, "LAYER-MODE-LUMINANCE")
	assert.Contains(t, s, `(gimp-file-load-layer RUN-NONINTERACTIVE image "/s/ha.tif"`)
	assert.Contains(t, s, "LAYER-MODE-SCREEN")
	assert.Contains(t, s, "(gimp-layer-set-opacity ha 30)")
	assert.Contains(t, s, "gimp-drawable-curves-spline d HISTOGRAM-VALUE 6 #(0.0000 0.0000 0.5000 0.5500 1.0000 1.0000)")
	assert.Contains(t, s, "gimp-drawable-hue-saturation d HUE-RANGE-ALL 0 0 15 0")
	assert.Contains(t, s, `"/o/final.xcf"`)
	assert.Contains(t, s, `"/o/final.tif"`)
	assert.Contains(t, s, `"/o/final.png"`)
}

func TestComposeScript_CoreHighlightShoulder(t *testing.T) {
	res := &Result{Xcf: "/o/final.xcf", Tif: "/o/final.tif", Png: "/o/final.png"}

	// Enabled: an explicit-LUT roll-off on the L luminance, emitted on `lum` before LAYER-MODE-LUMINANCE.
	on := Inputs{Base: "/s/base.tif", Lum: "/s/lum.tif", Color: true, CoreHighlightKnee: 0.72, CoreHighlightCeil: 0.85}
	s := composeScript(on, nil, 0, 0, res)
	mark := "(gimp-drawable-curves-explicit lum HISTOGRAM-VALUE 256 "
	assert.Contains(t, s, mark)
	assert.Less(t, strings.Index(s, mark), strings.Index(s, "LAYER-MODE-LUMINANCE"), "shoulder must run before the luminance blend")

	// Disabled (ceil 0) and invalid (knee >= ceil) → no shoulder.
	off := Inputs{Base: "/s/base.tif", Lum: "/s/lum.tif", Color: true}
	assert.NotContains(t, composeScript(off, nil, 0, 0, res), "gimp-drawable-curves-explicit")
	bad := Inputs{Base: "/s/base.tif", Lum: "/s/lum.tif", Color: true, CoreHighlightKnee: 0.9, CoreHighlightCeil: 0.85}
	assert.NotContains(t, composeScript(bad, nil, 0, 0, res), "gimp-drawable-curves-explicit")
}

func TestCoreShoulderLUT(t *testing.T) {
	const knee, ceil = 0.72, 0.85
	lut := coreShoulderLUT(knee, ceil)
	assert.Len(t, lut, coreShoulderSamples)
	for i, v := range lut {
		x := float64(i) / float64(coreShoulderSamples-1)
		if x <= knee {
			assert.InDelta(t, x, v, 1e-9, "must be exact identity below the knee at x=%.3f", x) // outer tones untouched
		} else {
			assert.Less(t, v, x, "must compress above the knee at x=%.3f", x)
			assert.Less(t, v, ceil, "must stay under the ceiling")
		}
		if i > 0 {
			assert.GreaterOrEqual(t, v, lut[i-1], "must be monotonic") // no tonal inversions
		}
	}
}

func TestComposeScript_MonoNoSaturation(t *testing.T) {
	in := Inputs{Base: "/s/base.tif", Color: false}
	res := &Result{Xcf: "/o/final.xcf", Tif: "/o/final.tif", Png: "/o/final.png"}
	s := composeScript(in, nil, 0, 0.2, res)

	assert.NotContains(t, s, "gimp-file-load-layer")         // no L/Ha layers
	assert.NotContains(t, s, "gimp-drawable-hue-saturation") // mono → no saturation
}

func TestSFEscaping(t *testing.T) {
	assert.Equal(t, `"a/b.tif"`, sf("a/b.tif"))
	assert.Equal(t, `"a\"b"`, sf(`a"b`))
}

func TestFloatVec(t *testing.T) {
	assert.Equal(t, "#(0.0000 1.0000)", floatVec([]float64{0, 1}))
}

func TestClamp(t *testing.T) {
	assert.Equal(t, 0.0, clamp01(-1))
	assert.Equal(t, 1.0, clamp01(2))
	assert.Equal(t, 0.5, clamp01(0.5))
}
