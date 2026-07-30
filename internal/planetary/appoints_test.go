package planetary

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// onDiscCount recomputes how many grid nodes fall on the lit disc — the denominator the usable
// count is judged against.
func onDiscCount(im *fits.Image, n int) int {
	cx, cy := apCenters(im.W, im.H, n)
	c := 0
	for _, on := range apDiskMask(im, cx, cy) {
		if on {
			c++
		}
	}
	return c
}

func TestEstimateAlignPoints_TexturedDisc(t *testing.T) {
	im := bumpDiskImage(1200, 1200, 5, 900)
	est := EstimateAlignPoints(im, 60)
	assert.Equal(t, 20, est.PerAxis, "1200/60 = 20 per axis")
	assert.Equal(t, 400, est.TotalPoints)
	assert.Equal(t, 400, est.SuggestedAlignPoints, "suggestion is the full grid; the run vetoes featureless nodes")
	onDisc := onDiscCount(im, est.PerAxis)
	assert.Greater(t, est.UsablePoints, int(0.6*float64(onDisc)),
		"most on-disc windows over a richly textured disc must correlate (usable %d, on-disc %d)", est.UsablePoints, onDisc)
}

func TestEstimateAlignPoints_FlatDiscMostlyVetoed(t *testing.T) {
	im := bumpDiskImage(1200, 1200, 5, 0) // a flat 0.5 disc, no texture
	est := EstimateAlignPoints(im, 60)
	onDisc := onDiscCount(im, est.PerAxis)
	require.Positive(t, onDisc)
	assert.Less(t, est.UsablePoints, int(0.35*float64(onDisc)),
		"a featureless disc keeps only limb-ring cells (usable %d, on-disc %d)", est.UsablePoints, onDisc)
}

func TestEstimateAlignPoints_MinPxScalesGrid(t *testing.T) {
	im := bumpDiskImage(1200, 1200, 5, 600)
	tests := []struct {
		minPx       int
		wantPerAxis int
	}{
		{0, 25},    // default window = 2·1200·2% = 48 px → 1200/48 = 25
		{120, 10},  // coarse window → floors at 10
		{24, 48},   // fine window → caps at 48
		{5000, 10}, // absurd window → floors at 10
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.minPx), func(t *testing.T) {
			est := EstimateAlignPoints(im, tt.minPx)
			assert.Equal(t, tt.wantPerAxis, est.PerAxis)
			assert.Equal(t, 1200/tt.wantPerAxis, est.CellPx)
			assert.Equal(t, denseGridN(1200, 1200), est.AutoPerAxis, "auto density is reported for comparison")
		})
	}
}

func TestEstimateAlignPoints_NoDiscFallback(t *testing.T) {
	// A fully textured frame with no dark sky and no circular limb (uniform noise): fitLunarDisc
	// finds no disc, but the lit-mask + structure gate still counts usable points.
	im := fits.NewImage(700, 700, 1)
	rng := rand.New(rand.NewSource(9))
	for i := range im.Pix[0] {
		im.Pix[0][i] = 0.4 + float32(rng.Float64())*0.3 // full-frame 0.4..0.7 noise
	}
	est := EstimateAlignPoints(im, 60)
	assert.False(t, est.Disc.OK, "no circular limb can be fit from full-frame noise")
	assert.Positive(t, est.UsablePoints, "the lit-mask + structure gate still finds points")
}

func TestEstimateAlignPoints_Deterministic(t *testing.T) {
	im := bumpDiskImage(900, 900, 3, 500)
	assert.Equal(t, EstimateAlignPoints(im, 50), EstimateAlignPoints(im, 50))
}

func TestLoadLuminanceFrame_PNG16(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gray.png")
	g := image.NewGray16(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			g.SetGray16(x, y, color.Gray16{Y: uint16((x + y) * 4096)})
		}
	}
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, g))
	require.NoError(t, f.Close())

	im, err := LoadLuminanceFrame(path)
	require.NoError(t, err)
	assert.Equal(t, 8, im.W)
	assert.Equal(t, 6, im.H)
	assert.Equal(t, 1, im.C)
	for _, v := range im.Pix[0] {
		assert.GreaterOrEqual(t, v, float32(0))
		assert.LessOrEqual(t, v, float32(1))
	}
}

func TestLoadLuminanceFrame_RejectsRaw(t *testing.T) {
	_, err := LoadLuminanceFrame("/x/moon.dng")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "camera-raw")
}

func TestExtractFirstFrame_OneFrame(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mp4")
	// A 3-frame synthetic clip, then pull exactly frame 1.
	gen := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "testsrc=size=64x48:rate=1:duration=3", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg lavfi unavailable: %s", out)
	}
	out, err := ExtractFirstFrame(context.Background(), "ffmpeg", src, dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "first.png"), out)
	im, err := LoadLuminanceFrame(out)
	require.NoError(t, err)
	assert.Equal(t, 64, im.W)
	assert.Equal(t, 48, im.H)
}
