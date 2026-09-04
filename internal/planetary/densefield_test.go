package planetary

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestMeasureTwoLevelField_RefinesSubCellWarp pins the whole point of the dense level: a
// seeing warp with a period well below what the smoothed coarse 10×10 grid can carry (the
// 3×3 grid smoothing kills structure faster than ~4–5 cells across) is recovered by the dense
// grid but invisible to the coarse one. Frame sizes are production-like so the dense grid is
// genuinely denser than 10×10 (denseGridN only densifies above ~1200 px).
func TestMeasureTwoLevelField_RefinesSubCellWarp(t *testing.T) {
	if testing.Short() {
		t.Skip("production-size frame; skipped with -short")
	}
	const w, h = 2400, 2400 // coarse cells 240 px, dense cells 120 px (denseGridN = 20)
	const period = 700.0    // killed by coarse-grid smoothing, carried by the dense grid
	truth := bumpDiskImage(w, h, 5, 900)

	warpAt := func(x, y float64) (dx, dy float64) {
		dx = 1.2 * math.Sin(2*math.Pi*x/period) * math.Cos(2*math.Pi*y/(3*period))
		dy = 1.0 * math.Sin(2*math.Pi*y/period+1.0) * math.Cos(2*math.Pi*x/(3*period))
		return dx, dy
	}
	const genN = 24
	genX, genY := apCenters(w, h, genN)
	genDx := make([]float64, genN*genN)
	genDy := make([]float64, genN*genN)
	for k := range genDx {
		genDx[k], genDy[k] = warpAt(genX[k], genY[k])
	}
	frame := warpByGrid(truth, genDx, genDy)

	rc := newRefContext(truth)
	rcD := denseContextFrom(truth, rc, 0)
	denseDx, denseDy, _ := measureTwoLevelField(frame, &rc, &rcD, frameSeed{})
	coarseDx, coarseDy, _ := measureFrameField(frame, &rc, true, frameSeed{})
	coarseAtD, coarseAtDy := resampleFieldTo(coarseDx, coarseDy, rc.gridN, &rcD, w, h)

	core := coreNodes(rcD.onDisk)
	require.GreaterOrEqual(t, len(core), 40, "fixture must keep a solid structured core")
	rms := func(fx, fy []float64) float64 {
		var s float64
		for _, k := range core {
			tx, ty := warpAt(rcD.cx[k], rcD.cy[k])
			// The registration field is the NEGATED content motion (sample-from offsets).
			s += (fx[k]+tx)*(fx[k]+tx) + (fy[k]+ty)*(fy[k]+ty)
		}
		return math.Sqrt(s / float64(2*len(core)))
	}
	denseRMS := rms(denseDx, denseDy)
	coarseRMS := rms(coarseAtD, coarseAtDy)
	assert.Less(t, denseRMS, 0.4, "dense level must recover the sub-cell warp (RMS px)")
	assert.Less(t, denseRMS, 0.5*coarseRMS,
		"dense residual must be well below the coarse grid's (dense %.3f px vs coarse %.3f px)", denseRMS, coarseRMS)
}

// TestDenseContextFrom_AlignPointsOverride pins the align_points override: 0 reproduces the
// auto grid exactly; a total point count lands an N×N grid with N=√total, leaving the patch
// half-size (apRadius) untouched.
func TestDenseContextFrom_AlignPointsOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("production-size frame; skipped with -short")
	}
	const w, h = 2400, 2400
	truth := bumpDiskImage(w, h, 5, 900)
	rc := newRefContext(truth)

	auto := denseContextFrom(truth, rc, 0)
	assert.Equal(t, denseGridN(w, h), auto.gridN, "0 = today's auto density")
	assert.Equal(t, 20, auto.gridN)

	over := denseContextFrom(truth, rc, 900)
	assert.Equal(t, 30, over.gridN, "900 total → 30×30")
	assert.Len(t, over.cx, 30*30, "grid has N² nodes")
	assert.Equal(t, auto.apRadius, over.apRadius, "override changes only the grid density, not the patch size")
}

// TestMeasureTwoLevelField_VetoRidesBaseline pins the safety property: dense APs over
// featureless surface are vetoed and ride the coarse baseline instead of derailing on a
// degenerate correlation — a pure global shift is recovered everywhere, flat half included.
func TestMeasureTwoLevelField_VetoRidesBaseline(t *testing.T) {
	const w, h = 512, 512
	truth := fits.NewImage(w, h, 1)
	p := truth.Pix[0]
	cx, cy := float64(w)/2, float64(h)/2
	rad := 0.42 * float64(w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy <= rad*rad {
				p[y*w+x] = 0.5
			}
		}
	}
	// Texture ONLY on the right half; the left half of the disk stays perfectly flat.
	textured := bumpDiskImage(w, h, 7, 400)
	for y := 0; y < h; y++ {
		for x := int(cx); x < w; x++ {
			p[y*w+x] = textured.Pix[0][y*w+x]
		}
	}
	frame := cubicShift(truth, 1.4, -0.8)

	rc := newRefContext(truth)
	rcD := denseContextFrom(truth, rc, 0)
	vetoed := 0
	for k, on := range rcD.onDisk {
		dx, dy := rcD.cx[k]-cx, rcD.cy[k]-cy
		if !on && dx*dx+dy*dy < (0.8*rad)*(0.8*rad) {
			vetoed++
		}
	}
	require.Greater(t, vetoed, 5, "the flat half must veto its dense APs")

	// The registration field stores sample-from offsets: the NEGATED content motion.
	dxG, dyG, _ := measureTwoLevelField(frame, &rc, &rcD, frameSeed{})
	for k := range dxG {
		assert.InDelta(t, -1.4, dxG[k], 0.35, "node %d dx: vetoed cells must ride the global baseline", k)
		assert.InDelta(t, 0.8, dyG[k], 0.35, "node %d dy", k)
	}
}
