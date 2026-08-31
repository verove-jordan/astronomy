package solar

import (
	"fmt"
	"math"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// profiles_test.go holds the three measurements the solar work is judged on, shared between the
// unit tests and the live bench so both are reading the same numbers.
//
// They answer three different questions and none of them substitutes for another:
//
//   - discContrast — how much fine detail survives, as a function of radius. This is what tells a
//     registration error apart from an optical one: a scale or rotation residual displaces a feature
//     by an amount proportional to its radius, so it is exactly zero at the centre and worst at the
//     limb, while a soft lens is soft everywhere.
//   - MeasurePSF (psf.go) — how wide the limb edge is, which is noise-independent and therefore the
//     only honest way to compare a single grainy frame against a clean stack.
//   - renderedRadial — what the FINISHED image looks like across the limb. The other two are measured
//     on the linear master and are blind to everything the finish does; a false bright ring is
//     invisible to both and is the single most obvious defect in the output.

// renderBins is how finely the finished image is profiled across the limb. The window is 0.2 R wide,
// so at a 900 px radius this is a bin every 1.1 px — fine enough to resolve a ring a few pixels wide,
// coarse enough that each bin still holds thousands of pixels to take a median over.
const renderBins = 160

// renderLo and renderHi bound that window, as fractions of the radius.
const renderLo, renderHi = 0.90, 1.10

// ringWindow is the span, in radius fractions, over which a rendered profile must fall monotonically.
// It brackets the physical limb transition — PSF plus chromosphere, ten pixels or so — with room
// either side for a blend to complete in.
var ringWindow = [2]float64{0.95, 1.05}

// discContrast measures fine detail as a function of distance from the disc centre.
//
// It is the band-pass FrameSharpness ranks frames on, binned by radius and divided by the local
// brightness, so that limb darkening — which halves the signal by 0.9 R all on its own — cannot
// masquerade as lost detail.
func discContrast(im *fits.Image, l Limb, bins int) []float64 {
	inner := imgops.GaussianBlur(im.Pix[0], im.W, im.H, float64(bandInner))
	outer := imgops.GaussianBlur(im.Pix[0], im.W, im.H, float64(bandOuter))
	sum := make([]float64, bins)
	n := make([]int, bins)
	lvl := make([][]float32, bins)
	for y := 0; y < im.H; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < im.W; x++ {
			dx := float64(x) - l.CX
			frac := math.Hypot(dx, dy) / l.R
			if frac >= 0.95 {
				continue
			}
			b := clampInt(int(frac/0.95*float64(bins)), 0, bins-1)
			i := y*im.W + x
			d := float64(inner[i] - outer[i])
			sum[b] += d * d
			n[b]++
			lvl[b] = append(lvl[b], im.Pix[0][i])
		}
	}
	out := make([]float64, bins)
	for i := range out {
		if n[i] == 0 {
			continue
		}
		med := float64(imgops.Percentile(imgops.Subsample(lvl[i], 100000), 50))
		if med <= 0 {
			continue
		}
		out[i] = math.Sqrt(sum[i]/float64(n[i])) / med
	}
	return out
}

// renderedRadial profiles the luminance of a FINISHED image by radius, across the limb.
//
// The median within each annulus, not the mean, for the same reason the limb-darkening model uses
// one: a prominence occupies a few degrees of a ring and would otherwise lift the whole bin, which is
// precisely the false positive this measurement must not produce.
func renderedRadial(img *fits.Image, l Limb) []float64 {
	buckets := make([][]float32, renderBins)
	lum := make([]float32, img.W*img.H)
	for i := range lum {
		switch img.C {
		case 1:
			lum[i] = img.Pix[0][i]
		default:
			lum[i] = 0.299*img.Pix[0][i] + 0.587*img.Pix[1][i] + 0.114*img.Pix[2][i]
		}
	}
	for y := 0; y < img.H; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < img.W; x++ {
			dx := float64(x) - l.CX
			frac := math.Hypot(dx, dy) / l.R
			if frac < renderLo || frac >= renderHi {
				continue
			}
			b := clampInt(int((frac-renderLo)/(renderHi-renderLo)*renderBins), 0, renderBins-1)
			buckets[b] = append(buckets[b], lum[y*img.W+x])
		}
	}
	out := make([]float64, renderBins)
	for i, vals := range buckets {
		if len(vals) == 0 {
			out[i] = math.NaN()
			continue
		}
		out[i] = float64(imgops.Percentile(imgops.Subsample(vals, 100000), 50))
	}
	return out
}

// radiusOfBin is the radius fraction a rendered-profile bin describes.
func radiusOfBin(i int) float64 {
	return renderLo + (float64(i)+0.5)/renderBins*(renderHi-renderLo)
}

// ringAmplitude is the acceptance test for a false limb ring, as one number.
//
// Across a real limb the rendered profile falls monotonically: the disc, then the transition, then
// sky. Every artefact this package has produced there — the prominence curve bleeding onto the disc,
// the limb-darkening gain read at the wrong radius, an unsharp overshoot on the limb step — shows up
// the same way, as brightness RISING again on the way out. So the measurement is the largest such
// rise: how far the profile climbs above the lowest point it had already reached.
//
// A monotone edge scores zero. Anything above the noise of the median is a ring, and the bin it
// peaks in says which artefact it is.
func ringAmplitude(prof []float64) (amp, atFrac float64) {
	lo := math.Inf(1)
	at := -1
	for i, v := range prof {
		if math.IsNaN(v) {
			continue
		}
		f := radiusOfBin(i)
		if f < ringWindow[0] {
			continue
		}
		if f > ringWindow[1] {
			break
		}
		if v < lo {
			lo = v
			continue
		}
		if v-lo > amp {
			amp, at = v-lo, i
		}
	}
	if at < 0 {
		return 0, 0
	}
	return amp, radiusOfBin(at)
}

// formatProfile renders a profile as an indented table, one line per bin, with a bar so the shape is
// readable without plotting it.
func formatProfile(prof []float64, label string, radius func(int) float64, step int) string {
	var b strings.Builder
	peak := 0.0
	for _, v := range prof {
		if !math.IsNaN(v) && v > peak {
			peak = v
		}
	}
	if peak <= 0 {
		peak = 1
	}
	fmt.Fprintf(&b, "\n  %s\n", label)
	for i := 0; i < len(prof); i += step {
		v := prof[i]
		if math.IsNaN(v) {
			continue
		}
		bar := clampInt(int(v/peak*40+0.5), 0, 40)
		fmt.Fprintf(&b, "    r=%.3fR  %.5f  %s\n", radius(i), v, strings.Repeat("#", bar))
	}
	return b.String()
}

// cropImage extracts a rectangle, clamped to the source, for 100% inspection crops.
func cropImage(im *fits.Image, cx, cy, half int) *fits.Image {
	x0, y0 := clampInt(cx-half, 0, im.W-1), clampInt(cy-half, 0, im.H-1)
	x1, y1 := clampInt(cx+half, 0, im.W-1), clampInt(cy+half, 0, im.H-1)
	w, h := x1-x0+1, y1-y0+1
	out := fits.NewImage(w, h, im.C)
	for c := 0; c < im.C; c++ {
		for y := 0; y < h; y++ {
			copy(out.Pix[c][y*w:(y+1)*w], im.Pix[c][(y0+y)*im.W+x0:(y0+y)*im.W+x0+w])
		}
	}
	return out
}

// promContrast measures fine detail in the OFF-LIMB material.
//
// It exists because every other measurement in this file is structurally blind to a derotation error.
// Rotating a disc about its own centre maps the disc onto itself: the limb lands exactly where it was,
// so MeasurePSF cannot see it, and discContrast stops at 0.95 R so it cannot either. A stack whose two
// clips are a degree and a half apart — twenty-five pixels at a 900 px limb — therefore scores
// identically to a correctly registered one on both, while the prominences come out doubled and the
// limb reads as a row of dark notches. That is exactly how it was missed.
//
// The prominences are the only structure a rotation displaces, so they are what this reads: the
// band-pass contrast over the brightest tenth of the off-limb annulus, normalised by its own height
// above the background so two stacks are comparable.
//
// "Brightest tenth" has to be judged against the MODELLED background rather than against a single sky
// level, and that distinction is the difference between measuring prominences and measuring nothing.
// The scattered-light skirt just outside the limb is far brighter than any prominence and covers the
// entire ring, so thresholding on raw brightness selects the innermost annulus — which is
// azimuthally uniform and therefore exactly as rotation-blind as the limb itself. Measured that way
// the metric moved by 2% between a correctly derotated stack and one with no derotation at all.
func promContrast(im *fits.Image, l Limb) float64 {
	inner := imgops.GaussianBlur(im.Pix[0], im.W, im.H, float64(bandInner))
	outer := imgops.GaussianBlur(im.Pix[0], im.W, im.H, float64(bandOuter))
	halo := offLimbProfile(im.Pix[0], im.W, im.H, l, nil)

	const lo, hi = 1.005, 1.10
	lo2, hi2 := (lo*l.R)*(lo*l.R), (hi*l.R)*(hi*l.R)
	type px struct {
		i   int
		res float64
	}
	var band []px
	var vals []float32
	for y := 0; y < im.H; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < im.W; x++ {
			dx := float64(x) - l.CX
			d2 := dx*dx + dy*dy
			if d2 < lo2 || d2 > hi2 {
				continue
			}
			i := y*im.W + x
			d := math.Sqrt(d2)
			r := float64(im.Pix[0][i]) - halo.at(d/l.R, math.Atan2(dy, dx))
			band = append(band, px{i, r})
			vals = append(vals, float32(r))
		}
	}
	if len(band) < 1000 {
		return 0
	}
	thr := float64(imgops.Percentile(imgops.Subsample(vals, 200000), 90))
	if thr <= 0 {
		return 0
	}
	var sum, level float64
	var n int
	for _, p := range band {
		if p.res < thr {
			continue
		}
		e := float64(inner[p.i] - outer[p.i])
		sum += e * e
		level += p.res
		n++
	}
	if n == 0 || level <= 0 {
		return 0
	}
	return math.Sqrt(sum/float64(n)) / (level / float64(n))
}
