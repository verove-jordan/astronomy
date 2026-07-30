package pipeline

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// identityH is the no-op homography (a frame exactly on the anchor canvas).
var identityH = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}

// rotH builds a rotation-about-the-canvas-centre homography (the multi-night shape: another
// night's frame lands rotated on the anchor canvas).
func rotH(deg float64, w, h int) [9]float64 {
	rad := deg * math.Pi / 180
	c, s := math.Cos(rad), math.Sin(rad)
	cx, cy := float64(w)/2, float64(h)/2
	// translate(-centre) → rotate → translate(+centre)
	return [9]float64{
		c, -s, cx - c*cx + s*cy,
		s, c, cy - s*cx - c*cy,
		0, 0, 1,
	}
}

func keepAll(int) bool { return false }

func TestRasterizeCoverage_IdentityCoversCanvas(t *testing.T) {
	frameH := map[int][9]float64{0: identityH, 1: identityH, 2: identityH}
	g := rasterizeCoverage(frameH, keepAll, 640, 480)

	require.Equal(t, 3, g.Frames)
	frac := coveredFrac(g.mask(0.30)) // threshold: ≥1 frame (30% of 3 rounds to 1)
	assert.Greater(t, frac, 0.97, "identity frames cover essentially the whole canvas")
	x0, y0, x1, y1 := largestInteriorRect(g.mask(0.30), g.W, g.H)
	assert.Greater(t, float64((x1-x0)*(y1-y0))/float64(g.W*g.H), 0.97, "the interior rect is ~the canvas")
	_ = x0
	_ = y0
}

func TestRasterizeCoverage_RotatedNightLeavesWedges(t *testing.T) {
	// A 45°-rotated landscape frame cannot cover the landscape canvas corners — the wedge shape
	// behind task #354's black corners. Mixed with identity frames at 30% depth, the corner cells
	// stay below threshold while the centre carries both nights.
	frameH := map[int][9]float64{
		0: identityH, 1: identityH,
		2: rotH(45, 640, 480), 3: rotH(45, 640, 480),
	}
	g := rasterizeCoverage(frameH, keepAll, 640, 480)
	mask := g.mask(0.7) // need ≥ round(0.7·4) = 3 of 4 frames → only the identity∩rotated region
	assert.False(t, mask[0], "the canvas corner is not covered by the rotated night")
	centre := (g.H/2)*g.W + g.W/2
	assert.True(t, mask[centre], "the centre carries all frames")
	frac := coveredFrac(mask)
	assert.Greater(t, frac, 0.2)
	assert.Less(t, frac, 0.9, "the 3-of-4 region is the rotated intersection, not the canvas")
}

func TestRasterizeCoverage_RejectedFramesDoNotCount(t *testing.T) {
	frameH := map[int][9]float64{0: identityH, 1: rotH(90, 640, 480)}
	rejected := func(i int) bool { return i == 1 }
	g := rasterizeCoverage(frameH, rejected, 640, 480)
	assert.Equal(t, 1, g.Frames, "a graded-out frame contributes no coverage")
	assert.True(t, g.mask(0.3)[0], "the identity frame covers the corner alone")
}

func TestLargestInteriorRect_FindsTheBand(t *testing.T) {
	// 8×4 grid with only columns 2..5 true → the rect is exactly that band.
	gw, gh := 8, 4
	mask := make([]bool, gw*gh)
	for y := 0; y < gh; y++ {
		for x := 2; x <= 5; x++ {
			mask[y*gw+x] = true
		}
	}
	x0, y0, x1, y1 := largestInteriorRect(mask, gw, gh)
	assert.Equal(t, [4]int{2, 0, 6, 4}, [4]int{x0, y0, x1, y1})
}

func TestCropFITS_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.fits")
	im := fits.NewImage(64, 32, 1)
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			im.Pix[0][y*64+x] = float32(y*64 + x)
		}
	}
	require.NoError(t, im.WriteFITS(src))

	dst := filepath.Join(dir, "dst.fits")
	require.NoError(t, cropFITS(src, dst, 8, 4, 40, 20))
	out, err := fits.ReadImage(dst)
	require.NoError(t, err)
	assert.Equal(t, 32, out.W)
	assert.Equal(t, 16, out.H)
	assert.Equal(t, float32(4*64+8), out.Pix[0][0], "top-left of the crop is (8,4) of the source")
	assert.Equal(t, float32(19*64+39), out.Pix[0][15*32+31], "bottom-right maps to (39,19)")
}

func TestWriteCoverageMaskPNG(t *testing.T) {
	g := &coverageGrid{W: 4, H: 2, Counts: []uint16{0, 1, 2, 4, 0, 0, 4, 4}, Frames: 4}
	p := filepath.Join(t.TempDir(), "coverage_L.png")
	require.NoError(t, writeCoverageMaskPNG(g, p))
	assert.FileExists(t, p)
}

// TestRasterizeCoverageOn_OffsetUnionCanvas pins the union-canvas form: a frame rasterized onto a
// bigger canvas with an origin offset lands exactly where the offset says, and the zero-offset
// general form matches the legacy delegate cell for cell.
func TestRasterizeCoverageOn_OffsetUnionCanvas(t *testing.T) {
	frameH := map[int][9]float64{0: identityH}

	legacy := rasterizeCoverage(frameH, keepAll, 640, 480)
	general := rasterizeCoverageOn(frameH, keepAll, 640, 480, canvasSpec{W: 640, H: 480}, coverageDownscale)
	assert.Equal(t, legacy.Counts, general.Counts, "zero-offset general form ≡ legacy delegate")
	assert.Equal(t, coverageDownscale, general.Scale)

	// Union canvas 800×600 with the anchor frame origin shifted to (100, 60): the frame's footprint
	// occupies exactly the shifted rectangle — covered inside, empty outside.
	cv := canvasSpec{W: 800, H: 600, OffX: 100, OffY: 60}
	g := rasterizeCoverageOn(frameH, keepAll, 640, 480, cv, coverageDownscale)
	cell := func(px, py float64) uint16 {
		return g.Counts[int(py)/g.Scale*g.W+int(px)/g.Scale]
	}
	assert.Equal(t, uint16(1), cell(100+320, 60+240), "frame centre covered")
	assert.Equal(t, uint16(1), cell(100+8, 60+8), "frame top-left corner covered")
	assert.Equal(t, uint16(0), cell(50, 50), "left of the shifted frame is empty")
	assert.Equal(t, uint16(0), cell(100+640+16, 60+240), "right of the shifted frame is empty")
}

// TestGroupFootprintMask_RotatedGroup pins the per-group union footprint used by the seam offset
// refit: only the span's frames contribute, and a rotated group's mask is its rotated rectangle.
func TestGroupFootprintMask_RotatedGroup(t *testing.T) {
	const w, h = 640, 480
	frameH := map[int][9]float64{
		0: identityH, 1: identityH, // group A: anchor frames
		2: rotH(30, w, h), 3: rotH(30, w, h), // group B: the rotated night
	}
	cv := canvasSpec{W: w, H: h}

	anchor := groupFootprintMask(frameH, keepAll, groupSpan{Start: 0, End: 2}, w, h, cv, coverageDownscale)
	rotated := groupFootprintMask(frameH, keepAll, groupSpan{Start: 2, End: 4}, w, h, cv, coverageDownscale)

	assert.Greater(t, coveredFrac(anchor), 0.97, "anchor group covers the whole canvas")
	frac := coveredFrac(rotated)
	assert.Greater(t, frac, 0.5, "rotated group still covers most of the canvas")
	assert.Less(t, frac, 0.95, "rotated group leaves the corner wedges uncovered")

	// A rejected frame drops out of its group's footprint.
	rejectGroupB := func(i int) bool { return i >= 2 }
	empty := groupFootprintMask(frameH, rejectGroupB, groupSpan{Start: 2, End: 4}, w, h, cv, coverageDownscale)
	assert.Equal(t, 0.0, coveredFrac(empty))
}
