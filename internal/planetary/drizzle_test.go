package planetary

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestSnapDrizzle(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"unset legacy zero", 0, 1},
		{"negative", -3, 1},
		{"exact native", 1, 1},
		{"near 1.5", 1.4, 1.5},
		{"exact 1.5", 1.5, 1.5},
		{"between snaps up", 1.8, 2},
		{"above range", 5, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SnapDrizzle(tt.in))
		})
	}
}

func TestWarpByGridScaled_Dimensions(t *testing.T) {
	im := bumpDiskImage(100, 80, 3, 30)
	dx, dy := uniformGrid(0, 0, apGridN)
	tests := []struct {
		scale  float64
		ow, oh int
	}{
		{1, 100, 80},
		{1.5, 150, 120},
		{2, 200, 160},
	}
	for _, tt := range tests {
		out := warpByGridScaled(im, dx, dy, tt.scale)
		assert.Equal(t, tt.ow, out.W, "scale %.1f width", tt.scale)
		assert.Equal(t, tt.oh, out.H, "scale %.1f height", tt.scale)
	}
}

func TestWarpByGridScaled_Scale1MatchesWarpByGrid(t *testing.T) {
	im := bumpDiskImage(160, 160, 5, 60)
	dx, dy := uniformGrid(2.25, -1.5, apGridN)
	for k := range dx { // a non-uniform field exercises the grid interpolation too
		dx[k] += 0.4 * math.Sin(float64(k))
		dy[k] -= 0.3 * math.Cos(float64(k))
	}
	a := warpByGrid(im, dx, dy)
	b := warpByGridScaled(im, dx, dy, 1)
	assert.Equal(t, a.Pix[0], b.Pix[0], "scale 1 must be bit-identical to the native warp")
}

func TestWarpByGridScaled_EnergyPreserved(t *testing.T) {
	im := bumpDiskImage(200, 200, 7, 80)
	dx, dy := uniformGrid(1.2, -0.8, apGridN)
	meanOf := func(p []float32) float64 {
		var s float64
		for _, v := range p {
			s += float64(v)
		}
		return s / float64(len(p))
	}
	native := meanOf(warpByGrid(im, dx, dy).Pix[0])
	for _, scale := range []float64{1.5, 2} {
		scaled := meanOf(warpByGridScaled(im, dx, dy, scale).Pix[0])
		assert.InEpsilon(t, native, scaled, 0.01,
			"mean intensity at ×%.1f must match native (Catmull-Rom is a partition of unity)", scale)
	}
}

func TestResamplePlane_RoundTrip(t *testing.T) {
	im := blurPlane(bumpDiskImage(120, 120, 9, 50), 1) // band-limit the limb step first
	assert.Same(t, im, resamplePlane(im, 1), "scale 1 returns the image untouched")

	up := resamplePlane(im, 2)
	require.Equal(t, 240, up.W)
	down := resamplePlaneTo(up, 120, 120)
	var maxDiff float64
	for i := range im.Pix[0] {
		if d := math.Abs(float64(im.Pix[0][i] - down.Pix[0][i])); d > maxDiff {
			maxDiff = d
		}
	}
	assert.Less(t, maxDiff, 0.02, "up ×2 then back is near-lossless on band-limited content")
}

func TestWarpByGridScaled_UniformFieldTranslatesAtScale(t *testing.T) {
	const w, h = 120, 120
	im := fits.NewImage(w, h, 1)
	// A small Gaussian dot at (40, 60): its centroid is sub-pixel measurable.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-40, float64(y)-60
			im.Pix[0][y*w+x] = float32(math.Exp(-(dx*dx + dy*dy) / (2 * 2.5 * 2.5)))
		}
	}
	centroid := func(p []float32, w, h int) (cx, cy float64) {
		var sx, sy, s float64
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := float64(p[y*w+x])
				sx += v * float64(x)
				sy += v * float64(y)
				s += v
			}
		}
		return sx / s, sy / s
	}
	// out(x) = src(x − F): a field of (−4, +3) moves content by (−4, +3) native px — at ×2 the
	// dot must land at the SCALED position of native (36, 63).
	dx, dy := uniformGrid(-4, 3, apGridN)
	out := warpByGridScaled(im, dx, dy, 2)
	cx, cy := centroid(out.Pix[0], out.W, out.H)
	assert.InDelta(t, (36.0+0.5)*2-0.5, cx, 0.4, "dot x at ×2")
	assert.InDelta(t, (63.0+0.5)*2-0.5, cy, 0.4, "dot y at ×2")
}
