package photom

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// rampCurve builds a reference FrameCurve from a monotonic [0,1] ramp of n pixels, exercising the real
// MeasureImage path so the fit tests share the production measurement.
func rampCurve(n int) FrameCurve {
	im := fits.NewImage(n, 1, 1)
	for i := 0; i < n; i++ {
		im.Pix[0][i] = float32(i) / float32(n-1)
	}
	return MeasureImage(im)
}

// affineFrame returns a FrameCurve whose probes are the exact inverse affine (ref-offset)/scale of ref,
// so an ideal fit recovers scale and offset.
func affineFrame(ref FrameCurve, scale, offset float64) FrameCurve {
	var f FrameCurve
	for i := range CurveQ {
		f.Q[i] = (ref.Q[i] - offset) / scale
	}
	f.Bg = f.Q[bgIdx]
	return f
}

func TestFitCurves_ExactAffineRecovery(t *testing.T) {
	ref := rampCurve(1000)
	frame := affineFrame(ref, 1.8, 0.02)

	tr := FitCurves(frame, ref, Meta{}, Meta{})

	assert.InDelta(t, 1.8, tr.Scale, 1e-9, "Theil–Sen recovers the exact slope")
	assert.InDelta(t, 0.02, tr.Offset, 1e-9, "median offset recovers the exact intercept")
	assert.False(t, tr.Clamped)
	assert.False(t, tr.MetaDisagree)
	assert.Less(t, tr.Resid, 1e-6, "an exact affine fit has ~zero residual")
}

func TestFitCurves_RobustToSaturatedProbe(t *testing.T) {
	ref := rampCurve(1000)
	frame := affineFrame(ref, 1.8, 0.02)
	frame.Q[fitHiIdx] *= 3 // corrupt the P97.5 probe (star saturation)

	tr := FitCurves(frame, ref, Meta{}, Meta{})

	assert.InDelta(t, 1.8, tr.Scale, 1.8*0.01, "Theil–Sen stays within 1% despite one corrupt probe")
}

func TestFitCurves_ClampsHighScale(t *testing.T) {
	ref := rampCurve(1000)
	frame := affineFrame(ref, 8.0, 0) // slope 8 → out of [0.2, 5.0]

	tr := FitCurves(frame, ref, Meta{}, Meta{})

	assert.Equal(t, scaleMax, tr.Scale, "scale clamped to the upper bound")
	assert.True(t, tr.Clamped)
}

func TestFitCurves_MetaDisagreeZWOGain(t *testing.T) {
	ref := rampCurve(1000)
	frame := ref // identical curves → measured scale 1

	fm := Meta{ExposureMs: 60000, Gain: 0, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 60000, Gain: 120, Instrument: "ZWO ASI1600MM Pro"}
	tr := FitCurves(frame, ref, fm, rm)

	// Same exposure, +120 gain → predicted scale 10^0.6 ≈ 3.98, but measured scale is 1.
	assert.InDelta(t, 1.0, tr.Scale, 1e-9)
	assert.True(t, tr.MetaDisagree, "measured scale 1 disagrees with the gain-predicted ~4×")
}

func TestFitCurves_NonZWOUsesExposureOnly(t *testing.T) {
	ref := rampCurve(1000)
	frame := ref

	// Non-ZWO instrument: the gain term is dropped even though gains differ; equal exposures ⇒ agree.
	fm := Meta{ExposureMs: 60000, Gain: 0, Instrument: "Canon EOS Ra"}
	rm := Meta{ExposureMs: 60000, Gain: 120, Instrument: "Canon EOS Ra"}
	tr := FitCurves(frame, ref, fm, rm)

	assert.InDelta(t, 1.0, tr.Scale, 1e-9)
	assert.False(t, tr.MetaDisagree, "no gain term for non-ZWO ⇒ prediction matches at equal exposure")
}
