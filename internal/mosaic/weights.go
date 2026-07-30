package mosaic

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// minFeatherPx floors the feather ramp so small panels still blend over a usable band.
const minFeatherPx = 16.0

// WeightMap is a panel's blending weight in PANEL pixel space: 0 at/outside the footprint edge,
// smoothstep-feathered to 1 in the interior. Center-weighting is what keeps every star round —
// each sky position is dominated by whichever panel renders it closest to its optical axis.
type WeightMap struct {
	W, H int
	Pix  []float32
}

// BuildWeights: valid mask = pixels > 0 eroded by edgeErodePx (kills ragged registration edges),
// then a chamfer distance transform to the nearest invalid pixel, then
// w = smoothstep(min(dist/featherPx, 1)). featherPx = featherFrac × overlapFrac × min(W,H),
// floor 16 px. The erosion is folded into the distance transform (dist − edgeErodePx): in the
// chamfer metric, eroding by e then measuring distance to the eroded boundary equals measuring
// distance to the original boundary and subtracting e — one two-pass transform instead of e
// dilation passes.
func BuildWeights(im *fits.Image, featherFrac, overlapFrac float64, edgeErodePx int) *WeightMap {
	w, h := im.W, im.H
	wm := &WeightMap{W: w, H: h, Pix: make([]float32, w*h)}
	if w <= 0 || h <= 0 || len(im.Pix) == 0 {
		return wm
	}
	valid := make([]bool, w*h)
	for i, v := range im.Pix[0] {
		valid[i] = v > 0
	}
	dist := chamferToInvalid(valid, w, h)
	featherPx := featherFrac * overlapFrac * float64(min(w, h))
	if featherPx < minFeatherPx {
		featherPx = minFeatherPx
	}
	for i, d := range dist {
		t := (float64(d)/3 - float64(edgeErodePx)) / featherPx
		if t <= 0 {
			continue
		}
		if t > 1 {
			t = 1
		}
		wm.Pix[i] = float32(t * t * (3 - 2*t))
	}
	return wm
}

// chamferToInvalid is the classic two-pass 3-4 chamfer distance transform: for every pixel, the
// approximate distance to the nearest invalid pixel in chamfer units (orthogonal step 3, diagonal
// 4 — divide by 3 for pixels). Everything beyond the image border counts as invalid, so even a
// fully-valid frame feathers from its border.
func chamferToInvalid(valid []bool, w, h int) []int32 {
	const inf = int32(math.MaxInt32 / 2)
	d := make([]int32, w*h)
	for i, v := range valid {
		if v {
			d[i] = inf
		}
	}
	at := func(x, y int) int32 {
		if x < 0 || x >= w || y < 0 || y >= h {
			return 0
		}
		return d[y*w+x]
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if d[i] == 0 {
				continue
			}
			d[i] = min(d[i], at(x-1, y)+3, at(x, y-1)+3, at(x-1, y-1)+4, at(x+1, y-1)+4)
		}
	}
	for y := h - 1; y >= 0; y-- {
		for x := w - 1; x >= 0; x-- {
			i := y*w + x
			if d[i] == 0 {
				continue
			}
			d[i] = min(d[i], at(x+1, y)+3, at(x, y+1)+3, at(x+1, y+1)+4, at(x-1, y+1)+4)
		}
	}
	return d
}
