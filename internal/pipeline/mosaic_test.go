package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestUnionCanvasOf(t *testing.T) {
	const w, h = 640, 480
	t.Run("identity frames add nothing", func(t *testing.T) {
		r := registrationReview{FrameH: map[int][9]float64{0: identityH, 1: identityH}}
		cv := r.unionCanvasOf(w, h)
		assert.Equal(t, w, cv.W)
		assert.Equal(t, h, cv.H)
		assert.Zero(t, cv.OffX)
		assert.Zero(t, cv.OffY)
	})
	t.Run("rotated night grows the union with a positive origin offset", func(t *testing.T) {
		r := registrationReview{FrameH: map[int][9]float64{0: identityH, 1: rotH(30, w, h)}}
		cv := r.unionCanvasOf(w, h)
		assert.Greater(t, cv.W, w)
		assert.Greater(t, cv.H, h)
		assert.Greater(t, cv.OffX, 0.0, "rotation about the centre pushes corners into negative x")
		assert.Greater(t, cv.OffY, 0.0)
	})
	t.Run("pure translation shifts the origin", func(t *testing.T) {
		shift := identityH
		shift[2], shift[5] = -100, 40 // frame maps 100 px left, 40 px down
		r := registrationReview{FrameH: map[int][9]float64{0: identityH, 1: shift}}
		cv := r.unionCanvasOf(w, h)
		assert.Equal(t, w+100, cv.W)
		assert.Equal(t, h+40, cv.H)
		assert.Equal(t, 100.0, cv.OffX)
		assert.Zero(t, cv.OffY)
	})
}

func TestComposeContentH(t *testing.T) {
	shift := identityH
	shift[2], shift[5] = 7, -3
	in := map[int][9]float64{4: shift}
	out := composeContentH(in, 100, 60)
	hc := out[4]
	// Content pixel (0,0) sits at padded (100,60); through the shifted H that lands at (107,57).
	x, y, ok := applyH3(hc, 0, 0)
	require.True(t, ok)
	assert.InDelta(t, 107, x, 1e-9)
	assert.InDelta(t, 57, y, 1e-9)
}

func TestPadFITS(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.fits")
	im := fits.NewImage(8, 6, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = 0.5
	}
	require.NoError(t, im.WriteFITS(src))

	dst := filepath.Join(dir, "dst.fits")
	require.NoError(t, padFITS(src, dst, 3, 2, 20, 12))
	out, err := fits.ReadImage(dst)
	require.NoError(t, err)
	assert.Equal(t, 20, out.W)
	assert.Equal(t, 12, out.H)
	assert.Equal(t, float32(0), out.Pix[0][0], "margin stays zero")
	assert.Equal(t, float32(0.5), out.Pix[0][2*20+3], "content lands at (left, top)")
	assert.Equal(t, float32(0.5), out.Pix[0][(2+5)*20+(3+7)], "content bottom-right in place")
	assert.Equal(t, float32(0), out.Pix[0][(2+5)*20+(3+8)], "right of content is margin")

	assert.Error(t, padFITS(src, dst, 15, 0, 20, 12), "placement overflowing the canvas errors")
}
