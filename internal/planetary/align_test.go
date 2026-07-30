package planetary

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// diskImage builds a W×H mono frame with a bright disk (radius rad) centred at (cx,cy) plus a brighter
// off-centre spot — enough structure (limb + feature) for both the centroid and the ZNCC to lock.
func diskImage(w, h int, cx, cy, rad float64) *fits.Image {
	im := fits.NewImage(w, h, 1)
	p := im.Pix[0]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			r := math.Hypot(dx, dy)
			if r <= rad {
				p[y*w+x] = 0.5
			}
			// A bright "crater" feature off-centre gives the correlation a unique peak.
			if math.Hypot(float64(x)-(cx+10), float64(y)-(cy-8)) <= rad/4 {
				p[y*w+x] = 1.0
			}
		}
	}
	return im
}

func TestWarpToSharpest_RecoversShift(t *testing.T) {
	dir := t.TempDir()
	const w, h = 200, 200
	ref := diskImage(w, h, 100, 100, 40)

	// Frame 0 is the reference (sharpest); the others are shifted by known sub-pixel amounts.
	shifts := []struct{ dx, dy float64 }{{0, 0}, {7, -5}, {-6.5, 9.25}}
	var paths []string
	for i, s := range shifts {
		im := comet.Translate(ref, s.dx, s.dy) // ground-truth shifted frame
		p := filepath.Join(dir, fmt.Sprintf("in_%02d.fits", i))
		require.NoError(t, im.WriteFITS(p))
		paths = append(paths, p)
	}
	scores := []float64{10, 5, 4} // frame 0 sharpest → alignment reference

	// The per-frame progress hook may fire concurrently from the parallel workers — count under a lock.
	var mu sync.Mutex
	ticks := 0
	res, err := warpToSharpest(context.Background(), paths, scores, dir, "al", true, 1, 0,
		func(done, total int) {
			mu.Lock()
			ticks++
			mu.Unlock()
			assert.Equal(t, 3, total)
		})
	require.NoError(t, err)
	out := res.paths
	require.Len(t, out, 3)
	assert.Equal(t, out[0], res.refPath, "frame 0 is the sharpest → the reference")
	assert.Nil(t, res.dxFields[0], "the unresampled reference carries no field")
	require.NotNil(t, res.dxFields[1], "warped frames keep their measured field for the double-stack seed")
	assert.Equal(t, []int{0, 1, 2}, res.srcIdx)
	assert.Equal(t, 3, ticks, "one progress tick per aligned frame")

	// Every aligned frame's feature must land back at the reference centroid (within ~1 px).
	refX, refY := brightCentroid(ref)
	for i, p := range out {
		im, err := fits.ReadImage(p)
		require.NoError(t, err)
		cx, cy := brightCentroid(im)
		assert.InDelta(t, refX, cx, 1.0, "aligned frame %d cx", i)
		assert.InDelta(t, refY, cy, 1.0, "aligned frame %d cy", i)
	}
}

// Output names are pre-assigned by INPUT position (parallel workers own their slot), so an unreadable
// frame leaves a numbering gap — harmless by design: no Siril sequence is ever built over the aligned
// dir, only the returned (compacted) path list is consumed.
func TestWarpToSharpest_SkipsUnreadableLeavingNamingGap(t *testing.T) {
	dir := t.TempDir()
	ref := diskImage(120, 120, 60, 60, 25)
	good := filepath.Join(dir, "g.fits")
	require.NoError(t, ref.WriteFITS(good))
	res, err := warpToSharpest(context.Background(),
		[]string{good, filepath.Join(dir, "missing.fits"), good},
		[]float64{9, 1, 5}, dir, "al", true, 1, 0, nil)
	require.NoError(t, err)
	// The unreadable middle frame drops; the survivors keep their input-position names (slot 2 → 00003)
	// and every parallel array stays index-aligned with the compacted output list.
	require.Len(t, res.paths, 2)
	assert.Equal(t, "al_00001.fits", filepath.Base(res.paths[0]))
	assert.Equal(t, "al_00003.fits", filepath.Base(res.paths[1]))
	assert.Len(t, res.cellSharp, 2, "cellSharp compacted alongside the aligned list")
	assert.Equal(t, []int{0, 2}, res.srcIdx, "srcIdx maps each survivor back to its input position")
}
