package meteor

// render.go paints the streaks worth keeping into a layer that can be blended over the clean stack.
//
// The layer holds each streak at the brightness IT ACTUALLY HAD, taken from the single frame it
// appeared in. That is the whole point of keeping it separate: the clean stack is a mean of thirty-one
// frames, so a meteor that appears in one of them would be diluted to a thirty-first of itself if it
// survived at all. Adding back the background-subtracted excess restores it to what the eye saw.
//
// Detection is expected to have run on the REGISTERED frames, so a streak's coordinates already mean
// the same thing as the stack's. Nothing here transforms anything, and that is deliberate: a meteor
// belongs where it was photographed, and a registration meant for stars is the right transform for it
// only because both are far away.

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// RenderOptions tune how a streak is lifted out of its frame.
type RenderOptions struct {
	// PadPx widens the painted band beyond the measured width, so the streak's own faint wings come
	// with it rather than being cut off at a hard edge.
	PadPx float64
	// FeatherPx is the distance over which the band fades to nothing at its edge. Without it the
	// blend shows the rectangle rather than the meteor.
	FeatherPx float64
	// EndFeatherPx does the same along the trail's length, so it does not start and stop abruptly.
	EndFeatherPx float64
	// TilePx is the tile the per-channel background is measured over, in native pixels.
	TilePx int
	// BgPercentile is the level taken as sky inside a tile.
	BgPercentile float64
}

func DefaultRenderOptions() RenderOptions {
	return RenderOptions{PadPx: 6, FeatherPx: 4, EndFeatherPx: 12, TilePx: 384, BgPercentile: 40}
}

// RenderLayer paints every streak in ss into one image the size of the frames.
//
// load is called once per distinct frame, in order, so a caller can read them lazily rather than
// holding thirty-one full-resolution frames in memory at once.
//
// ref is the CLEAN STACK, and passing it matters more than any tuning knob here. What has to be
// lifted out of a frame is the transient alone, and a smooth sky model cannot deliver that: it leaves
// every star that happens to lie in the painted band, which renders as a speckled envelope around the
// trail — plainly visible once the layer is looked at. The clean stack is the same sky with the same
// stars in the same places, so subtracting it cancels them, and it cancels nothing of the meteor
// because the sigma-clip that built it had already thrown the meteor out. When ref is nil a tiled sky
// percentile is used instead, which works but carries the stars with it.
func RenderLayer(load func(frame int) (*fits.Image, error), ref *fits.Image, ss []Streak, o RenderOptions) (*fits.Image, error) {
	if len(ss) == 0 {
		return nil, nil
	}
	byFrame := map[int][]Streak{}
	var order []int
	for _, s := range ss {
		if _, ok := byFrame[s.Frame]; !ok {
			order = append(order, s.Frame)
		}
		byFrame[s.Frame] = append(byFrame[s.Frame], s)
	}
	sort.Ints(order)

	var out *fits.Image
	for _, f := range order {
		im, err := load(f)
		if err != nil {
			return nil, err
		}
		if im == nil {
			continue
		}
		if out == nil {
			out = fits.NewImage(im.W, im.H, im.C)
		}
		if im.W != out.W || im.H != out.H || im.C != out.C {
			// A frame of another shape cannot be composited onto this canvas, and quietly resizing it
			// would put the meteor in the wrong place. Skip it rather than lie about where it was.
			continue
		}
		bg := refPlanes(im, ref, o)
		for _, s := range byFrame[f] {
			paint(out, im, bg, s, o)
		}
	}
	return out, nil
}

// paint adds one streak's excess into the layer, keeping the brighter value where two overlap.
func paint(out, im *fits.Image, bg [][]float32, s Streak, o RenderOptions) {
	dx, dy := s.X2-s.X1, s.Y2-s.Y1
	length := math.Hypot(dx, dy)
	if length < 1 {
		return
	}
	ux, uy := dx/length, dy/length
	half := s.WidthPx/2 + o.PadPx
	// The band's bounding box, generously padded so the feather has room.
	pad := half + o.FeatherPx + o.EndFeatherPx
	x0 := clampInt(int(math.Floor(math.Min(s.X1, s.X2)-pad)), 0, out.W-1)
	x1 := clampInt(int(math.Ceil(math.Max(s.X1, s.X2)+pad)), 0, out.W-1)
	y0 := clampInt(int(math.Floor(math.Min(s.Y1, s.Y2)-pad)), 0, out.H-1)
	y1 := clampInt(int(math.Ceil(math.Max(s.Y1, s.Y2)+pad)), 0, out.H-1)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			ax, ay := float64(x)-s.X1, float64(y)-s.Y1
			t := ax*ux + ay*uy
			p := math.Abs(-ax*uy + ay*ux)
			wAcross := feather(half+o.FeatherPx-p, o.FeatherPx)
			if wAcross <= 0 {
				continue
			}
			// Along the trail: full inside, fading over EndFeatherPx past each end.
			wAlong := feather(math.Min(t+o.EndFeatherPx, length+o.EndFeatherPx-t), o.EndFeatherPx)
			if wAlong <= 0 {
				continue
			}
			wgt := float32(wAcross * wAlong)
			i := y*out.W + x
			for c := 0; c < out.C && c < im.C; c++ {
				e := im.Pix[c][i] - bg[c][i]
				if e <= 0 {
					continue
				}
				if v := e * wgt; v > out.Pix[c][i] {
					out.Pix[c][i] = v
				}
			}
		}
	}
}

// feather is a smooth 0..1 ramp: 0 at or below zero, 1 at or above width.
func feather(d, width float64) float64 {
	if width <= 0 {
		if d > 0 {
			return 1
		}
		return 0
	}
	t := d / width
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3 - 2*t)
}

// refPlanes is what gets subtracted from the frame: the clean stack when there is one, a tiled sky
// percentile otherwise.
func refPlanes(im, ref *fits.Image, o RenderOptions) [][]float32 {
	if ref != nil && ref.W == im.W && ref.H == im.H && ref.C >= im.C {
		return ref.Pix
	}
	return backgroundPlanes(im, o)
}

// backgroundPlanes estimates the sky under each channel, so what gets painted is the streak's own
// light rather than the sky it crossed.
func backgroundPlanes(im *fits.Image, o RenderOptions) [][]float32 {
	tile := o.TilePx
	if tile < 16 {
		tile = 16
	}
	out := make([][]float32, im.C)
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		out[c] = tileMap(p, im.W, im.H, tile, func(t []float32) float64 {
			return percentileOf(t, o.BgPercentile)
		})
	}
	return out
}

func percentileOf(t []float32, pct float64) float64 {
	if len(t) == 0 {
		return 0
	}
	c := make([]float32, len(t))
	copy(c, t)
	sort.Slice(c, func(a, b int) bool { return c[a] < c[b] })
	i := int(math.Min(math.Max(pct/100, 0), 1) * float64(len(c)-1))
	return float64(c[i])
}

// Blend adds a rendered layer over a base image and returns a new image. gain scales the layer, so a
// caller can dial a meteor back without re-rendering it.
func Blend(base, layer *fits.Image, gain float64) *fits.Image {
	out := fits.NewImage(base.W, base.H, base.C)
	for c := 0; c < base.C; c++ {
		copy(out.Pix[c], base.Pix[c])
	}
	if layer == nil || layer.W != base.W || layer.H != base.H {
		return out
	}
	g := float32(gain)
	for c := 0; c < out.C && c < layer.C; c++ {
		for i := range out.Pix[c] {
			out.Pix[c][i] += g * layer.Pix[c][i]
		}
	}
	return out
}
