package skypano

// grade.go turns the flattened linear canvas into something to look at.
//
// It is a separate, small stretch rather than a call into nightscape's grade for one structural
// reason: every statistic here has to be taken over COVERED pixels only. A canvas is 15 to 50 per
// cent nothing — a mosaic is not a rectangle — and a percentile over the whole array is mostly
// measuring the empty half of it. Everything else is the usual asinh recipe.
//
// The sky background is placed by solving for the scale that puts it there, not by hunting for a
// percentile that happens to look right. Given a measured sky level s, the curve
// asinh(beta*k*v)/asinh(beta) is inverted for k, so "the sky should sit at 0.09" is a request the
// grade satisfies exactly, on any data.

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// GradeOptions tune the stretch.
type GradeOptions struct {
	// MinCoverage is what counts as a real pixel, in units of ONE PANEL AT FULL WEIGHT — the units
	// Render's weight map is already in. Absolute rather than relative to the typical pixel: panels
	// pile up about three deep over most of this canvas, so a relative cut would throw away every
	// part of the sky only one panel reached, which is exactly the field the edges add.
	MinCoverage float64
	// BandMaskLatDeg is the galactic latitude inside which pixels are BAND, not background. The sky's
	// own colour is measured outside it: anchoring on the median of everything measures the Milky Way
	// and then neutralises IT to grey, which turns the rest of the frame the band's complement.
	BandMaskLatDeg float64
	// NeutralPct is the percentile of the band-free sky taken as its background level, per channel.
	// Subtracting those levels removes the colour cast the way it arrived — airglow and light
	// pollution ADD light, so an offset removes them and a gain would not.
	NeutralPct float64
	// BlackPct is the black point after neutralising, as a percentile of the BAND-FREE sky and COMMON
	// to all three channels — the colour has already been dealt with, and a per-channel cut here would
	// re-introduce a cast wherever the channels' distributions differ in shape.
	BlackPct float64
	// SkyPct is the percentile taken as "the sky background" — the level TargetBg refers to. Measured
	// outside the band, because on a canvas lying along the galactic plane the median of everything is
	// the Milky Way, and pinning THAT to TargetBg renders the whole picture dark.
	SkyPct float64
	// TargetBg is where that sky lands in the output, as an sRGB display value in 0..1.
	TargetBg float64
	// Exclude marks pixels that are REAL but must not be measured — the landscape under an arch, which
	// is genuinely part of the picture and would drag the sky's black point down if it were sampled.
	//
	// It exists because coverage cannot answer both questions. Zeroing a pixel's coverage does keep it
	// out of the statistics, and it also declares the pixel unreal, so the exporter paints it black:
	// the foreground was composited correctly and then blacked out on the way to the PNG.
	Exclude []bool
	// Intensity is the asinh beta: how hard the faint end is lifted relative to the bright end.
	Intensity float64
	// Saturation scales each pixel away from its luminance. 1 leaves colour alone.
	Saturation float64
	// HighlightPct sets the white point, as a percentile of covered pixels over all channels.
	HighlightPct float64
	// Floor lifts the whole picture onto a pedestal at the very end, as display-referred linear light:
	// out = Floor + (1-Floor)*out, so black becomes Floor and white stays white.
	//
	// A real night sky is never black. Every other control here only ever takes light AWAY — the black
	// point subtracts, the stretch divides — so the darkest sky lands wherever the subtraction left it,
	// and on a flattened canvas that is a couple of values off zero. Measured against a reference frame
	// the photographer was happy with: its darkest sky sat at 20/255 while ours sat at 7, and no
	// combination of black point and target background could close that, because they scale the picture
	// rather than raise its floor. This does exactly one thing, and it is the thing that was missing.
	Floor float64
}

func DefaultGradeOptions() GradeOptions {
	return GradeOptions{
		MinCoverage: 0.5, BandMaskLatDeg: 20, NeutralPct: 40, BlackPct: 20, SkyPct: 50,
		TargetBg: 0.12, Intensity: 8, Saturation: 1.4, HighlightPct: 99.9,
	}
}

// Grade stretches im in place and returns the mask of pixels that carry real data. The result is
// display-referred LINEAR light in 0..1 — the sRGB encoding belongs to whatever writes the file, so
// TargetBg is converted through the same encoding to stay a promise about what will be seen.
func Grade(im *fits.Image, cov []float32, c Canvas, o GradeOptions) []bool {
	keep := make([]bool, im.W*im.H)
	var covered, bandFree []int
	for i, v := range cov {
		if v < float32(o.MinCoverage) {
			continue
		}
		keep[i] = true
		if len(o.Exclude) == len(cov) && o.Exclude[i] {
			continue // real, and kept, but never measured
		}
		covered = append(covered, i)
		if o.BandMaskLatDeg <= 0 {
			bandFree = append(bandFree, i)
			continue
		}
		if sky, ok := c.PixToSky(float64(i%im.W)+0.5, float64(i/im.W)+0.5); ok {
			if _, b := vecToLonLat(equatorialToGalactic(sky)); math.Abs(b) >= o.BandMaskLatDeg {
				bandFree = append(bandFree, i)
			}
		}
	}
	if len(covered) == 0 {
		return keep
	}
	if len(bandFree) < len(covered)/50 {
		bandFree = covered // too little sky outside the band to measure it there
	}

	// Level the channels on the band-free sky, so what is left of the colour belongs to the sky
	// itself rather than to the air it was shot through.
	if im.C == 3 {
		var lvl [3]float64
		for ch := 0; ch < 3; ch++ {
			lvl[ch] = coveredPercentile(im.Pix[ch], bandFree, o.NeutralPct)
		}
		floor := math.Min(lvl[0], math.Min(lvl[1], lvl[2]))
		for ch := 0; ch < 3; ch++ {
			d := float32(lvl[ch] - floor)
			p := im.Pix[ch]
			for i := range p {
				if p[i] -= d; p[i] < 0 {
					p[i] = 0
				}
			}
		}
	}

	// The black point and the sky level are measured OUTSIDE the band, for the same reason the colour
	// is. On a canvas that lies along the galactic plane the median of everything covered IS the Milky
	// Way, so taking the sky level there pins the BAND to TargetBg and the picture renders dark —
	// measured on a two-panel canvas centred at galactic latitude -6 and -9, whose linear data was
	// perfectly normal (p50 0.0125 against a good canvas's 0.0211) and whose render came out nearly
	// black. The white point below is different and stays on everything covered: the brightest thing
	// in the frame really is in the band, and that is what white should be set by.
	black := float32(coveredPercentileAll(im, bandFree, o.BlackPct))
	for ch := 0; ch < im.C; ch++ {
		p := im.Pix[ch]
		for i := range p {
			if p[i] -= black; p[i] < 0 {
				p[i] = 0
			}
		}
	}

	// One white point across all channels, so the band's colour survives being normalised.
	hi := coveredPercentileAll(im, covered, o.HighlightPct)
	if hi <= 0 {
		return keep
	}
	for ch := 0; ch < im.C; ch++ {
		p := im.Pix[ch]
		for i := range p {
			p[i] /= float32(hi)
		}
	}

	sky := coveredPercentileAll(im, bandFree, o.SkyPct)
	beta := math.Max(o.Intensity, 1.0001)
	den := math.Asinh(beta)
	// Solve asinh(beta*k*sky)/asinh(beta) == srgbDecode(TargetBg) for k.
	k := 1.0
	if sky > 0 && o.TargetBg > 0 {
		k = math.Sinh(srgbDecode(o.TargetBg)*den) / (beta * sky)
	}
	for ch := 0; ch < im.C; ch++ {
		p := im.Pix[ch]
		for i := range p {
			v := math.Asinh(float64(p[i])*beta*k) / den
			p[i] = float32(math.Min(math.Max(v, 0), 1))
		}
	}

	if im.C == 3 && o.Saturation != 1 {
		f := float32(o.Saturation)
		r, g, b := im.Pix[0], im.Pix[1], im.Pix[2]
		for i := range r {
			l := 0.2126*r[i] + 0.7152*g[i] + 0.0722*b[i]
			for _, p := range [][]float32{r, g, b} {
				p[i] = clamp01(l + f*(p[i]-l))
			}
		}
	}

	// The pedestal goes on last, after the colour work, so it lifts the finished picture rather than
	// becoming something saturation can push around.
	if o.Floor > 0 {
		fl := float32(math.Min(o.Floor, 0.5))
		for ch := 0; ch < im.C; ch++ {
			p := im.Pix[ch]
			for i := range p {
				p[i] = fl + (1-fl)*p[i]
			}
		}
	}
	return keep
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// srgbDecode maps a display value to linear light, so a target expressed the way a viewer sees it
// can be aimed at in the space the stretch works in.
func srgbDecode(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// SRGBEncode is the inverse, for whatever writes the display file.
func SRGBEncode(v float64) float64 {
	if v <= 0.0031308 {
		return v * 12.92
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

func coveredPercentile(p []float32, covered []int, pct float64) float64 {
	if len(covered) == 0 {
		return 0
	}
	buf := make([]float32, len(covered))
	for i, idx := range covered {
		buf[i] = p[idx]
	}
	sort.Slice(buf, func(a, b int) bool { return buf[a] < buf[b] })
	return float64(buf[int(math.Min(math.Max(pct, 0), 100)/100*float64(len(buf)-1))])
}

func coveredPercentileAll(im *fits.Image, covered []int, pct float64) float64 {
	buf := make([]float32, 0, len(covered)*im.C)
	for ch := 0; ch < im.C; ch++ {
		for _, idx := range covered {
			buf = append(buf, im.Pix[ch][idx])
		}
	}
	if len(buf) == 0 {
		return 0
	}
	sort.Slice(buf, func(a, b int) bool { return buf[a] < buf[b] })
	return float64(buf[int(math.Min(math.Max(pct, 0), 100)/100*float64(len(buf)-1))])
}
