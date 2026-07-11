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
	// Saturation is boosted on a luminosity-masked copy so near-black chroma noise is never saturated into
	// blotches. The mask is a BAND-PASS (an explicit LUT, not a plain levels ramp): it protects the shadows
	// AND rolls the boost back off over the highlights so bright star cores/wings don't over-saturate.
	assert.Contains(t, s, "gimp-drawable-hue-saturation sat HUE-RANGE-ALL 0 0 15 0")
	assert.Contains(t, s, "gimp-layer-create-mask sat ADD-MASK-COPY")
	assert.Contains(t, s, "(gimp-drawable-curves-explicit m HISTOGRAM-VALUE 256 ")
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

func TestComposeScript_LumOpacity(t *testing.T) {
	res := &Result{Xcf: "/o/final.xcf", Tif: "/o/final.tif", Png: "/o/final.png"}
	base := Inputs{Base: "/s/base.tif", Lum: "/s/lum.tif", Color: true}

	// Below full opacity → an explicit set-opacity on the L layer, emitted after its luminance-mode line.
	lo := base
	lo.LumOpacity = 0.7
	s := composeScript(lo, nil, 0, 0, res)
	assert.Contains(t, s, "(gimp-layer-set-opacity lum 70)")
	assert.Less(t, strings.Index(s, "LAYER-MODE-LUMINANCE"), strings.Index(s, "(gimp-layer-set-opacity lum 70)"),
		"opacity must be set after the layer's luminance mode")

	// Full (1.0) or unset (0) → no opacity op (the L composites at 100%, byte-identical to before the knob).
	full := base
	full.LumOpacity = 1.0
	assert.NotContains(t, composeScript(full, nil, 0, 0, res), "gimp-layer-set-opacity lum")
	assert.NotContains(t, composeScript(base, nil, 0, 0, res), "gimp-layer-set-opacity lum") // unset (0)

	// No L layer → the opacity op never applies even if a value slips through.
	noLum := Inputs{Base: "/s/base.tif", Color: true, LumOpacity: 0.5}
	assert.NotContains(t, composeScript(noLum, nil, 0, 0, res), "gimp-layer-set-opacity lum")
}

func TestComposeScript_StarDesat(t *testing.T) {
	res := &Result{Xcf: "/o/final.xcf", Tif: "/o/final.tif", Png: "/o/final.png"}
	base := Inputs{Base: "/s/base.tif", Lum: "/s/lum.tif", Color: true}

	// On → a luminosity-masked desaturated copy, emitted after the saturation boost and before the
	// final highlight shoulder, so only bright star cores/wings lose chroma.
	on := base
	on.StarDesat = 0.6
	s := composeScript(on, nil, 0, 0.12, res)
	assert.Contains(t, s, "gimp-drawable-hue-saturation desat HUE-RANGE-ALL 0 0 -60 0")
	assert.Contains(t, s, "gimp-layer-create-mask desat ADD-MASK-COPY")
	// Ordering: after the saturation copy, before the per-channel highlight shoulder (when one is set).
	withShoulder := on
	withShoulder.HighlightKnee, withShoulder.HighlightCeil = 0.85, 0.92
	s2 := composeScript(withShoulder, nil, 0, 0.12, res)
	assert.Less(t, strings.Index(s2, "hue-saturation sat "), strings.Index(s2, "hue-saturation desat "),
		"star desat must run after the saturation boost")
	assert.Less(t, strings.Index(s2, "hue-saturation desat "), strings.Index(s2, "gimp-drawable-curves-explicit d HISTOGRAM-VALUE"),
		"star desat must run before the final highlight shoulder")

	// Off (0) → no desat op at all (byte-identical to before the knob).
	assert.NotContains(t, composeScript(base, nil, 0, 0.12, res), "desat")
	// Mono base → colour ops never emit, so no desat even if a value slips through.
	mono := Inputs{Base: "/s/base.tif", Color: false, StarDesat: 0.6}
	assert.NotContains(t, composeScript(mono, nil, 0, 0.12, res), "desat")
}

func TestStarDesatMaskLUT(t *testing.T) {
	const lo, hi = 0.50, 0.85
	lut := starDesatMaskLUT(lo, hi)
	assert.Len(t, lut, coreShoulderSamples)
	at := func(x float64) float64 { return lut[int(x*float64(coreShoulderSamples-1)+0.5)] }
	assert.InDelta(t, 0.0, at(0.30), 1e-9, "sky/mid-tone chroma untouched below lo")
	assert.InDelta(t, 1.0, at(0.95), 1e-9, "bright star cores fully desaturated above hi")
	assert.Greater(t, at(0.675), 0.0, "ramps within the band")
	assert.Less(t, at(0.675), 1.0, "ramps within the band")
	for i, v := range lut {
		assert.GreaterOrEqual(t, v, 0.0, "at i=%d", i)
		assert.LessOrEqual(t, v, 1.0, "at i=%d", i)
		if i > 0 {
			assert.GreaterOrEqual(t, v, lut[i-1], "monotonic at i=%d", i)
		}
	}
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

func TestSaturationMaskLUT(t *testing.T) {
	const shadLo, shadHi, hiLo, hiFloor = 0.12, 0.45, 0.70, 0.30
	lut := saturationMaskLUT(shadLo, shadHi, hiLo, hiFloor)
	assert.Len(t, lut, coreShoulderSamples)
	at := func(x float64) float64 { return lut[int(x*float64(coreShoulderSamples-1)+0.5)] }

	assert.InDelta(t, 0.0, at(0.05), 1e-9, "shadows fully protected (no boost)") // sky chroma noise
	assert.InDelta(t, 1.0, at(0.55), 1e-9, "mid/upper tones get the full boost") // extended-object colour
	assert.Less(t, at(0.95), 0.5, "highlights rolled back off so star cores don't over-saturate")
	assert.InDelta(t, hiFloor, at(1.0), 1e-9, "white asymptotes to the highlight floor")
	for i, v := range lut { // a mask is an opacity in [0,1]
		assert.GreaterOrEqual(t, v, 0.0, "at i=%d", i)
		assert.LessOrEqual(t, v, 1.0, "at i=%d", i)
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

func TestComposeScript_GreenTrimOnlyWhenUncalibrated(t *testing.T) {
	res := &Result{Xcf: "o.xcf", Tif: "o.tif", Png: "o.png"}
	uncal := composeScript(Inputs{Base: "b.tif", Color: true}, nil, 0, 0.1, res)
	assert.Contains(t, uncal, "HUE-RANGE-GREEN", "uncalibrated colour keeps the gentle green trim")

	cal := composeScript(Inputs{Base: "b.tif", Color: true, CalibratedColor: true}, nil, 0, 0.1, res)
	assert.NotContains(t, cal, "HUE-RANGE-GREEN", "a photometrically calibrated balance must not be green-trimmed (magenta tip)")
	assert.Contains(t, cal, "HUE-RANGE-ALL", "global saturation still applies to calibrated colour")
}
