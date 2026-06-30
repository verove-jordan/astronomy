package skyplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// visKit is a representative visual eyepiece set for the FC-100 (740 mm): 30mm→24.7×/2.75°/4.05mm,
// 18mm→41.1×/1.58°/2.43mm, 10mm→74×/0.81°/1.35mm, 6mm→123×/0.49°/0.81mm.
var visKit = []Eyepiece{
	{FocalMM: 30, AFOVDeg: 68, Label: "30mm"},
	{FocalMM: 18, AFOVDeg: 65, Label: "18mm"},
	{FocalMM: 10, AFOVDeg: 60, Label: "10mm"},
	{FocalMM: 6, AFOVDeg: 60, Label: "6mm"},
}

func TestOptics_View(t *testing.T) {
	v := fc100.View(Eyepiece{FocalMM: 10, AFOVDeg: 60, Label: "10mm"})
	assert.InDelta(t, 74.0, v.MagX, 0.1)
	assert.InDelta(t, 0.811, v.TrueFOVDeg, 0.01)
	assert.InDelta(t, 1.351, v.ExitPupilMM, 0.01)
	assert.InDelta(t, 48.65, v.TrueFOVMinArcmin(), 0.5)
}

func TestOptics_View_Unusable(t *testing.T) {
	assert.Zero(t, fc100.View(Eyepiece{FocalMM: 0, AFOVDeg: 60}).MagX, "zero focal eyepiece")
	assert.Zero(t, Optics{}.View(Eyepiece{FocalMM: 10, AFOVDeg: 60}).MagX, "zero focal scope")
}

func TestOptics_Barlow(t *testing.T) {
	base := fc100 // BarlowX 0 → no Barlow
	barlowed := fc100
	barlowed.BarlowX = 2

	// A 2× Barlow doubles the effective focal length: image scale halves, f-ratio doubles, eyepiece
	// magnification doubles and exit pupil halves.
	assert.InDelta(t, base.ImageScale()/2, barlowed.ImageScale(), 1e-6)
	assert.InDelta(t, base.FRatio()*2, barlowed.FRatio(), 1e-6)

	ep := Eyepiece{FocalMM: 10, AFOVDeg: 60}
	assert.InDelta(t, base.View(ep).MagX*2, barlowed.View(ep).MagX, 1e-6)
	assert.InDelta(t, base.View(ep).ExitPupilMM/2, barlowed.View(ep).ExitPupilMM, 1e-6)

	// Zero/negative means "no Barlow".
	none := fc100
	none.BarlowX = 0
	assert.InDelta(t, base.ImageScale(), none.ImageScale(), 1e-9)
}

func TestChooseEyepiece(t *testing.T) {
	t.Run("empty kit returns false", func(t *testing.T) {
		_, ok := chooseEyepiece(fc100, nil, 30, true)
		assert.False(t, ok)
	})
	t.Run("large object frames in the widest field", func(t *testing.T) {
		v, ok := chooseEyepiece(fc100, visKit, 178, true) // ~M31
		require.True(t, ok)
		assert.Equal(t, "30mm", v.Label)
	})
	t.Run("small object frames at more power", func(t *testing.T) {
		v, ok := chooseEyepiece(fc100, visKit, 25, true) // ~half the 10mm field
		require.True(t, ok)
		assert.Equal(t, "10mm", v.Label)
	})
	t.Run("unknown size picks a medium exit pupil", func(t *testing.T) {
		v, ok := chooseEyepiece(fc100, visKit, 0, false)
		require.True(t, ok)
		assert.Equal(t, "18mm", v.Label) // exit pupil 2.43mm is closest to the ~2mm ideal
	})
	t.Run("object larger than every field picks the widest", func(t *testing.T) {
		v, ok := chooseEyepiece(fc100, visKit, 600, true)
		require.True(t, ok)
		assert.Equal(t, "30mm", v.Label)
	})
	t.Run("out-of-range exit pupils are excluded", func(t *testing.T) {
		kit := []Eyepiece{
			{FocalMM: 80, AFOVDeg: 60, Label: "80mm"}, // exit pupil 10.8mm → excluded
			{FocalMM: 18, AFOVDeg: 65, Label: "18mm"}, // exit pupil 2.43mm → usable
		}
		v, ok := chooseEyepiece(fc100, kit, 0, false)
		require.True(t, ok)
		assert.Equal(t, "18mm", v.Label)
	})
}

func TestVisualDetectabilityScore(t *testing.T) {
	view := fc100.View(Eyepiece{FocalMM: 18, AFOVDeg: 65})
	t.Run("unknown magnitude is neutral and flagged", func(t *testing.T) {
		got, known := visualDetectabilityScore("galaxy", 0, 10, 100, view, false, true)
		assert.Equal(t, 0.5, got)
		assert.False(t, known)
	})
	t.Run("point-like brighter scores higher", func(t *testing.T) {
		bright, _ := visualDetectabilityScore("globular", 6, 0, 100, view, true, false)
		faint, _ := visualDetectabilityScore("globular", 12, 0, 100, view, true, false)
		assert.Greater(t, bright, faint)
	})
	t.Run("extended target eases with aperture", func(t *testing.T) {
		small, _ := visualDetectabilityScore("galaxy", 9, 20, 100, view, true, true)
		big, _ := visualDetectabilityScore("galaxy", 9, 20, 250, view, true, true)
		assert.Greater(t, big, small)
	})
}

func TestMoonSensitivityVisual_StrongerThanCamera(t *testing.T) {
	assert.Greater(t, moonSensitivityVisual("galaxy", true, 21), moonSensitivity("galaxy", true, 21))
	assert.Greater(t, moonSensitivityVisual("emission_nebula", true, 20), moonSensitivity("emission_nebula", true, 20))
}
