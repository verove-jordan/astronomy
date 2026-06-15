package gimp

import (
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
