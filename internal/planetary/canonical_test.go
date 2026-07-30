package planetary

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// bumpDiskImage is the alignment fixtures' truth scene: a bright disk stamped with `bumps`
// seeded APERIODIC Gaussian bumps. The quality fixture's sinusoid texture is ambiguous to a
// windowed ZNCC (a one-period shift correlates almost as well, and a mislocked node gets reset
// to the global baseline); random bumps have a single unambiguous correlation peak.
func bumpDiskImage(w, h int, seed int64, bumps int) *fits.Image {
	im := fits.NewImage(w, h, 1)
	p := im.Pix[0]
	cx, cy := float64(w)/2, float64(h)/2
	rad := 0.42 * math.Min(float64(w), float64(h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy <= rad*rad {
				p[y*w+x] = 0.5
			}
		}
	}
	rng := rand.New(rand.NewSource(seed))
	for b := 0; b < bumps; b++ {
		bx := cx + (rng.Float64()*2-1)*rad*0.9
		by := cy + (rng.Float64()*2-1)*rad*0.9
		amp := (rng.Float64()*2 - 1) * 0.18
		sig := 2 + rng.Float64()*3
		x0, x1 := int(bx-3*sig), int(bx+3*sig)
		y0, y1 := int(by-3*sig), int(by+3*sig)
		for y := max(y0, 0); y <= min(y1, h-1); y++ {
			for x := max(x0, 0); x <= min(x1, w-1); x++ {
				i := y*w + x
				if p[i] == 0 {
					continue // keep the sky black
				}
				ddx, ddy := float64(x)-bx, float64(y)-by
				p[i] += float32(amp * math.Exp(-(ddx*ddx+ddy*ddy)/(2*sig*sig)))
			}
		}
	}
	return im
}

// canonicalFixture builds a ground-truth scene for the canonical-geometry math: a textured
// truth disk and per-frame node displacement fields. Frame 0 (the reference) is distorted by
// a known smooth warp aRef; the other frames come in ±pairs, so at every node the median of
// their warps is EXACTLY zero — the property the estimator relies on. A paired global drift
// rides on top to exercise the off-disk DC anchor.
type canonicalFixture struct {
	truth  *fits.Image
	paths  []string
	dxTrue [][]float64 // per frame, per node (content-motion convention of warpByGrid)
	dyTrue [][]float64
}

func newCanonicalFixture(t *testing.T, dir string, w, h, pairs int) canonicalFixture {
	t.Helper()
	truth := bumpDiskImage(w, h, 42, 120)
	n := apGridN
	cxN, cyN := apCenters(w, h, n)

	// Patterns are built in DISK-CENTERED coordinates and the reference's uses phase 0, so its
	// sin(du)·cos(dv) shape is odd about the disk centre: its mean over the central global-lock
	// window is ~0. That matters for the truth comparison — the canonical master deliberately
	// keeps the reference's GLOBAL position, so any window-mean of the reference warp would show
	// up as a constant (harmless in reality, but a confound when diffing against the truth).
	warpPattern := func(ampX, ampY, fu, fv, phase float64) (dx, dy []float64) {
		dx = make([]float64, n*n)
		dy = make([]float64, n*n)
		for k := range dx {
			du, dv := cxN[k]/float64(w)-0.5, cyN[k]/float64(h)-0.5
			dx[k] = ampX * math.Sin(2*math.Pi*(du*fu+phase)) * math.Cos(2*math.Pi*dv*fv)
			dy[k] = ampY * math.Sin(2*math.Pi*(dv*fv+phase)) * math.Cos(2*math.Pi*du*fu)
		}
		return dx, dy
	}
	addConst := func(g []float64, c float64) {
		for k := range g {
			g[k] += c
		}
	}

	fx := canonicalFixture{truth: truth}
	refDx, refDy := warpPattern(1.2, 1.0, 1.0, 1.5, 0) // the reference's own frozen seeing
	fx.dxTrue = append(fx.dxTrue, refDx)
	fx.dyTrue = append(fx.dyTrue, refDy)
	for m := 0; m < pairs; m++ {
		pDx, pDy := warpPattern(1.0+0.1*float64(m), 0.9, 1.0+0.2*float64(m), 1.2, 0.13*float64(m))
		gShift := 0.8 * math.Sin(float64(m+1))
		for _, sign := range []float64{1, -1} {
			dx := make([]float64, len(pDx))
			dy := make([]float64, len(pDy))
			for k := range dx {
				dx[k] = sign * pDx[k]
				dy[k] = sign * pDy[k]
			}
			addConst(dx, sign*gShift) // paired pointing drift → median exactly zero
			addConst(dy, -sign*gShift*0.5)
			fx.dxTrue = append(fx.dxTrue, dx)
			fx.dyTrue = append(fx.dyTrue, dy)
		}
	}
	for i := range fx.dxTrue {
		frame := warpByGrid(truth, fx.dxTrue[i], fx.dyTrue[i])
		p := filepath.Join(dir, fmt.Sprintf("cf_%02d.fits", i))
		require.NoError(t, frame.WriteFITS(p))
		fx.paths = append(fx.paths, p)
	}
	return fx
}

// coreNodes are the on-disk nodes whose full 3×3 neighborhood is on-disk — the nodes where the
// measured (smoothed) field is comparable to a smoothed ground truth without edge mixing.
func coreNodes(onDisk []bool) []int {
	n := gridSize(make([]float64, len(onDisk)))
	var core []int
	for j := 1; j < n-1; j++ {
	next:
		for i := 1; i < n-1; i++ {
			for dj := -1; dj <= 1; dj++ {
				for di := -1; di <= 1; di++ {
					if !onDisk[(j+dj)*n+(i+di)] {
						continue next
					}
				}
			}
			core = append(core, j*n+i)
		}
	}
	return core
}

func TestCanonicalizeFields_RecoversReferenceWarp(t *testing.T) {
	dir := t.TempDir()
	const w, h = 256, 256
	fx := newCanonicalFixture(t, dir, w, h, 5) // ref + 10 paired frames

	ref, err := fits.ReadImage(fx.paths[0])
	require.NoError(t, err)
	rc := newRefContext(ref)
	rcD := denseContextFrom(ref, rc, 0)
	dx, dy, err := measureAllFields(context.Background(), fx.paths, 0, &rc, &rcD)
	require.NoError(t, err)
	canonicalizeFields(dx, dy, 0, rcD.onDisk)

	// The reference's corrected field is −C; C must match the reference's own warp (up to the
	// global DC the anchor deliberately leaves in the master's position, and in the smoothed
	// space the field estimator works in).
	wantDx := append([]float64(nil), fx.dxTrue[0]...)
	wantDy := append([]float64(nil), fx.dyTrue[0]...)
	smoothGrid(wantDx)
	smoothGrid(wantDy)
	core := coreNodes(rcD.onDisk)
	require.GreaterOrEqual(t, len(core), 8, "fixture must keep a solid on-disk core")
	var sumX, sumY float64
	for _, k := range core {
		sumX += -dx[0][k] - wantDx[k]
		sumY += -dy[0][k] - wantDy[k]
	}
	dcX, dcY := sumX/float64(len(core)), sumY/float64(len(core))
	for _, k := range core {
		assert.InDelta(t, wantDx[k], -dx[0][k]-dcX, 0.35, "C_x at node %d recovers the reference warp", k)
		assert.InDelta(t, wantDy[k], -dy[0][k]-dcY, 0.35, "C_y at node %d recovers the reference warp", k)
	}

	// Far off-disk (pure sky neighborhoods, beyond smoothGrid's limb mixing) the correction
	// must stay ~zero (anchored): the limb keeps its continuity and the master its position.
	pure := 0
	for k, on := range rcD.onDisk {
		if on || !offDiskNeighborhood(rcD.onDisk, rcD.gridN, k) {
			continue
		}
		pure++
		assert.InDelta(t, 0, dx[0][k], 0.3, "far off-disk C_x node %d", k)
		assert.InDelta(t, 0, dy[0][k], 0.3, "far off-disk C_y node %d", k)
	}
	require.GreaterOrEqual(t, pure, dcMinOffDisk, "fixture must keep enough pure sky nodes to anchor the DC")
}

func TestCanonicalizeFields_BeatsReferenceGeometry(t *testing.T) {
	dir := t.TempDir()
	const w, h = 256, 256
	fx := newCanonicalFixture(t, dir, w, h, 5)

	ref, err := fits.ReadImage(fx.paths[0])
	require.NoError(t, err)
	rc := newRefContext(ref)
	rcD := denseContextFrom(ref, rc, 0)
	dx, dy, err := measureAllFields(context.Background(), fx.paths, 0, &rc, &rcD)
	require.NoError(t, err)
	legacyDx := make([][]float64, len(dx))
	legacyDy := make([][]float64, len(dy))
	for i := range dx {
		legacyDx[i] = append([]float64(nil), dx[i]...)
		legacyDy[i] = append([]float64(nil), dy[i]...)
	}
	canonicalizeFields(dx, dy, 0, rcD.onDisk)

	meanOf := func(fdx, fdy [][]float64, warpRef bool) []float64 {
		sum := make([]float64, w*h)
		for i := range fx.paths {
			im, rerr := fits.ReadImage(fx.paths[i])
			require.NoError(t, rerr)
			aligned := im
			if i != 0 || warpRef {
				aligned = warpByGrid(im, fdx[i], fdy[i])
			}
			for j, v := range aligned.Pix[0] {
				sum[j] += float64(v)
			}
		}
		for j := range sum {
			sum[j] /= float64(len(fx.paths))
		}
		return sum
	}
	ssdVsTruth := func(mean []float64) float64 {
		var ssd float64
		for y := h/2 - 60; y < h/2+60; y++ {
			for x := w/2 - 60; x < w/2+60; x++ {
				d := mean[y*w+x] - float64(fx.truth.Pix[0][y*w+x])
				ssd += d * d
			}
		}
		return ssd
	}

	ssdLegacy := ssdVsTruth(meanOf(legacyDx, legacyDy, false))
	ssdCanonical := ssdVsTruth(meanOf(dx, dy, true))
	assert.Less(t, ssdCanonical, 0.6*ssdLegacy,
		"the canonical-geometry stack must sit closer to the TRUE undistorted scene than the reference-geometry stack (legacy %.4g vs canonical %.4g)",
		ssdLegacy, ssdCanonical)
}

func TestWarpToSharpest_FewFramesFallsBack(t *testing.T) {
	dir := t.TempDir()
	const w, h = 200, 200
	ref := diskImage(w, h, 100, 100, 40)
	var paths []string
	var scores []float64
	for i := 0; i < canonicalMin-1; i++ { // one below the floor
		p := filepath.Join(dir, fmt.Sprintf("ff_%02d.fits", i))
		require.NoError(t, ref.WriteFITS(p))
		paths = append(paths, p)
		scores = append(scores, float64(10-i))
	}

	res, err := warpToSharpest(context.Background(), paths, scores, dir, "ff", true, 1, 0, nil)
	require.NoError(t, err)
	require.Len(t, res.paths, len(paths))
	assert.Contains(t, res.note, "skipped", "below the floor the run must say canonical geometry was skipped")
	assert.Nil(t, res.dxFields[0], "legacy path: the reference carries no field")

	out, err := fits.ReadImage(res.refPath)
	require.NoError(t, err)
	assert.Equal(t, ref.Pix[0], out.Pix[0], "legacy path: the reference is written unresampled, byte-identical")
}

func TestCanonicalizeFields_Deterministic(t *testing.T) {
	dir := t.TempDir()
	fx := newCanonicalFixture(t, dir, 256, 256, 5)
	ref, err := fits.ReadImage(fx.paths[0])
	require.NoError(t, err)
	rc := newRefContext(ref)

	rcD := denseContextFrom(ref, rc, 0)
	run := func() ([][]float64, [][]float64) {
		dx, dy, merr := measureAllFields(context.Background(), fx.paths, 0, &rc, &rcD)
		require.NoError(t, merr)
		canonicalizeFields(dx, dy, 0, rcD.onDisk)
		return dx, dy
	}
	dx1, dy1 := run()
	dx2, dy2 := run()
	assert.Equal(t, dx1, dx2, "canonical fields must be identical across runs")
	assert.Equal(t, dy1, dy2)
}
