// Lunar canvas assembly: merge the panel masters of one segmented sweep into a single image bigger
// than any frame that went into it.
//
// The deep-sky mosaic (internal/mosaic) cannot be reused here. It reprojects panels through a plate
// solve onto a TAN sky grid, and the Moon has no stars to solve against — its surface IS the
// registration target. So panels are placed by the same starless cross-correlation the lucky stack
// uses: the drift trajectory gives each panel's approximate offset, and a full-resolution ZNCC over
// the overlap refines it to sub-pixel before the blend.
package planetary

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	// panelRefineFrac is the search radius when refining a panel's placement, as a fraction of the
	// smaller panel axis — applied on the COARSE plane. The trajectory seed is already good to a few
	// percent; this only has to absorb its accumulated error.
	panelRefineFrac = 0.12
	// refineCoarseDim is the long-axis size of the plane stage 1 searches on.
	refineCoarseDim = 384
	// refineFineWindow caps the full-resolution correlation window. Sub-pixel registration on a
	// textured lunar surface needs a few hundred pixels, not a few thousand, and the cost of the
	// fine search is quadratic in this number.
	refineFineWindow = 256
	// panelMinOverlap is how much of each axis two panels must still share for a candidate placement
	// to be scored. Low by design: consecutive panels are stepped apart deliberately and may share
	// little more than their blend margin.
	panelMinOverlap = 0.2
	// panelFeatherFrac is the blend ramp width as a fraction of the smaller panel axis. Panels
	// overlap by roughly (panelDriftFrac−panelStepFrac) of the axis, so the ramp stays inside it.
	panelFeatherFrac = 0.10
	// canvasMaxPixels caps the assembled canvas so a runaway trajectory fails loudly instead of
	// trying to allocate a gigapixel image.
	canvasMaxPixels = int64(20000) * int64(20000)
)

// placedPanel is a stacked panel master with its refined placement on the canvas.
type placedPanel struct {
	Label string
	Image *fits.Image
	OffX  float64 // canvas x of the panel's (0,0), before the canvas origin shift
	OffY  float64
	Gain  float64
	Bias  float64
}

// assemblePanels merges panel masters onto one canvas. seeds[i] is panel i's approximate offset from
// the drift trajectory (in master pixels — already scaled by the drizzle factor by the caller).
// Panels are placed in capture order, each refined against the canvas built so far, photometrically
// matched to it over the overlap, then blended with a feathered, coverage-weighted accumulation.
//
// Returns the canvas and the notes describing what was placed. A panel that cannot be refined keeps
// its trajectory seed (a note says so) rather than being dropped: a slightly misplaced panel is a
// soft seam, a dropped one is a hole.
func assemblePanels(ctx context.Context, masters []string, labels []string, seeds []struct{ X, Y float64 }) (*fits.Image, []string, error) {
	if len(masters) == 0 {
		return nil, nil, fmt.Errorf("no panel masters to assemble")
	}
	panels := make([]placedPanel, 0, len(masters))
	for i, m := range masters {
		im, err := fits.ReadImage(m)
		if err != nil {
			return nil, nil, fmt.Errorf("read panel master %s: %w", m, err)
		}
		panels = append(panels, placedPanel{Label: labels[i], Image: im, OffX: seeds[i].X, OffY: seeds[i].Y, Gain: 1})
	}
	var notes []string

	// Refine each panel against the running mosaic of everything placed before it. Building the
	// reference incrementally (rather than pairwise to panel 0) is what lets a long sweep close:
	// panel N overlaps panel N−1, not panel 0.
	for i := 1; i < len(panels); i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		dx, dy, corr, ok := refineAgainst(panels[:i], panels[i])
		switch {
		case !ok:
			notes = append(notes, fmt.Sprintf("panel %s: no overlap to refine against, placed from the drift track", panels[i].Label))
		case corr < driftMinCorr:
			notes = append(notes, fmt.Sprintf("panel %s: weak overlap correlation %.2f, kept the drift-track placement", panels[i].Label, corr))
		default:
			panels[i].OffX, panels[i].OffY = dx, dy
		}
	}
	// Photometric match: each panel is scaled/offset onto the first panel's level over its overlap,
	// so a sweep whose exposure drifted (phone auto-exposure walks with the lit fraction in frame)
	// does not band the canvas.
	for i := 1; i < len(panels); i++ {
		if g, b, ok := matchLevels(panels[:i], panels[i]); ok {
			panels[i].Gain, panels[i].Bias = g, b
		}
	}
	canvas, err := blendPanels(panels)
	if err != nil {
		return nil, nil, err
	}
	notes = append(notes, fmt.Sprintf("lunar canvas: %d panels merged into %d×%d px", len(panels), canvas.W, canvas.H))
	return canvas, notes, nil
}

// refineAgainst measures panel p's true offset by correlating it against the panels already placed.
// The reference is the placed panel it overlaps most.
//
// Coarse-to-fine, and it has to be. A panel master is tens of megapixels and the trajectory seed can
// be wrong by a few percent of it, so a single full-resolution search over that range would be
// hundreds of thousands of candidate shifts against a multi-megapixel window — it does not finish.
// Stage 1 finds the offset on ~refineCoarseDim-wide planes, scored over the whole overlap (the only
// thing that localizes a large shift); stage 2 refines what is left to sub-pixel at full resolution
// with a small, bounded window.
func refineAgainst(placed []placedPanel, p placedPanel) (dx, dy, corr float64, ok bool) {
	best := -1
	bestArea := 0.0
	for i, q := range placed {
		if a := overlapArea(q, p); a > bestArea {
			best, bestArea = i, a
		}
	}
	if best < 0 || bestArea < 0.02*float64(p.Image.W*p.Image.H) {
		return 0, 0, 0, false
	}
	ref := placed[best]
	relX, relY := p.OffX-ref.OffX, p.OffY-ref.OffY
	rl, pl := lumaPlane(ref.Image), lumaPlane(p.Image)
	if rl == nil || pl == nil {
		return 0, 0, 0, false
	}

	// Stage 1: coarse, over the full overlap.
	k := max(rl.W, rl.H) / refineCoarseDim
	if k < 1 {
		k = 1
	}
	rs, ps := lumaDown(rl, k), lumaDown(pl, k)
	if rs == nil || ps == nil {
		return 0, 0, 0, false
	}
	reach := int(math.Max(4, panelRefineFrac*float64(min(rs.W, rs.H))))
	cx, cy, ccorr := shiftByOverlap(rs, ps, int(math.Round(relX/float64(k))), int(math.Round(relY/float64(k))),
		reach, panelMinOverlap)
	if ccorr < driftMinCorr {
		return 0, 0, ccorr, false
	}
	relX, relY = float64(cx*k), float64(cy*k)

	// Stage 2: sub-pixel, full resolution, small bounded window centred on the overlap.
	full := shiftPlane(pl, rl.W, rl.H, relX, relY)
	ox0, oy0 := math.Max(0, relX), math.Max(0, relY)
	ox1 := math.Min(float64(rl.W), relX+float64(pl.W))
	oy1 := math.Min(float64(rl.H), relY+float64(pl.H))
	center := comet.Point{X: (ox0 + ox1) / 2, Y: (oy0 + oy1) / 2}
	radius := int(math.Min(math.Min(ox1-ox0, oy1-oy0)/2.5, refineFineWindow))
	if radius < 24 {
		return ref.OffX + relX, ref.OffY + relY, ccorr, true // coarse answer is all there is
	}
	fx, fy := comet.AlignSeeded(rl, full, center, radius, float64(k)+2, 1, 0, 0)
	fcorr := znccAt(rl, full, center, radius, fx, fy)
	if fcorr < ccorr {
		return ref.OffX + relX, ref.OffY + relY, ccorr, true // the fine pass made it worse; keep coarse
	}
	return ref.OffX + relX + fx, ref.OffY + relY + fy, fcorr, true
}

// lumaPlane returns a single-plane luminance view of an image (the plane itself when already mono).
func lumaPlane(im *fits.Image) *fits.Image {
	if im == nil || im.C == 0 {
		return nil
	}
	if im.C == 1 {
		return im
	}
	out := fits.NewImage(im.W, im.H, 1)
	inv := float32(1) / float32(im.C)
	for i := range out.Pix[0] {
		var s float32
		for ch := 0; ch < im.C; ch++ {
			s += im.Pix[ch][i]
		}
		out.Pix[0][i] = s * inv
	}
	return out
}

// shiftPlane renders src onto a w×h grid translated by (dx,dy), bilinearly. Outside pixels are 0.
func shiftPlane(src *fits.Image, w, h int, dx, dy float64) *fits.Image {
	out := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		sy := float64(y) - dy
		for x := 0; x < w; x++ {
			out.Pix[0][y*w+x] = bilinear(src, float64(x)-dx, sy)
		}
	}
	return out
}

func bilinear(im *fits.Image, x, y float64) float32 {
	if im == nil || x < 0 || y < 0 || x > float64(im.W-1) || y > float64(im.H-1) {
		return 0
	}
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, im.W-1), min(y0+1, im.H-1)
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	p := im.Pix[0]
	a := p[y0*im.W+x0]*(1-fx) + p[y0*im.W+x1]*fx
	b := p[y1*im.W+x0]*(1-fx) + p[y1*im.W+x1]*fx
	return a*(1-fy) + b*fy
}

// overlapArea is the area (in panel pixels) shared by two placed panels.
func overlapArea(a, b placedPanel) float64 {
	x0 := math.Max(a.OffX, b.OffX)
	y0 := math.Max(a.OffY, b.OffY)
	x1 := math.Min(a.OffX+float64(a.Image.W), b.OffX+float64(b.Image.W))
	y1 := math.Min(a.OffY+float64(a.Image.H), b.OffY+float64(b.Image.H))
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	return (x1 - x0) * (y1 - y0)
}

// matchLevels fits p = gain·ref + bias over the overlap with the already-placed panel it shares most
// area with, by a robust two-percentile fit (immune to the few saturated limb pixels a least-squares
// fit would chase).
func matchLevels(placed []placedPanel, p placedPanel) (gain, bias float64, ok bool) {
	best := -1
	bestArea := 0.0
	for i, q := range placed {
		if a := overlapArea(q, p); a > bestArea {
			best, bestArea = i, a
		}
	}
	if best < 0 || bestArea <= 0 {
		return 1, 0, false
	}
	ref := placed[best]
	var rv, pv []float64
	rl, pl := lumaPlane(ref.Image), lumaPlane(p.Image)
	x0 := math.Max(ref.OffX, p.OffX)
	y0 := math.Max(ref.OffY, p.OffY)
	x1 := math.Min(ref.OffX+float64(ref.Image.W), p.OffX+float64(p.Image.W))
	y1 := math.Min(ref.OffY+float64(ref.Image.H), p.OffY+float64(p.Image.H))
	stride := int(math.Max(1, math.Min(x1-x0, y1-y0)/256))
	for y := y0; y < y1; y += float64(stride) {
		for x := x0; x < x1; x += float64(stride) {
			a := bilinear(rl, x-ref.OffX, y-ref.OffY)
			b := bilinear(pl, x-p.OffX, y-p.OffY)
			if a <= 0 || b <= 0 {
				continue
			}
			rv = append(rv, float64(a)*ref.Gain+ref.Bias)
			pv = append(pv, float64(b))
		}
	}
	if len(rv) < 64 {
		return 1, 0, false
	}
	sort.Float64s(rv)
	sort.Float64s(pv)
	rLo, rHi := quantile(rv, 0.25), quantile(rv, 0.90)
	pLo, pHi := quantile(pv, 0.25), quantile(pv, 0.90)
	if pHi-pLo < 1e-6 {
		return 1, 0, false
	}
	gain = (rHi - rLo) / (pHi - pLo)
	if gain < 0.2 || gain > 5 {
		return 1, 0, false // implausible: keep the panel as measured
	}
	return gain, rLo - gain*pLo, true
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	return sorted[i]
}

// blendPanels accumulates every placed panel onto the union canvas with a smoothstep-feathered
// weight (1 in the panel interior, ramping to 0 at its edge), so overlaps cross-fade instead of
// stepping. Pixels no panel covers stay 0.
func blendPanels(panels []placedPanel) (*fits.Image, error) {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	channels := 1
	for _, p := range panels {
		minX = math.Min(minX, p.OffX)
		minY = math.Min(minY, p.OffY)
		maxX = math.Max(maxX, p.OffX+float64(p.Image.W))
		maxY = math.Max(maxY, p.OffY+float64(p.Image.H))
		if p.Image.C > channels {
			channels = p.Image.C
		}
	}
	w := int(math.Ceil(maxX - minX))
	h := int(math.Ceil(maxY - minY))
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("degenerate canvas %dx%d", w, h)
	}
	if int64(w)*int64(h) > canvasMaxPixels {
		return nil, fmt.Errorf("canvas %dx%d exceeds the sanity cap — the drift track is probably wrong", w, h)
	}
	sum := make([][]float64, channels)
	for ch := range sum {
		sum[ch] = make([]float64, w*h)
	}
	wsum := make([]float64, w*h)
	// Panels are sampled BILINEARLY at their fractional offset rather than dropped on the nearest
	// integer pixel. The refinement measures placement to a fraction of a pixel; rounding it away
	// would leave neighbouring panels up to half a pixel apart, and a half-pixel disagreement inside
	// a feathered overlap is a blurred seam — the one artifact a mosaic is judged on.
	for _, p := range panels {
		feather := math.Max(8, panelFeatherFrac*float64(min(p.Image.W, p.Image.H)))
		ox, oy := p.OffX-minX, p.OffY-minY
		x0, y0 := int(math.Floor(ox)), int(math.Floor(oy))
		x1, y1 := int(math.Ceil(ox))+p.Image.W, int(math.Ceil(oy))+p.Image.H
		planes := make([]*fits.Image, channels)
		for ch := 0; ch < channels; ch++ {
			planes[ch] = &fits.Image{W: p.Image.W, H: p.Image.H, C: 1,
				Pix: [][]float32{p.Image.Pix[min(ch, p.Image.C-1)]}}
		}
		for cy := max(0, y0); cy < min(h, y1); cy++ {
			sy := float64(cy) - oy
			for cx := max(0, x0); cx < min(w, x1); cx++ {
				sx := float64(cx) - ox
				if sx < 0 || sy < 0 || sx > float64(p.Image.W-1) || sy > float64(p.Image.H-1) {
					continue
				}
				wt := edgeWeight(int(sx), int(sy), p.Image.W, p.Image.H, feather)
				if wt <= 0 {
					continue
				}
				di := cy*w + cx
				for ch := 0; ch < channels; ch++ {
					sample := float64(bilinear(planes[ch], sx, sy))
					sum[ch][di] += wt * (sample*p.Gain + p.Bias)
				}
				wsum[di] += wt
			}
		}
	}
	out := fits.NewImage(w, h, channels)
	for i := range wsum {
		if wsum[i] <= 0 {
			continue
		}
		for ch := 0; ch < channels; ch++ {
			out.Pix[ch][i] = float32(sum[ch][i] / wsum[i])
		}
	}
	return out, nil
}

// edgeWeight is the smoothstep ramp from 0 at a panel's border to 1 once `feather` pixels inside.
func edgeWeight(x, y, w, h int, feather float64) float64 {
	d := float64(min(min(x, w-1-x), min(y, h-1-y)))
	if d >= feather {
		return 1
	}
	if d <= 0 {
		return 0
	}
	t := d / feather
	return t * t * (3 - 2*t)
}

// neutralizeMaster equalizes a colour master's channels over the LIT SURFACE, in place. It is the
// white balance a debayered camera raw never received. The reference is the subject itself: for the
// Moon — a grey body — matching the channel medians of the lit disc is the physically right answer,
// and it is measured over lit pixels only so the sky (which is noise around zero) cannot drag it.
//
// Returns a describing note, or "" when there was nothing to do (a mono master, or channels already
// within a few percent of each other).
func neutralizeMaster(path string) (string, error) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return "", err
	}
	if im.C < 3 {
		return "", nil
	}
	lum := lumaPlane(im)
	vals := append([]float32(nil), lum.Pix[0]...)
	sort.Slice(vals, func(a, b int) bool { return vals[a] < vals[b] })
	// The lit surface: everything above the midpoint between the sky floor and a high percentile.
	lo := float64(vals[len(vals)/100])
	hi := float64(vals[int(float64(len(vals))*0.995)])
	thr := float32(lo + 0.35*(hi-lo))
	med := make([]float64, im.C)
	for ch := 0; ch < im.C; ch++ {
		var lit []float32
		for i, v := range lum.Pix[0] {
			if v >= thr {
				lit = append(lit, im.Pix[ch][i])
			}
		}
		if len(lit) < 256 {
			return "", nil // not enough lit surface to balance against
		}
		sort.Slice(lit, func(a, b int) bool { return lit[a] < lit[b] })
		med[ch] = float64(lit[len(lit)/2])
	}
	target := (med[0] + med[1] + med[2]) / 3
	if target <= 0 {
		return "", nil
	}
	maxDev := 0.0
	for _, m := range med {
		if m <= 0 {
			return "", nil
		}
		if d := math.Abs(m/target - 1); d > maxDev {
			maxDev = d
		}
	}
	if maxDev < 0.03 {
		return "", nil // already neutral
	}
	gains := make([]float64, im.C)
	for ch := range gains {
		gains[ch] = target / med[ch]
		for i := range im.Pix[ch] {
			im.Pix[ch][i] = float32(float64(im.Pix[ch][i]) * gains[ch])
		}
	}
	if err := im.WriteFITS(path); err != nil {
		return "", err
	}
	return fmt.Sprintf("channel balance (debayered raw has no white balance): gains R %.2f G %.2f B %.2f, from the lit surface",
		gains[0], gains[1], gains[2]), nil
}
