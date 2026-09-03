package meteor

// layer.go builds the image that gets blended back into the picture.
//
// This is deliberately a FILTER, not a detector. The transient layer already holds the meteors — the
// clip put them there — so the job is to remove what else it holds rather than to find them again.
// That matters because a blend does not need every meteor identified and named; it needs a layer
// that adds meteors and adds nothing else. Anything uncertain can simply be dropped, and the cost is
// a meteor missed rather than junk painted over the sky.
//
// Three things contaminate the layer, and each has a physical signature rather than a brightness:
//
//   - Star residue. Registration never cancels a star exactly — the frames drift and interpolate
//     differently — so every star leaves a speck, and those specks are BRIGHTER than the meteors.
//     They sit at star positions, which the clean stack knows.
//   - The drift rim. Where only part of the sequence reached, the edge is a bright straight border
//     the length of the frame. Never a meteor, always the strongest thing in the picture.
//   - Hot pixels and anything else that recurs. A meteor crosses once; a pixel rejected in several
//     frames is a property of the sensor.

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// LayerOptions tune the cleaning.
type LayerOptions struct {
	// MarginPx blanks the frame border, where the drift rim lives.
	MarginPx int
	// StarRadiusPx blanks a disc around each star found in the reference. Small on purpose: it only
	// has to cover the residue, and a generous radius eats the meteor crossing it.
	StarRadiusPx int
	// MaxRejectFrames drops pixels rejected in more than this many frames — a meteor is rejected in
	// exactly one.
	MaxRejectFrames int
	// NoiseQuantile and Margin set the brightness a pixel needs to be considered at all.
	NoiseQuantile, Margin float64
	// MinLengthPx and MinAspect keep only components long and thin enough to have flown.
	MinLengthPx, MinAspect float64
	// DilateIters closes the beading along a streak before components are measured.
	DilateIters int
}

func DefaultLayerOptions() LayerOptions {
	return LayerOptions{
		MarginPx: 160, StarRadiusPx: 5, MaxRejectFrames: 1,
		NoiseQuantile: 0.9, Margin: 12,
		MinLengthPx: 45, MinAspect: 4, DilateIters: 2,
	}
}

// Stars is the minimum this package needs to know about the star field: where they are.
type Stars struct{ X, Y []float64 }

// BuildLayer returns the blendable image and the mask of pixels it kept.
//
// tran is the transient image (the rejecting frame's own values, so a meteor keeps the brightness it
// actually had). l carries the per-pixel excess, frame and count.
func BuildLayer(tran *fits.Image, l Layer, stars Stars, o LayerOptions) (*fits.Image, []bool) {
	out := fits.NewImage(l.W, l.H, tran.C)
	if len(l.Excess) != l.W*l.H {
		return out, make([]bool, l.W*l.H)
	}
	usable := cleanMask(l, stars, o)

	cut := float32(quantileOfPositive(l.Excess, o.NoiseQuantile) * o.Margin)
	seed := make([]bool, l.W*l.H)
	for i := range seed {
		seed[i] = usable[i] && l.Excess[i] > cut
	}
	if o.DilateIters > 0 {
		grown := imgops.BinaryDilation(seed, l.W, l.H, o.DilateIters)
		for i := range grown {
			seed[i] = grown[i] && usable[i] // dilation must not reach back into a star or the rim
		}
	}

	labels, n := imgops.Label(seed, l.W, l.H)
	if n == 0 {
		return out, make([]bool, l.W*l.H)
	}
	groups := make([][]int, n+1)
	for i, lab := range labels {
		if lab > 0 {
			groups[lab] = append(groups[lab], i)
		}
	}
	keep := make([]bool, l.W*l.H)
	for _, idx := range groups[1:] {
		if len(idx) < 8 {
			continue
		}
		length, aspect := extent(idx, l.W)
		if length < o.MinLengthPx || aspect < o.MinAspect {
			continue
		}
		for _, i := range idx {
			keep[i] = true
			for ch := 0; ch < out.C && ch < tran.C; ch++ {
				out.Pix[ch][i] = tran.Pix[ch][i]
			}
		}
	}
	return out, keep
}

// cleanMask marks the pixels that are allowed to contribute at all.
func cleanMask(l Layer, stars Stars, o LayerOptions) []bool {
	ok := make([]bool, l.W*l.H)
	for y := o.MarginPx; y < l.H-o.MarginPx; y++ {
		for x := o.MarginPx; x < l.W-o.MarginPx; x++ {
			i := y*l.W + x
			if l.Count[i] >= 1 && int(l.Count[i]) <= o.MaxRejectFrames {
				ok[i] = true
			}
		}
	}
	r := o.StarRadiusPx
	for k := range stars.X {
		cx, cy := int(stars.X[k]), int(stars.Y[k])
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				if dx*dx+dy*dy > r*r {
					continue
				}
				x, y := cx+dx, cy+dy
				if x >= 0 && y >= 0 && x < l.W && y < l.H {
					ok[y*l.W+x] = false
				}
			}
		}
	}
	return ok
}

// extent measures a component's length along its major axis and its length-to-width ratio.
func extent(idx []int, w int) (length, aspect float64) {
	var sx, sy float64
	for _, i := range idx {
		sx += float64(i % w)
		sy += float64(i / w)
	}
	n := float64(len(idx))
	cx, cy := sx/n, sy/n
	var sxx, syy, sxy float64
	for _, i := range idx {
		dx, dy := float64(i%w)-cx, float64(i/w)-cy
		sxx += dx * dx
		syy += dy * dy
		sxy += dx * dy
	}
	sxx, syy, sxy = sxx/n, syy/n, sxy/n
	theta := 0.5 * math.Atan2(2*sxy, sxx-syy)
	ux, uy := math.Cos(theta), math.Sin(theta)
	minT, maxT, minP, maxP := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, i := range idx {
		dx, dy := float64(i%w)-cx, float64(i/w)-cy
		t, p := dx*ux+dy*uy, -dx*uy+dy*ux
		minT, maxT = math.Min(minT, t), math.Max(maxT, t)
		minP, maxP = math.Min(minP, p), math.Max(maxP, p)
	}
	length = maxT - minT
	width := math.Max(maxP-minP, 1)
	return length, length / width
}

// quantileOfPositive is the q-quantile of the non-zero values — most of a transient layer is exactly
// zero, and including that would put any quantile at zero.
func quantileOfPositive(v []float32, q float64) float64 {
	var nz []float32
	for _, e := range v {
		if e > 0 {
			nz = append(nz, e)
		}
	}
	if len(nz) < 64 {
		return 0
	}
	sortFloat32(nz)
	return float64(nz[int(math.Min(math.Max(q, 0), 1)*float64(len(nz)-1))])
}

func sortFloat32(v []float32) {
	sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
}
