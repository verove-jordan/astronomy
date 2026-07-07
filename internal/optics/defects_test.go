package optics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Detection fixtures are kept at 512px (<=1024) so LoadFlatPlane's pool factor is 1 and scale==1,
// making the geometry assertions exact. The high-pass deviation (1-plane/smooth) reports roughly 65%
// of a feature's raw transmission dip, so a ring built with a 7.7% dip measures Depth ~0.05.
func TestDetectDefectsDonut(t *testing.T) {
	dir := t.TempDir()
	p := vignetteBase(512, 512, 0.18)
	addRing(p, 512, 200, 150, 8, 18, 0.077) // ~0.05 measured depth after high-pass attenuation
	path := writeFloatMaster(t, dir, "donut.fits", 512, 512, p)

	plane, w, h, scale, bayer, err := LoadFlatPlane(path)
	require.NoError(t, err)
	require.Equal(t, 1.0, scale, "512px master must not be pooled")
	assert.False(t, bayer)

	defects, dev := DetectDefects(plane, w, h, scale)
	require.Len(t, dev, w*h)
	require.Len(t, defects, 1, "exactly one donut")
	d := defects[0]
	assert.True(t, d.Donut, "hollow ring must be classified as a donut")
	assert.InDelta(t, 200, d.CX, 3, "centroid X")
	assert.InDelta(t, 150, d.CY, 3, "centroid Y")
	assert.InDelta(t, 0.05, d.Depth, 0.01, "peak depth")
	assert.Greater(t, equivDiameter(d.AreaPx), 20.0, "donut is resolved (>20px)")
	assert.NotEmpty(t, d.Shape, "repair kernel populated")
}

func TestDetectDefectsBlob(t *testing.T) {
	dir := t.TempDir()
	p := vignetteBase(512, 512, 0.18)
	// A solid Gaussian blob big enough to resolve (>20px) but masked through its center, so it is a
	// blob, not a donut. (A literal "radius 30, 2.5% dip" blob is fully absorbed by the smooth model;
	// this sharper/deeper blob is what the high-pass actually flags.)
	addGauss(p, 512, 256, 256, 14, 0.12)
	path := writeFloatMaster(t, dir, "blob.fits", 512, 512, p)

	plane, w, h, scale, _, err := LoadFlatPlane(path)
	require.NoError(t, err)
	defects, _ := DetectDefects(plane, w, h, scale)
	require.Len(t, defects, 1, "exactly one blob")
	d := defects[0]
	assert.False(t, d.Donut, "solid blob must NOT be a donut")
	assert.InDelta(t, 256, d.CX, 4)
	assert.InDelta(t, 256, d.CY, 4)
	assert.Greater(t, equivDiameter(d.AreaPx), 20.0, "blob is resolved, so non-donut is via hollowness")
}

func TestDetectDefectsCleanVignette(t *testing.T) {
	dir := t.TempDir()
	p := vignetteBase(512, 512, 0.18)
	path := writeFloatMaster(t, dir, "clean.fits", 512, 512, p)

	qc, defects, err := AnalyzeFlat(path, nil)
	require.NoError(t, err)
	assert.Empty(t, defects, "smooth vignette has no discrete defects")
	assert.InDelta(t, 0.15, qc.VignetteDepth, 0.03, "vignetting is measured even with no defects")
	assert.Equal(t, "ok", qc.Status)
}

func TestDetectDefectsBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	p := vignetteBase(512, 512, 0.18)
	addGauss(p, 512, 300, 300, 8, 0.01) // ~1% dip -> measured ~0.6% < 1.5% threshold
	path := writeFloatMaster(t, dir, "shallow.fits", 512, 512, p)

	plane, w, h, scale, _, err := LoadFlatPlane(path)
	require.NoError(t, err)
	defects, _ := DetectDefects(plane, w, h, scale)
	assert.Empty(t, defects, "a 1%-deep blob is below the detection threshold")
}
