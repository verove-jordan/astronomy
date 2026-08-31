package skypano

// occluder.go finds the things that are not sky in a panel aimed at the sky.
//
// A bag on the ground, a tripod leg, a shoulder, the top of a tent — anything close to the camera
// that leans into the frame. They are not the landscape (the panel that carries the horizon deals
// with that, and this one is pointed well above it); they are objects that happen to be in the way,
// and rendered as sky they are painted onto the canvas as a smooth pale intrusion that nothing
// downstream can tell from a nebula.
//
// What separates them from sky is not brightness and not colour — a bag lit by a town is brighter
// than the sky and so is the Milky Way's core, and a dark tree is darker than the sky and so is a
// dust lane. It is STARS. Every part of the sky has them; nothing in the foreground does. So the
// discriminator is the density of point sources, and the test is deliberately conservative: a region
// counts as an occluder only if it has almost no star signal AND it reaches the frame's edge, since
// something in the way is attached to the world outside the frame rather than floating in the middle
// of it.

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// OccluderOptions tune the search.
type OccluderOptions struct {
	// TilePx is the grid the star density is measured on. It has to be big enough to hold several
	// stars of ordinary sky and small enough to follow the edge of the object.
	TilePx int
	// StarSigma is how far above a tile's OWN noise a pixel must be to count as a point source, and
	// MinStarFrac the share of the tile that must reach it for the tile to be sky.
	//
	// Measured against the tile's own noise, deliberately, and not against the frame's typical star
	// energy. That was the first attempt and it failed on the panel that mattered: with the Milky Way
	// filling the middle of the frame, the median tile is a BAND tile, and ordinary off-band sky sits
	// far below any sensible fraction of it — so 46% of that panel was masked, the beach and the whole
	// outer sky along with it. Faint sky is not an occluder. What actually separates them is that sky
	// has point sources at all, however few, and an object has none.
	StarSigma   float64
	MinStarFrac float64
	// MinAreaFrac is the smallest share of the frame an occluder may occupy. Below it the finding is
	// more likely to be a dust lane or a patch of cloud than an object.
	MinAreaFrac float64
	// MaxAreaFrac is the largest share it may occupy before the whole finding is thrown away.
	//
	// A cap rather than a trim, because past a certain size the answer is not "a very big object", it
	// is "this measurement is wrong". Measured: the panel carrying the horizon reported a third of
	// itself blocked — its beach, correctly, plus a lot of thin off-band sky — and dropping that much
	// of one panel changed WHICH panels cover the overlap regions, so each came out at its own
	// photometric level and the canvas grew visible rectangular seams. A landscape is the horizon
	// clearing's job, not this one; this one exists for things in the way, and those are small.
	MaxAreaFrac float64
	// FeatherPx softens the mask's edge so the object does not leave a hard rim on the canvas.
	FeatherPx int
}

func DefaultOccluderOptions() OccluderOptions {
	// 6 sigma admits nothing but real point sources; 1 in 4000 pixels is a couple of stars in a 64 px
	// tile, which the emptiest sky in this session still clears and an object never does.
	return OccluderOptions{TilePx: 64, StarSigma: 6, MinStarFrac: 2.5e-4,
		MinAreaFrac: 0.004, MaxAreaFrac: 0.12, FeatherPx: 48}
}

// FindOccluders returns a per-pixel validity mask for a panel: 1 where the panel may be used, 0 where
// something is in the way, feathered between. It returns nil when nothing qualifies, which is the
// normal case and means "use the whole panel".
func FindOccluders(im *fits.Image, o OccluderOptions) []float32 {
	if im == nil || o.TilePx < 8 {
		return nil
	}
	w, h := im.W, im.H
	resid := starResidual(im)

	// Per tile: how much of it is a point source, judged against that tile's OWN noise. Self-
	// calibrating is the whole point — a dim off-band tile has faint stars over faint noise and still
	// counts as sky, while an object has no point sources over any noise at all.
	tx, ty := (w+o.TilePx-1)/o.TilePx, (h+o.TilePx-1)/o.TilePx
	blocked := make([]bool, tx*ty)
	buf := make([]float64, 0, o.TilePx*o.TilePx)
	for gy := 0; gy < ty; gy++ {
		for gx := 0; gx < tx; gx++ {
			buf = buf[:0]
			for y := gy * o.TilePx; y < min((gy+1)*o.TilePx, h); y++ {
				for x := gx * o.TilePx; x < min((gx+1)*o.TilePx, w); x++ {
					buf = append(buf, resid[y*w+x])
				}
			}
			if len(buf) < 64 {
				continue
			}
			sigma := madSigma(buf)
			if sigma <= 0 {
				blocked[gy*tx+gx] = true // perfectly flat: not sky by any measure
				continue
			}
			hits := 0
			for _, v := range buf {
				if v > o.StarSigma*sigma {
					hits++
				}
			}
			blocked[gy*tx+gx] = float64(hits)/float64(len(buf)) < o.MinStarFrac
		}
	}

	// Only what reaches the edge. Flood from the border through blocked tiles.
	reach := make([]bool, tx*ty)
	var stack [][2]int
	push := func(x, y int) {
		if x < 0 || y < 0 || x >= tx || y >= ty {
			return
		}
		if i := y*tx + x; blocked[i] && !reach[i] {
			reach[i] = true
			stack = append(stack, [2]int{x, y})
		}
	}
	for x := 0; x < tx; x++ {
		push(x, 0)
		push(x, ty-1)
	}
	for y := 0; y < ty; y++ {
		push(0, y)
		push(tx-1, y)
	}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		push(c[0]+1, c[1])
		push(c[0]-1, c[1])
		push(c[0], c[1]+1)
		push(c[0], c[1]-1)
	}

	n := 0
	for _, r := range reach {
		if r {
			n++
		}
	}
	frac := float64(n) / float64(len(reach))
	if frac < o.MinAreaFrac {
		return nil
	}
	if o.MaxAreaFrac > 0 && frac > o.MaxAreaFrac {
		return nil // too much of the panel to be an object; see MaxAreaFrac
	}

	mask := make([]float32, w*h)
	for i := range mask {
		mask[i] = 1
	}
	for gy := 0; gy < ty; gy++ {
		for gx := 0; gx < tx; gx++ {
			if !reach[gy*tx+gx] {
				continue
			}
			for y := gy * o.TilePx; y < min((gy+1)*o.TilePx, h); y++ {
				for x := gx * o.TilePx; x < min((gx+1)*o.TilePx, w); x++ {
					mask[y*w+x] = 0
				}
			}
		}
	}
	if o.FeatherPx > 0 {
		mask = featherMask(mask, w, h, o.FeatherPx)
	}
	return mask
}

// starResidual is the SIGNED high-frequency content: what is left of a plane once the smooth part is
// taken away. Signed, not clipped at zero, because the negative half is what the noise estimate is
// made of — clip it and the noise looks smaller than it is and everything reads as full of stars.
func starResidual(im *fits.Image) []float64 {
	w, h := im.W, im.H
	lum := make([]float64, w*h)
	for i := range lum {
		var s float64
		for c := 0; c < im.C; c++ {
			s += float64(im.Pix[c][i])
		}
		lum[i] = s / float64(im.C)
	}
	const r = 3
	blur := boxBlur1(lum, w, h, r)
	out := make([]float64, w*h)
	for i := range lum {
		out[i] = lum[i] - blur[i]
	}
	return out
}

// madSigma is a noise estimate that a few hundred bright stars cannot inflate: the median absolute
// deviation, scaled to a Gaussian sigma.
func madSigma(v []float64) float64 {
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	med := c[len(c)/2]
	for i := range c {
		c[i] = math.Abs(c[i] - med)
	}
	sort.Float64s(c)
	return 1.4826 * c[len(c)/2]
}

func boxBlur1(p []float64, w, h, radius int) []float64 {
	tmp := make([]float64, len(p))
	out := make([]float64, len(p))
	for y := 0; y < h; y++ {
		var s float64
		var n int
		row := y * w
		for x := 0; x <= radius && x < w; x++ {
			s += p[row+x]
			n++
		}
		for x := 0; x < w; x++ {
			tmp[row+x] = s / float64(n)
			if add := x + radius + 1; add < w {
				s += p[row+add]
				n++
			}
			if drop := x - radius; drop >= 0 {
				s -= p[row+drop]
				n--
			}
		}
	}
	for x := 0; x < w; x++ {
		var s float64
		var n int
		for y := 0; y <= radius && y < h; y++ {
			s += tmp[y*w+x]
			n++
		}
		for y := 0; y < h; y++ {
			out[y*w+x] = s / float64(n)
			if add := y + radius + 1; add < h {
				s += tmp[add*w+x]
				n++
			}
			if drop := y - radius; drop >= 0 {
				s -= tmp[drop*w+x]
				n--
			}
		}
	}
	return out
}

// featherMask softens a hard 0/1 mask by box-blurring it, then keeps it at zero wherever it was
// zero — the object itself must stay fully excluded; only the approach to it ramps.
func featherMask(mask []float32, w, h, radius int) []float32 {
	d := make([]float64, len(mask))
	for i, v := range mask {
		d[i] = float64(v)
	}
	b := boxBlur1(d, w, h, radius)
	out := make([]float32, len(mask))
	for i := range mask {
		if mask[i] == 0 {
			continue
		}
		out[i] = float32(math.Min(math.Max(b[i], 0), 1))
	}
	return out
}
