package meteor

// streak.go finds streaks in ONE ORIGINAL frame, before registration and before stacking.
//
// This replaces detection in the rejected layer, which was tried first and does not work. The reason
// is worth recording because it is not obvious: the sigma-clip rejects the moving edge of every
// trailed star in the field, so the layer it hands over is mostly star residue, and that residue is
// BRIGHTER than the meteors. Every threshold that reaches a meteor has already marked thousands of
// stars. Detection has to happen where the stars are still stars, which is the original frame.
//
// The primitive is a GREY-SCALE OPENING ALONG A LINE, not a threshold and not a Hough transform.
//
// Opening with a line-shaped structuring element of length L keeps exactly those structures that
// contain an unbroken run of L pixels in the tested direction, and deletes everything else — not
// "attenuates", deletes, because the erosion takes the MINIMUM along the line and a star has
// background at both ends of any line longer than the star. So a 5-pixel star is gone for every
// direction, while a 200-pixel streak survives at its own. Brightness never enters into it. That is
// what makes it able to find a meteor that is FAINTER than the stars it sits among, which is the
// case that defeated every earlier attempt.
//
// Hough was tried before this and failed for a reason that also generalises: it is a global vote, so
// it needs a threshold to pick seed pixels before any geometry is considered, and on a frame filled
// with Milky Way there is no threshold that admits a faint meteor without admitting half the sky.
// The opening asks the geometric question FIRST and the brightness question only afterwards, of the
// handful of pixels that survive.
//
// Two implementation choices carry the cost:
//
//   - The image is binned before anything else. This is not just for speed: binning RAISES a streak's
//     signal-to-noise. Noise falls with the bin area while a line's signal falls only with the width
//     dilution, so a 2-pixel-wide streak gains a factor of about Bin/2 — the opposite of what binning
//     does to a star.
//   - The extremum along the line is computed by the van Herk / Gil-Werman scheme, which costs a
//     constant number of comparisons per pixel however long the element is. Without it, a 21-long
//     element at 32 orientations would be ~700 taps per pixel and the detector would be unusable in a
//     pipeline rather than merely slow.

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// StreakOptions tune single-frame detection. Lengths marked "binned" are in the units of the binned
// image, so they scale with Bin; DetectStreaks reports its results back in native pixels.
type StreakOptions struct {
	// Bin box-averages the frame before anything else. See the note above: for a thin line this
	// improves the signal-to-noise rather than costing it.
	Bin int
	// TilePx is the tile the background and the noise are measured over, in binned pixels. It must be
	// comfortably larger than LenPx or the tiles start absorbing the streaks themselves.
	TilePx int
	// BgPercentile is the level taken as "sky" inside a tile. Well below the median because the tile
	// contains stars, and a percentile is used rather than a mean so that a bright star cannot drag
	// the background up with it.
	BgPercentile float64
	// Angles is how many orientations are tried over 180 degrees. This is not a quality knob to be
	// turned up idly: it is set by geometry. A line element sampled at an angle error e wanders
	// LenPx*sin(e) pixels off a real streak over its length, and once that exceeds the streak's width
	// the erosion finds background and the response collapses.
	Angles int
	// LenPx is the structuring element's length in binned pixels: the shortest streak that can be
	// detected at all.
	LenPx int
	// Thicken dilates the frame by a 3x3 maximum before the opening, widening a streak so the line
	// element can be a pixel or so off it and still find signal. It widens stars by the same amount,
	// which costs nothing because they are killed by length, not by width.
	Thicken int
	// MinZ is the detection threshold on the opened map, in units of THAT MAP'S OWN spread — not of
	// the pixel noise.
	//
	// This distinction is the difference between the detector working and not working here. Measured
	// on a real Milky Way panel, the median pixel of the response already scores 1.33 sigma of pixel
	// noise: at 64 arcsec per pixel the galactic band is a sea of blended faint stars, so half the
	// frame genuinely carries a continuous linear ridge. Pixel noise is therefore the wrong ruler —
	// the floor a streak has to clear is set by the star field, which is a hundred times higher and
	// varies across the sky. Measuring the response's own median and spread per frame gets that floor
	// for free, and makes the same threshold mean the same thing in the crowded core and in an empty
	// corner of sky.
	MinZ float64
	// MinDuty is the fraction of the streak's length that must actually be lit IN THE FRAME, and it is
	// what separates a meteor from a chance alignment of stars. A meteor is a continuous ridge; a star
	// chain is lit at the stars and dark between them. It cannot be measured on the opened map, whose
	// dilation step fills in precisely those gaps — every one of 2821 real candidates scored exactly
	// 1.00 there, which is what exposed the mistake.
	MinDuty float64
	// MarginPx ignores a border in binned pixels — the lens vignette's edge is a long curved ridge and
	// would otherwise be the strongest linear structure in the frame.
	MarginPx int
	// MinLengthPx and MinAspect are measured on the surviving components, in binned pixels. They are a
	// second, weaker filter: the opening has already imposed LenPx, and these reject the L-shaped
	// junk that survives it by being long in two directions at once.
	MinLengthPx, MinAspect float64
	// BridgePx closes the gaps a faint streak breaks into at threshold, so one meteor is one
	// component rather than six.
	BridgePx int
}

// DefaultStreakOptions is calibrated for a 12 MP phone frame (4032x3024) at roughly 64 arcsec per
// pixel. Bin 4 puts the working image at 1008x756, where LenPx 21 is a 84-pixel streak in the
// original — about 1.5 degrees of sky, comfortably shorter than any meteor that is worth keeping and
// far longer than any star.
func DefaultStreakOptions() StreakOptions {
	return StreakOptions{
		Bin: 4, TilePx: 96, BgPercentile: 40,
		Angles: 32, LenPx: 21, Thicken: 1,
		MinZ: 6, MinDuty: 0.5, MarginPx: 8,
		MinLengthPx: 24, MinAspect: 3.5, BridgePx: 2,
	}
}

// DetectStreaks finds the streaks in one frame and returns them in NATIVE pixel coordinates.
//
// frame is the index recorded on each streak, so that a caller can concatenate several frames'
// results and hand the lot to classify, which is where a satellite is told from a meteor.
func DetectStreaks(im *fits.Image, frame int, o StreakOptions) []Streak {
	z, w, h, ok := whiten(im, o)
	if !ok {
		return nil
	}
	r := LinearResponse(z, w, h, o)
	return componentsOf(r, z, w, h, frame, o)
}

// whiten reduces a frame to one noise-normalised plane: binned, background removed, divided by the
// local noise. Everything downstream then works in units of sigma and is comparable between frames
// shot at different exposures or under different sky brightness.
func whiten(im *fits.Image, o StreakOptions) (z []float32, w, h int, ok bool) {
	if im == nil || im.W < 64 || im.H < 64 || len(im.Pix) == 0 {
		return nil, 0, 0, false
	}
	bin := o.Bin
	if bin < 1 {
		bin = 1
	}
	v, w, h := binDown(luminance(im), im.W, im.H, bin)
	if w < 32 || h < 32 {
		return nil, 0, 0, false
	}
	tile := o.TilePx
	if tile < 8 {
		tile = 8
	}
	bg := tileMap(v, w, h, tile, func(t []float32) float64 {
		return imgops.Percentile(t, o.BgPercentile)
	})
	res := make([]float32, len(v))
	for i := range v {
		res[i] = v[i] - bg[i]
	}
	// The noise is measured as a median absolute deviation, which a streak crossing the tile cannot
	// move: it would have to occupy half the tile to shift a median.
	sig := tileMap(res, w, h, tile, func(t []float32) float64 {
		return 1.4826 * medianAbs(t)
	})
	z = make([]float32, len(res))
	for i := range res {
		s := sig[i]
		if s <= 0 {
			continue
		}
		z[i] = res[i] / s
	}
	return z, w, h, true
}

// LinearResponse is the detector proper: for every pixel, the strongest opening over all tested
// orientations. A pixel scores highly only if it lies on an unbroken run of LenPx pixels of signal in
// SOME direction, which is the definition of a streak and is false of everything else in the frame.
func LinearResponse(z []float32, w, h int, o StreakOptions) []float32 {
	src := z
	for i := 0; i < o.Thicken; i++ {
		src = dilate3(src, w, h)
	}
	n := o.Angles
	if n < 4 {
		n = 4
	}
	k := o.LenPx
	if k < 3 {
		k = 3
	}
	out := make([]float32, len(z))
	for i := range out {
		out[i] = float32(math.Inf(-1))
	}
	tmp := make([]float32, len(z))
	op := make([]float32, len(z))
	buf := newLineBuf(w, h)
	for a := 0; a < n; a++ {
		theta := math.Pi * float64(a) / float64(n)
		// An opening is an erosion followed by a dilation, both along the same lines: the erosion
		// deletes everything shorter than the element, the dilation restores what survived to its
		// original extent.
		buf.apply(src, tmp, w, h, theta, k, false)
		buf.apply(tmp, op, w, h, theta, k, true)
		for i := range out {
			if op[i] > out[i] {
				out[i] = op[i]
			}
		}
	}
	for i := range out {
		if math.IsInf(float64(out[i]), -1) {
			out[i] = 0
		}
	}
	return out
}

// componentsOf turns the response map into measured streaks.
func componentsOf(r, z []float32, w, h, frame int, o StreakOptions) []Streak {
	mask := make([]bool, len(r))
	m := o.MarginPx
	med, spread := robustLevel(r, w, h, m)
	cut := med + o.MinZ*spread
	for y := m; y < h-m; y++ {
		for x := m; x < w-m; x++ {
			i := y*w + x
			mask[i] = float64(r[i]) > cut
		}
	}
	if o.BridgePx > 0 {
		grown := imgops.BinaryDilation(mask, w, h, o.BridgePx)
		for y := m; y < h-m; y++ {
			for x := m; x < w-m; x++ {
				mask[y*w+x] = grown[y*w+x]
			}
		}
	}
	labels, n := imgops.Label(mask, w, h)
	if n == 0 {
		return nil
	}
	groups := make([][]int, n+1)
	for i, lab := range labels {
		if lab > 0 {
			groups[lab] = append(groups[lab], i)
		}
	}
	bin := float64(o.Bin)
	if bin < 1 {
		bin = 1
	}
	var out []Streak
	for _, idx := range groups[1:] {
		if len(idx) < 6 {
			continue
		}
		s := measure(r, idx, w)
		if s.LengthPx < o.MinLengthPx || s.LengthPx/s.WidthPx < o.MinAspect {
			continue
		}
		s.Duty, s.Fullness = profileStats(profileAlong(z, w, h, s))
		if s.Duty < o.MinDuty {
			continue
		}
		s.Frame = frame
		// Back to the frame's own pixels, so a caller never has to know the detector binned.
		s.X1, s.Y1, s.X2, s.Y2 = s.X1*bin, s.Y1*bin, s.X2*bin, s.Y2*bin
		s.LengthPx *= bin
		s.WidthPx *= bin
		s.StraightnessPx *= bin
		out = append(out, s)
	}
	return out
}

// measure describes one component: its axis, its extent along that axis, its thickness, how far it
// bends away from straight, and how much of it is actually lit.
//
// Width and straightness are both measured ROBUSTLY, and that is not fussiness. The obvious
// definitions — width as the largest perpendicular extent, straightness as the scatter about the axis
// — were tried and both are wrong here:
//
//   - Largest extent is set by the single worst pixel, so one star that the bridging dilation happened
//     to weld onto the streak collapsed a real meteor's length-to-width ratio from 32 to 6, close
//     enough to the junk to be indistinguishable from it. A high percentile of the offsets cannot be
//     moved by a blob hanging off the side.
//   - Scatter about the axis measures WIDTH, not curvature: a straight bar scores exactly as badly as
//     a bent thread of the same thickness, so it cannot do the job classify asks of it. Curvature is
//     the wander of the CENTRELINE, so the pixels are first collapsed to one mean offset per step
//     along the axis, and the scatter of those means is what bends.
func measure(r []float32, idx []int, w int) Streak {
	var sx, sy float64
	for _, i := range idx {
		sx += float64(i % w)
		sy += float64(i / w)
	}
	nf := float64(len(idx))
	cx, cy := sx/nf, sy/nf
	var sxx, syy, sxy float64
	for _, i := range idx {
		dx, dy := float64(i%w)-cx, float64(i/w)-cy
		sxx += dx * dx
		syy += dy * dy
		sxy += dx * dy
	}
	sxx, syy, sxy = sxx/nf, syy/nf, sxy/nf
	theta := 0.5 * math.Atan2(2*sxy, sxx-syy)
	ux, uy := math.Cos(theta), math.Sin(theta)
	minT, maxT := math.Inf(1), math.Inf(-1)
	peak := 0.0
	offs := make([]float64, 0, len(idx))
	type acc struct {
		sum float64
		n   int
	}
	steps := map[int]*acc{}
	for _, i := range idx {
		dx, dy := float64(i%w)-cx, float64(i/w)-cy
		t, p := dx*ux+dy*uy, -dx*uy+dy*ux
		minT, maxT = math.Min(minT, t), math.Max(maxT, t)
		offs = append(offs, math.Abs(p))
		s := int(math.Round(t))
		a := steps[s]
		if a == nil {
			a = &acc{}
			steps[s] = a
		}
		a.sum += p
		a.n++
		if v := float64(r[i]); v > peak {
			peak = v
		}
	}
	span := maxT - minT
	sort.Float64s(offs)
	width := math.Max(2*offs[int(0.9*float64(len(offs)-1))], 1)
	// The centreline's own wander: one offset per step along the axis, then their scatter.
	var bend float64
	for _, a := range steps {
		m := a.sum / float64(a.n)
		bend += m * m
	}
	bend = math.Sqrt(bend / float64(len(steps)))
	return Streak{
		X1: cx + minT*ux, Y1: cy + minT*uy,
		X2: cx + maxT*ux, Y2: cy + maxT*uy,
		LengthPx: span, WidthPx: width, StraightnessPx: bend,
		Pixels:     len(idx), PeakExcess: peak,
	}
}

// profileAlong walks the fitted line THROUGH THE FRAME ITSELF and returns the brightness along it.
//
// At each step it takes the best value within a NARROW window either side. The width of that window
// is the whole difficulty. It has to be wide enough to tolerate the fitted axis being a pixel off the
// true ridge, and no wider — searching half a trail width, which is what this did first, defeats the
// measurement completely: a chain of stars a few pixels off the axis then fills in the very gaps that
// make it a chain, and 55 star chains scored a duty of 0.76 to 0.96 and were kept as meteors. They
// were plainly chains when the layer was rendered and looked at.
func profileAlong(z []float32, w, h int, s Streak) []float64 {
	dx, dy := s.X2-s.X1, s.Y2-s.Y1
	n := int(math.Hypot(dx, dy))
	if n < 2 {
		return nil
	}
	ux, uy := dx/float64(n), dy/float64(n)
	px, py := -uy, ux
	const reach = 1.5
	out := make([]float64, n+1)
	for i := 0; i <= n; i++ {
		bx, by := s.X1+ux*float64(i), s.Y1+uy*float64(i)
		best := float32(0)
		for d := -reach; d <= reach; d++ {
			x, y := int(math.Round(bx+px*d)), int(math.Round(by+py*d))
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			if v := z[y*w+x]; v > best {
				best = v
			}
		}
		out[i] = float64(best)
	}
	return out
}

// profileStats reduces the walk to how much of the trail is lit at all, and how much of it is lit
// BRIGHTLY.
//
// The reference level is a high percentile rather than the maximum, so that one star sitting on the
// trail cannot halve the answer by redefining what full brightness means.
func profileStats(p []float64) (duty, fullness float64) {
	if len(p) == 0 {
		return 0, 0
	}
	lit := 0
	for _, v := range p {
		if v > 1.5 {
			lit++
		}
	}
	duty = float64(lit) / float64(len(p))
	s := append([]float64(nil), p...)
	sort.Float64s(s)
	ref := s[int(0.9*float64(len(s)-1))]
	if ref <= 0 {
		return duty, 0
	}
	full := 0
	for _, v := range p {
		if v > 0.5*ref {
			full++
		}
	}
	return duty, float64(full) / float64(len(p))
}

// robustLevel is the response map's own middle and spread, measured where a detection could be made.
// A median and a median absolute deviation are used because a streak — the thing being looked for —
// must not be able to raise the bar it has to clear.
func robustLevel(v []float32, w, h, margin int) (med, spread float64) {
	var s []float32
	for y := margin; y < h-margin; y += 2 {
		for x := margin; x < w-margin; x += 2 {
			s = append(s, v[y*w+x])
		}
	}
	if len(s) < 64 {
		return 0, 1
	}
	sort.Slice(s, func(a, b int) bool { return s[a] < s[b] })
	med = float64(s[len(s)/2])
	for i := range s {
		s[i] = float32(math.Abs(float64(s[i]) - med))
	}
	sort.Slice(s, func(a, b int) bool { return s[a] < s[b] })
	spread = 1.4826 * float64(s[len(s)/2])
	if spread <= 0 {
		spread = 1
	}
	return med, spread
}

// ---- image helpers ----

// luminance averages the channels. A meteor is broadband and the phone's three planes are already
// white-balanced, so there is nothing to gain from weighting them and something to lose: a per-channel
// detector would find each streak three times.
func luminance(im *fits.Image) []float32 {
	if im.C == 1 {
		out := make([]float32, len(im.Pix[0]))
		copy(out, im.Pix[0])
		return out
	}
	out := make([]float32, im.W*im.H)
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		for i := range out {
			if i < len(p) {
				out[i] += p[i]
			}
		}
	}
	inv := float32(1) / float32(im.C)
	for i := range out {
		out[i] *= inv
	}
	return out
}

// binDown box-averages by k, discarding the ragged right and bottom edge.
func binDown(v []float32, w, h, k int) ([]float32, int, int) {
	if k <= 1 {
		out := make([]float32, len(v))
		copy(out, v)
		return out, w, h
	}
	bw, bh := w/k, h/k
	out := make([]float32, bw*bh)
	inv := float32(1) / float32(k*k)
	for by := 0; by < bh; by++ {
		for bx := 0; bx < bw; bx++ {
			var s float32
			for dy := 0; dy < k; dy++ {
				row := (by*k+dy)*w + bx*k
				for dx := 0; dx < k; dx++ {
					s += v[row+dx]
				}
			}
			out[by*bw+bx] = s * inv
		}
	}
	return out, bw, bh
}

// tileMap measures f on a grid of tiles and interpolates it back to full resolution, so the result is
// smooth rather than blocky. Used for both the background and the noise.
func tileMap(v []float32, w, h, tile int, f func([]float32) float64) []float32 {
	nx, ny := (w+tile-1)/tile, (h+tile-1)/tile
	if nx < 1 {
		nx = 1
	}
	if ny < 1 {
		ny = 1
	}
	grid := make([]float64, nx*ny)
	buf := make([]float32, 0, tile*tile)
	for gy := 0; gy < ny; gy++ {
		for gx := 0; gx < nx; gx++ {
			buf = buf[:0]
			for y := gy * tile; y < (gy+1)*tile && y < h; y++ {
				for x := gx * tile; x < (gx+1)*tile && x < w; x++ {
					buf = append(buf, v[y*w+x])
				}
			}
			if len(buf) == 0 {
				continue
			}
			grid[gy*nx+gx] = f(buf)
		}
	}
	out := make([]float32, w*h)
	at := func(gx, gy int) float64 {
		gx = clampInt(gx, 0, nx-1)
		gy = clampInt(gy, 0, ny-1)
		return grid[gy*nx+gx]
	}
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)/float64(tile) - 0.5
		gy := int(math.Floor(fy))
		ty := fy - float64(gy)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)/float64(tile) - 0.5
			gx := int(math.Floor(fx))
			tx := fx - float64(gx)
			a := at(gx, gy)*(1-tx) + at(gx+1, gy)*tx
			b := at(gx, gy+1)*(1-tx) + at(gx+1, gy+1)*tx
			out[y*w+x] = float32(a*(1-ty) + b*ty)
		}
	}
	return out
}

func medianAbs(t []float32) float64 {
	if len(t) == 0 {
		return 0
	}
	c := make([]float32, len(t))
	copy(c, t)
	sort.Slice(c, func(a, b int) bool { return c[a] < c[b] })
	med := float64(c[len(c)/2])
	for i := range c {
		c[i] = float32(math.Abs(float64(c[i]) - med))
	}
	sort.Slice(c, func(a, b int) bool { return c[a] < c[b] })
	return float64(c[len(c)/2])
}

// dilate3 is a 3x3 grey-scale maximum.
func dilate3(v []float32, w, h int) []float32 {
	out := make([]float32, len(v))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m := v[y*w+x]
			for dy := -1; dy <= 1; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					if p := v[yy*w+xx]; p > m {
						m = p
					}
				}
			}
			out[y*w+x] = m
		}
	}
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
