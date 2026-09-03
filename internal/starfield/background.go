package starfield

// background.go estimates the sky LOCALLY.
//
// A single median and MAD for the whole frame is fine over a flat field and badly wrong over a Milky
// Way, where the background varies by a factor of two across the picture. Two things break at once:
// the detection threshold is too low inside the bright band and too high outside it, and — worse —
// the flux measured for a star sitting on the band includes the band, so a mediocre star in bright
// nebulosity outranks a genuinely bright one in a dark corner. Ranking by that flux returns a map of
// the nebulosity instead of a list of stars, and nothing downstream can match it to a catalogue.

import (
	"math"
	"sort"
)

// bgCellPx is the target cell size for the background grid. Large enough that stars are a minority
// of each cell (so a median rejects them), small enough to follow real nebulosity.
const bgCellPx = 128

// bgCellSamples caps how many pixels of a cell are sorted for its median.
const bgCellSamples = 1024

// localBackground is a coarse grid of per-cell sky level and noise, bilinearly interpolated.
type localBackground struct {
	gw, gh int
	cell   float64
	bg     []float64
	noise  []float64
}

// newLocalBackground measures the sky on a grid. Each cell takes the median of a subsample as its
// level and the median absolute deviation as its noise, so stars — always a minority of a cell that
// size — do not lift either.
func newLocalBackground(plane []float32, w, h int) *localBackground {
	gw := max(1, w/bgCellPx)
	gh := max(1, h/bgCellPx)
	lb := &localBackground{gw: gw, gh: gh, bg: make([]float64, gw*gh), noise: make([]float64, gw*gh)}

	buf := make([]float64, 0, bgCellSamples)
	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			x0, x1 := gx*w/gw, (gx+1)*w/gw
			y0, y1 := gy*h/gh, (gy+1)*h/gh
			// Stride the cell so the sample is spread over it rather than taken from one corner.
			n := (x1 - x0) * (y1 - y0)
			stride := max(1, n/bgCellSamples)
			buf = buf[:0]
			for i := 0; i < n; i += stride {
				x, y := x0+i%(x1-x0), y0+i/(x1-x0)
				if y >= y1 {
					break
				}
				buf = append(buf, float64(plane[y*w+x]))
			}
			if len(buf) == 0 {
				continue
			}
			sort.Float64s(buf)
			med := buf[len(buf)/2]
			for i := range buf {
				buf[i] = math.Abs(buf[i] - med)
			}
			sort.Float64s(buf)
			lb.bg[gy*gw+gx] = med
			lb.noise[gy*gw+gx] = 1.4826 * buf[len(buf)/2]
		}
	}
	return lb
}

// sample returns the interpolated background and noise at a pixel.
func (lb *localBackground) sample(x, y float64, w, h int) (bg, noise float64) {
	// Grid nodes sit at cell centres, so convert to node coordinates and clamp at the edges.
	fx := x/float64(w)*float64(lb.gw) - 0.5
	fy := y/float64(h)*float64(lb.gh) - 0.5
	x0, y0 := int(math.Floor(fx)), int(math.Floor(fy))
	tx, ty := fx-float64(x0), fy-float64(y0)

	at := func(cx, cy int) (float64, float64) {
		cx = min(max(cx, 0), lb.gw-1)
		cy = min(max(cy, 0), lb.gh-1)
		return lb.bg[cy*lb.gw+cx], lb.noise[cy*lb.gw+cx]
	}
	b00, n00 := at(x0, y0)
	b10, n10 := at(x0+1, y0)
	b01, n01 := at(x0, y0+1)
	b11, n11 := at(x0+1, y0+1)
	lerp := func(a, b, t float64) float64 { return a + (b-a)*t }
	bg = lerp(lerp(b00, b10, tx), lerp(b01, b11, tx), ty)
	noise = lerp(lerp(n00, n10, tx), lerp(n01, n11, tx), ty)
	return bg, noise
}

// medianNoise is the typical noise across the frame, used only to reject a degenerate image.
func (lb *localBackground) medianNoise() float64 {
	v := append([]float64(nil), lb.noise...)
	sort.Float64s(v)
	if len(v) == 0 {
		return 0
	}
	return v[len(v)/2]
}
