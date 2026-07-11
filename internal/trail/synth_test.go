package trail

import (
	"math"
	"math/rand"
)

// noisyPlane builds a w×h float32 plane of bg + Gaussian(0,sigma) noise, seeded for determinism.
func noisyPlane(w, h int, bg, sigma float64, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	p := make([]float32, w*h)
	for i := range p {
		p[i] = float32(bg + rng.NormFloat64()*sigma)
	}
	return p
}

// addStreak stamps a straight streak from (ax,ay) to (bx,by): every pixel whose projection lies on the
// segment gets amp·exp(-d²/2σ²) added, where d is its perpendicular distance (σ sets the streak width).
func addStreak(p []float32, w, h int, ax, ay, bx, by, amp, sigma float64) {
	dx, dy := bx-ax, by-ay
	length := math.Hypot(dx, dy)
	ux, uy := dx/length, dy/length
	nx, ny := -uy, ux
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rx, ry := float64(x)-ax, float64(y)-ay
			t := rx*ux + ry*uy
			if t < 0 || t > length {
				continue
			}
			d := rx*nx + ry*ny
			p[y*w+x] += float32(amp * math.Exp(-d*d/(2*sigma*sigma)))
		}
	}
}

// lineAngleDeg returns the segment's line orientation folded into [0,180).
func lineAngleDeg(s Segment) float64 {
	dx, dy := s.dirVec()
	return math.Mod(math.Atan2(dy, dx)*180/math.Pi+180, 180)
}
