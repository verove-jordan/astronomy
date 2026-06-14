package grade

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// makeGrid builds a deterministic test image: noisy background + compact star blobs, optionally
// with a straight bright streak across the frame.
func makeGrid(w, h int, withTrail bool) []float64 {
	g := make([]float64, w*h)
	seed := uint32(12345)
	rnd := func() float64 { seed = seed*1664525 + 1013904223; return float64(seed>>8&0xffff) / 65536 }
	for i := range g {
		g[i] = 100 + (rnd()-0.5)*10
	}
	for s := 0; s < 30; s++ {
		cx, cy := int(rnd()*float64(w)), int(rnd()*float64(h))
		for oy := -1; oy <= 1; oy++ {
			for ox := -1; ox <= 1; ox++ {
				if x, y := cx+ox, cy+oy; x >= 0 && x < w && y >= 0 && y < h {
					g[y*w+x] += 3000
				}
			}
		}
	}
	if withTrail {
		for t := 0.0; t < 1; t += 0.002 {
			x, y := int(5+t*float64(w-10)), int(8+t*float64(h-20))
			if x >= 0 && x < w && y >= 0 && y < h {
				g[y*w+x] += 6000
				if x+1 < w {
					g[y*w+x+1] += 6000
				}
			}
		}
	}
	return g
}

func TestDetectTrail(t *testing.T) {
	const w, h = 200, 200

	det, _ := DetectTrail(makeGrid(w, h, false), w, h)
	assert.False(t, det, "a star field with no streak must not be flagged")

	det, score := DetectTrail(makeGrid(w, h, true), w, h)
	assert.True(t, det, "a clear streak must be flagged")
	assert.Greater(t, score, trailVoteFrac)
}
