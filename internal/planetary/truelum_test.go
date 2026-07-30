package planetary

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// tintedSoftComposite builds the leak scenario: a colour composite whose luminance is a BLURRED
// copy of the sharp L (what rgbcomp -lum effectively produces), each channel tinted.
func tintedSoftComposite(l *fits.Image, tints [3]float64) *fits.Image {
	soft := blurPlane(l, 4)
	comp := fits.NewImage(l.W, l.H, 3)
	for c := 0; c < 3; c++ {
		for i, v := range soft.Pix[0] {
			comp.Pix[c][i] = float32(float64(v) * tints[c])
		}
	}
	return comp
}

func TestApplyTrueLum_RestoresSharpLuminanceKeepsChroma(t *testing.T) {
	l := drawMoon(200, 200, 100, 100, 70, 0, 0.8, 0.01, 0.001, 0)
	tints := [3]float64{0.9, 1.0, 1.1}
	comp := tintedSoftComposite(l, tints)

	applyTrueLum(comp, l.Pix[0])

	// On-disc samples: luminance equals L exactly and the channel ratios (chromaticity) survive.
	for _, pt := range [][2]int{{100, 100}, {80, 120}, {130, 90}} {
		i := pt[1]*200 + pt[0]
		lum := (float64(comp.Pix[0][i]) + float64(comp.Pix[1][i]) + float64(comp.Pix[2][i])) / 3
		assert.InDelta(t, float64(l.Pix[0][i]), lum, 2e-3, "mean luminance re-imposed at (%d,%d)", pt[0], pt[1])
		ratio := float64(comp.Pix[2][i]) / float64(comp.Pix[0][i])
		assert.InDelta(t, tints[2]/tints[0], ratio, 0.02, "chromaticity preserved at (%d,%d)", pt[0], pt[1])
	}
}

func TestReimposeLuminance_FileFlow(t *testing.T) {
	dir := t.TempDir()
	l := drawMoon(160, 160, 80, 80, 55, 0, 0.8, 0.01, 0.001, 0)
	lBase := filepath.Join(dir, "master_L")
	require.NoError(t, l.WriteFITS(lBase+".fits"))
	comp := tintedSoftComposite(l, [3]float64{1, 1, 1})
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, comp.WriteFITS(outBase+".fits"))

	note := reimposeLuminance(outBase, lBase)
	assert.Empty(t, note, "success is silent")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	i := 80*160 + 80
	lum := (float64(out.Pix[0][i]) + float64(out.Pix[1][i]) + float64(out.Pix[2][i])) / 3
	assert.InDelta(t, float64(l.Pix[0][i]), lum, 2e-3, "written composite carries L's luminance")
}

func TestReimposeLuminance_SoftFails(t *testing.T) {
	dir := t.TempDir()
	comp := fits.NewImage(32, 32, 3)
	outBase := filepath.Join(dir, "moon_stack")
	require.NoError(t, comp.WriteFITS(outBase+".fits"))

	note := reimposeLuminance(outBase, filepath.Join(dir, "missing_L"))
	assert.Contains(t, note, "true-lum skipped:")

	out, err := fits.ReadImage(outBase + ".fits")
	require.NoError(t, err)
	assert.Equal(t, comp.Pix[0], out.Pix[0], "composite untouched on skip")
}
