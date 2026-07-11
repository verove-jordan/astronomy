package optics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

const frameW, frameH = 512, 512

// uniformFrame returns a flat background of `bg` with the defect's core (Shape>0.5) multiplied by
// `mul`, written to a FITS light frame.
func uniformFrame(t *testing.T, dir, name string, d *Defect, bg, mul float32) string {
	t.Helper()
	pix := make([]float32, frameW*frameH)
	for i := range pix {
		pix[i] = bg
	}
	imprintDefect(pix, frameW, frameH, d, mul)
	return writeFrame(t, dir, name, frameW, frameH, pix)
}

func coreValue(t *testing.T, path string, d *Defect) float32 {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	return im.Pix[0][d.CY*frameW+d.CX]
}

// TestMeasureAndRepairResidual: a consistent -3% residual is measured (Rel~0.03) and divided back out,
// leaving core/annulus within 0.5% (re-measured |Rel| ~ 0).
func TestMeasureAndRepairResidual(t *testing.T) {
	dir := t.TempDir()
	d := diskShapeDefect(255, 255, 31, 755, 0.03)
	var frames []string
	for _, n := range []string{"f0.fits", "f1.fits", "f2.fits"} {
		frames = append(frames, uniformFrame(t, dir, n, &d, 0.05, 0.97))
	}

	res, err := MeasureResiduals([]Defect{d}, frames)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.InDelta(t, 0.03, res[0].Rel, 0.01, "measured residual dip")
	assert.True(t, res[0].Consistent)

	repaired, notes := RepairFrames(context.Background(), []Defect{d}, res, frames)
	assert.Equal(t, 3, repaired)
	assert.NotEmpty(t, notes)

	after, err := MeasureResiduals([]Defect{d}, frames)
	require.NoError(t, err)
	assert.InDelta(t, 0, after[0].Rel, 0.005, "core/annulus within 0.5% after repair")
}

// TestRepairClampsCorrection: a large (-25%) residual is clamped so the per-pixel correction stays
// within +/-8%, never over-correcting.
func TestRepairClampsCorrection(t *testing.T) {
	dir := t.TempDir()
	d := diskShapeDefect(255, 255, 31, 755, 0.03)
	frame := uniformFrame(t, dir, "bump.fits", &d, 0.05, 1.25) // core 25% too bright

	res, err := MeasureResiduals([]Defect{d}, []string{frame})
	require.NoError(t, err)
	require.True(t, res[0].Consistent)
	assert.Less(t, res[0].Rel, -0.15)

	before := coreValue(t, frame, &d)
	repaired, _ := RepairFrames(context.Background(), []Defect{d}, res, []string{frame})
	require.Equal(t, 1, repaired)
	after := coreValue(t, frame, &d)

	change := float64(after/before) - 1
	assert.LessOrEqual(t, absF(change), 0.08, "correction clamped to <=8%")
}

// TestResidualInconsistentNotActionable: alternating-sign residuals across frames read as
// inconsistent, so nothing is repaired.
func TestResidualInconsistentNotActionable(t *testing.T) {
	dir := t.TempDir()
	d := diskShapeDefect(255, 255, 31, 755, 0.03)
	muls := []float32{0.97, 1.03, 0.97, 1.03}
	var frames []string
	for i, m := range muls {
		frames = append(frames, uniformFrame(t, dir, string(rune('a'+i))+".fits", &d, 0.05, m))
	}

	res, err := MeasureResiduals([]Defect{d}, frames)
	require.NoError(t, err)
	assert.False(t, res[0].Consistent, "alternating signs are not a stable artifact")

	repaired, notes := RepairFrames(context.Background(), []Defect{d}, res, frames)
	assert.Zero(t, repaired)
	assert.Empty(t, notes)
}

// TestOversizedDefectMeasuredButNotRepaired: a defect larger than 2% of the frame is measured and
// flagged, but never divided out (too large to safely touch).
func TestOversizedDefectMeasuredButNotRepaired(t *testing.T) {
	dir := t.TempDir()
	d := diskShapeDefect(255, 255, 90, 6561, 0.05) // 6561 > 2% of 512^2 (5243)
	var frames []string
	for _, n := range []string{"o0.fits", "o1.fits", "o2.fits"} {
		frames = append(frames, uniformFrame(t, dir, n, &d, 0.05, 0.95))
	}

	res, err := MeasureResiduals([]Defect{d}, frames)
	require.NoError(t, err)
	assert.InDelta(t, 0.05, res[0].Rel, 0.01, "still measured")
	assert.True(t, res[0].Consistent)

	before := coreValue(t, frames[0], &d)
	repaired, notes := RepairFrames(context.Background(), []Defect{d}, res, frames)
	assert.Zero(t, repaired, "oversized defect is never repaired")
	require.NotEmpty(t, notes)
	assert.Contains(t, notes[0], "too large")
	assert.Equal(t, before, coreValue(t, frames[0], &d), "frame left untouched")
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
