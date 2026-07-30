package planetary

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// texturedDisk builds a bright disk scattered with spots at the alignment-point grid, so every on-disk
// AP has a feature to correlate (a uniform disk would give the interior APs nothing to lock onto).
func texturedDisk(w, h int) *fits.Image {
	im := fits.NewImage(w, h, 1)
	p := im.Pix[0]
	cx, cy := float64(w)/2, float64(h)/2
	rad := float64(min(w, h)) * 0.45
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if math.Hypot(float64(x)-cx, float64(y)-cy) <= rad {
				p[y*w+x] = 0.3
			}
		}
	}
	for j := 1; j < apGridN; j++ {
		for i := 1; i < apGridN; i++ {
			sx, sy := i*w/apGridN, j*h/apGridN
			if math.Hypot(float64(sx)-cx, float64(sy)-cy) > rad*0.9 {
				continue
			}
			for dy := -5; dy <= 5; dy++ {
				for dx := -5; dx <= 5; dx++ {
					x, y := sx+dx, sy+dy
					if x >= 0 && x < w && y >= 0 && y < h && dx*dx+dy*dy <= 25 {
						p[y*w+x] = 1.0
					}
				}
			}
		}
	}
	return im
}

// ssd is the sum of squared differences over the central region (edges excluded — warping pulls in
// zero from outside the frame there).
func ssd(a, b *fits.Image) float64 {
	var s float64
	m := 20
	for y := m; y < a.H-m; y++ {
		for x := m; x < a.W-m; x++ {
			d := float64(a.Pix[0][y*a.W+x] - b.Pix[0][y*a.W+x])
			s += d * d
		}
	}
	return s
}

func TestWarpToSharpest_CorrectsLocalDistortion(t *testing.T) {
	dir := t.TempDir()
	ref := texturedDisk(256, 256)
	refPath := filepath.Join(dir, "in_00001.fits")
	require.NoError(t, ref.WriteFITS(refPath))
	shifted := comet.Translate(ref, 3, -2) // a small drift the measure-and-warp pass must undo
	shPath := filepath.Join(dir, "in_00002.fits")
	require.NoError(t, shifted.WriteFITS(shPath))

	before := ssd(ref, shifted)
	// Frame 0 is sharpest → reference (written unresampled); frame 1 is measured and warped once.
	res, err := warpToSharpest(context.Background(), []string{refPath, shPath}, []float64{9, 1}, dir, "w", true, 1, 0, nil)
	require.NoError(t, err)
	out := res.paths
	require.Len(t, out, 2)

	warped, err := fits.ReadImage(out[1])
	require.NoError(t, err)
	after := ssd(ref, warped)
	assert.Less(t, after, before*0.5,
		"single-pass warp should substantially reduce the residual (before=%.4g after=%.4g)", before, after)

	// The reference frame is written unresampled (byte-for-byte the source).
	r2, err := fits.ReadImage(out[0])
	require.NoError(t, err)
	assert.Zero(t, ssd(ref, r2))
}

func TestWarpByGrid_UniformFieldTranslates(t *testing.T) {
	im := texturedDisk(128, 128)
	dx := make([]float64, apGridN*apGridN)
	dy := make([]float64, apGridN*apGridN)
	for k := range dx {
		dx[k], dy[k] = 4, -3 // a uniform field must act like a plain translation
	}
	got := warpByGrid(im, dx, dy)
	want := cubicShift(im, 4, -3) // same Catmull-Rom kernel; an integer shift is exact
	assert.Less(t, ssd(got, want), 1e-6+ssd(im, want)*0.01, "uniform-field warp ≈ cubic translate")
}
