package skypano

// render.go turns solved panels into one image.
//
// Every canvas pixel asks the sphere which direction it is, then asks each panel whether it saw that
// direction. That is inverse mapping, and it is what lets the output projection be anything at all —
// the panels never have to agree with the canvas about geometry, only about the sky.

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// Panel is a solved, stacked frame ready to be placed on the sky.
type Panel struct {
	Name string
	Cam  Camera
	Img  *fits.Image
	// Corr is the photometric correction MatchPhotometry fits. Its zero value is the identity, so a
	// panel that has never been matched renders as itself.
	Corr Correction
	// Valid is an optional per-pixel weight on the panel's OWN pixels, in [0,1], multiplying the edge
	// feather: 0 where the panel must not be used at all. nil means the whole panel is usable.
	//
	// It is how something in the way — see FindOccluders — is kept off the canvas. Excluding it here
	// rather than cropping the panel matters: a crop would also throw away the sky beside the object,
	// and on a panorama that sky is often the only coverage of that patch.
	Valid []float32
}

// RenderOptions tune the assembly.
type RenderOptions struct {
	// FeatherPx is how far in from a panel's edge its weight ramps up from zero. It is what turns a
	// hard panel boundary into a gradient the eye cannot find.
	FeatherPx float64
	// EdgeTrimPx discards a margin at each panel edge outright, before feathering — registration
	// leaves the outermost pixels thin and they are not worth blending in.
	EdgeTrimPx float64
	// SharpFromBestPx turns on two-band blending at this low-pass radius, in canvas pixels. Zero
	// leaves the plain weighted average.
	//
	// It exists because averaging overlapping panels averages their DISAGREEMENT. Each panel is fitted
	// to the catalogue independently and lands within about 2.3 px of it, so two panels place the same
	// star some 3 px apart — and the average then draws that star twice, which is what "the stars are
	// doubled in the overlaps" is. No amount of feathering helps: the feather controls how much of
	// each panel is used, not whether their stars agree.
	//
	// So the two bands are taken from different places. Everything below this radius — the sky's
	// level and colour, which is what a seam would show up in — comes from the weighted average, and
	// is as smooth as it always was. Everything above it — stars, dust lanes, all the detail — comes
	// from an average weighted by w^sharpWeightPower, which is so concentrated on the best-covered
	// panel that a star is effectively drawn once, by one panel.
	//
	// Concentrated rather than chosen outright: see sharpWeightPower.
	SharpFromBestPx float64
}

func DefaultRenderOptions() RenderOptions {
	// The trim is wide because a panel's outermost pixels are its stack's thin edge — the drift rim,
	// where few frames overlapped — and they arrive discoloured. There is field to spare: the panels
	// step about 10 degrees apart and each is 57 by 72, so 100 px (about 2 degrees) costs nothing
	// that a neighbour does not already cover.
	return RenderOptions{FeatherPx: 400, EdgeTrimPx: 100}
}

// PlanCanvas sizes a canvas that holds every panel, at the given scale.
func PlanCanvas(panels []Panel, proj Projection, fr Frame, scaleDegPerPix float64) (Canvas, error) {
	return PlanCanvasAt(panels, proj, fr, scaleDegPerPix, 0, 0)
}

// PlanCanvasAt is PlanCanvas for a canvas that needs to know where and when it is standing: the
// Horizon frame, which draws the sky as it stood over a site at one named instant. siteLatDeg and
// lstDeg are ignored by every other frame.
func PlanCanvasAt(panels []Panel, proj Projection, fr Frame, scaleDegPerPix, siteLatDeg, lstDeg float64) (Canvas, error) {
	if len(panels) == 0 || scaleDegPerPix <= 0 {
		return Canvas{}, fmt.Errorf("skypano: no panels to plan a canvas for")
	}
	// Centre on the mean viewing direction, so the projection's least-distorted region sits where
	// the data is.
	var sum [3]float64
	for _, p := range panels {
		a := p.Cam.Axis()
		for i := range sum {
			sum[i] += a[i]
		}
	}
	centre := normalize3(sum)
	var lon0, lat0 float64
	switch fr {
	case Galactic:
		lon0, lat0 = vecToLonLat(equatorialToGalactic(centre))
	case Horizon:
		// Which centre is right depends on the projection, and the two want opposite things.
		//
		// EQUIRECTANGULAR needs the middle of the azimuths, NOT the mean direction. An arch reaching
		// over the zenith has its mean direction AT the zenith, where azimuth is degenerate — the
		// average of pointings to the south-west and the north-east points straight up, and straight up
		// has no azimuth. Taking the centre from it gave an arbitrary Lon0 and the wrap in SkyToPix
		// folded the two ends of the arch onto each other: the real panels landed from -3143 to +2867
		// on a canvas that started at zero.
		//
		// STEREOGRAPHIC needs the mean DIRECTION, because there distance from the centre is radius. The
		// azimuth-arc midpoint is a bearing the data need not cover at all — on this session it is 125
		// degrees, between the two clusters and pointing at nothing — and centring there put the panels
		// 90 degrees out, asking for a 73941x87418 canvas. It has no pole to avoid, so the zenith is a
		// perfectly good centre.
		if proj == Stereographic {
			lon0, lat0 = equatorialToHorizon(centre, siteLatDeg, lstDeg)
		} else {
			lon0, lat0 = horizonCentre(panels, siteLatDeg, lstDeg)
		}
	default:
		lon0, lat0 = vecToLonLat(centre)
	}

	// Measure the extent with a provisional canvas big enough not to clip, then size to fit.
	probe := Canvas{Proj: proj, Fr: fr, W: 1 << 20, H: 1 << 20, Lon0: lon0, Lat0: lat0,
		ScaleDegPerPix: scaleDegPerPix, SiteLatDeg: siteLatDeg, LSTDeg: lstDeg}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, p := range panels {
		for _, c := range panelBoundary(p) {
			x, y, ok := probe.SkyToPix(c)
			if !ok {
				continue
			}
			minX, minY = math.Min(minX, x), math.Min(minY, y)
			maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
		}
	}
	if math.IsInf(minX, 1) {
		return Canvas{}, fmt.Errorf("skypano: no panel projects onto the canvas")
	}
	w := int(math.Ceil(maxX - minX))
	h := int(math.Ceil(maxY - minY))
	if w <= 0 || h <= 0 || w > 60000 || h > 60000 {
		return Canvas{}, fmt.Errorf("skypano: canvas %dx%d is out of range at %.4f deg/pix", w, h, scaleDegPerPix)
	}
	// Re-centre so the measured bounds sit symmetrically in the final canvas.
	out := probe
	out.W, out.H = w, h
	cx, cy := (minX+maxX)/2, (minY+maxY)/2
	shiftX := (cx - float64(probe.W)/2) * scaleDegPerPix
	shiftY := -(cy - float64(probe.H)/2) * scaleDegPerPix
	if proj == Equirectangular {
		out.Lon0, out.Lat0 = lon0+shiftX, lat0+shiftY
	} else {
		v := rotateFromCentre(lon0, lat0,
			math.Hypot(shiftX, shiftY)*math.Pi/180, math.Atan2(shiftX, shiftY))
		out.Lon0, out.Lat0 = vecToLonLat(v)
	}
	return out, nil
}

// panelBoundary samples a panel's edge as sky directions, densely enough that the curvature between
// samples cannot hide an excursion.
func panelBoundary(p Panel) [][3]float64 {
	const steps = 24
	w, h := float64(p.Img.W-1), float64(p.Img.H-1)
	var out [][3]float64
	for i := 0; i <= steps; i++ {
		t := float64(i) / steps
		out = append(out,
			p.Cam.Unproject(t*w, 0),
			p.Cam.Unproject(t*w, h),
			p.Cam.Unproject(0, t*h),
			p.Cam.Unproject(w, t*h),
		)
	}
	return out
}

// Render assembles the panels onto the canvas. It returns the image and the accumulated blend weight
// per pixel — the coverage map, which every downstream statistic needs: a mosaic is not a rectangle,
// and a percentile that includes the empty corners is measuring the shape of the mosaic rather than
// the sky.
func Render(panels []Panel, c Canvas, o RenderOptions) (*fits.Image, []float32, error) {
	if len(panels) == 0 {
		return nil, nil, fmt.Errorf("skypano: nothing to render")
	}
	ch := panels[0].Img.C
	out := fits.NewImage(c.W, c.H, ch)
	acc := make([][]float64, ch)
	for i := range acc {
		acc[i] = make([]float64, c.W*c.H)
	}
	wsum := make([]float64, c.W*c.H)

	// The detail source for two-band blending: the same panels averaged again, but with their weights
	// raised to a high power so the best-covered one takes almost all of it.
	//
	// A hard argmax was tried first and it drew seams. Panels differ in level by whatever
	// MatchPhotometry could not remove, so picking one outright makes the detail source STEP at the
	// selection boundary — and since that source is then high-passed, the step comes back as a
	// plus-or-minus-half-delta edge across every seam, sharper than the 400 px feather it replaced.
	// A high power keeps the source continuous (no step, no edge) while still concentrating it on one
	// panel everywhere except a thin band where two panels are within a few per cent of each other.
	var acs [][]float64
	var wps []float64
	if o.SharpFromBestPx > 0 {
		acs = make([][]float64, ch)
		for i := range acs {
			acs[i] = make([]float64, c.W*c.H)
		}
		wps = make([]float64, c.W*c.H)
	}
	sample := make([]float64, ch)

	for _, p := range panels {
		if p.Img.C != ch {
			return nil, nil, fmt.Errorf("skypano: panel %s has %d channels, expected %d", p.Name, p.Img.C, ch)
		}
		x0, y0, x1, y1 := panelCanvasBounds(p, c)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				v, ok := c.PixToSky(float64(x)+0.5, float64(y)+0.5)
				if !ok {
					continue
				}
				px, py, ok := p.Cam.Project(v)
				if !ok {
					continue
				}
				w := edgeWeight(px, py, p.Img.W, p.Img.H, o)
				if w <= 0 {
					continue
				}
				if len(p.Valid) == len(p.Img.Pix[0]) {
					w *= float64(imgops.SampleCubic(p.Valid, p.Img.W, p.Img.H, px, py))
					if w <= 0 {
						continue
					}
				}
				i := y*c.W + x
				pu, pv := panelUV(px, py, p.Img.W, p.Img.H)
				for k := 0; k < ch; k++ {
					raw := float64(imgops.SampleCubic(p.Img.Pix[k], p.Img.W, p.Img.H, px, py))
					sample[k] = p.Corr.Apply(k, raw, pu, pv)
					acc[k][i] += w * sample[k]
				}
				wsum[i] += w
				if wps != nil {
					wp := math.Pow(w, sharpWeightPower)
					wps[i] += wp
					for k := 0; k < ch; k++ {
						acs[k][i] += wp * sample[k]
					}
				}
			}
		}
	}
	weight := make([]float32, c.W*c.H)
	for i := range wsum {
		if wsum[i] <= 0 {
			continue
		}
		weight[i] = float32(wsum[i])
		for k := 0; k < ch; k++ {
			out.Pix[k][i] = float32(acc[k][i] / wsum[i])
		}
	}
	if wps != nil {
		sharp := make([][]float32, ch)
		for k := 0; k < ch; k++ {
			sharp[k] = make([]float32, c.W*c.H)
			for i := range sharp[k] {
				if wps[i] > 0 {
					sharp[k][i] = float32(acs[k][i] / wps[i])
				} else {
					sharp[k][i] = out.Pix[k][i] // no concentrated weight anywhere: keep the average
				}
			}
		}
		blendTwoBand(out, sharp, weight, int(math.Round(o.SharpFromBestPx)))
	}
	return out, weight, nil
}

// sharpWeightPower concentrates the detail band on the best-covered panel. High enough that a panel
// 10% behind the leader contributes about 3%, low enough to stay numerically ordinary.
const sharpWeightPower = 32

// blendTwoBand replaces the averaged canvas's detail with the best-covering panel's detail, keeping
// the average's smooth part.
//
//	out = lowpass(avg) + (best - lowpass(best))
//
// Both bands are low-passed with the SAME coverage-weighted kernel, so at the edge of the data — where
// the kernel is one-sided and the blur is least trustworthy — the two low-passes agree and cancel,
// leaving the average untouched rather than ringing.
func blendTwoBand(out *fits.Image, sharp [][]float32, weight []float32, radius int) {
	if radius < 1 || len(sharp) == 0 {
		return
	}
	covered := make([]float32, len(weight))
	for i, w := range weight {
		if w > 0 {
			covered[i] = 1
		}
	}
	for k := 0; k < out.C && k < len(sharp); k++ {
		loAvg := maskedBoxBlur(out.Pix[k], covered, out.W, out.H, radius)
		loSharp := maskedBoxBlur(sharp[k], covered, out.W, out.H, radius)
		p := out.Pix[k]
		for i := range p {
			if covered[i] == 0 {
				continue
			}
			p[i] = loAvg[i] + (sharp[k][i] - loSharp[i])
		}
	}
}

// maskedBoxBlur is a separable box blur that only ever averages covered pixels, so the uncovered
// surround cannot drag the edge of the data toward zero.
func maskedBoxBlur(p, covered []float32, w, h, radius int) []float32 {
	num := make([]float32, len(p))
	den := make([]float32, len(p))
	// Horizontal.
	for y := 0; y < h; y++ {
		var sv, sw float64
		row := y * w
		for x := 0; x <= radius && x < w; x++ {
			sv += float64(p[row+x] * covered[row+x])
			sw += float64(covered[row+x])
		}
		for x := 0; x < w; x++ {
			num[row+x], den[row+x] = float32(sv), float32(sw)
			if add := x + radius + 1; add < w {
				sv += float64(p[row+add] * covered[row+add])
				sw += float64(covered[row+add])
			}
			if drop := x - radius; drop >= 0 {
				sv -= float64(p[row+drop] * covered[row+drop])
				sw -= float64(covered[row+drop])
			}
		}
	}
	// Vertical, over the horizontal sums.
	outV := make([]float32, len(p))
	for x := 0; x < w; x++ {
		var sv, sw float64
		for y := 0; y <= radius && y < h; y++ {
			sv += float64(num[y*w+x])
			sw += float64(den[y*w+x])
		}
		for y := 0; y < h; y++ {
			if sw > 0 {
				outV[y*w+x] = float32(sv / sw)
			}
			if add := y + radius + 1; add < h {
				sv += float64(num[add*w+x])
				sw += float64(den[add*w+x])
			}
			if drop := y - radius; drop >= 0 {
				sv -= float64(num[drop*w+x])
				sw -= float64(den[drop*w+x])
			}
		}
	}
	return outV
}

// panelCanvasBounds is the canvas rectangle a panel can possibly touch, so the renderer does not walk
// the whole canvas once per panel.
func panelCanvasBounds(p Panel, c Canvas) (x0, y0, x1, y1 int) {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, v := range panelBoundary(p) {
		x, y, ok := c.SkyToPix(v)
		if !ok {
			continue
		}
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
	}
	if math.IsInf(minX, 1) {
		return 0, 0, -1, -1
	}
	return max(int(minX)-1, 0), max(int(minY)-1, 0), min(int(maxX)+1, c.W-1), min(int(maxY)+1, c.H-1)
}

// edgeWeight ramps a panel's contribution up from its edges, so neighbouring panels cross-fade.
func edgeWeight(x, y float64, w, h int, o RenderOptions) float64 {
	d := math.Min(math.Min(x, float64(w-1)-x), math.Min(y, float64(h-1)-y)) - o.EdgeTrimPx
	if d <= 0 {
		return 0
	}
	if o.FeatherPx <= 0 || d >= o.FeatherPx {
		return 1
	}
	t := d / o.FeatherPx
	return t * t * (3 - 2*t) // smoothstep
}

// samplePoints spreads sample positions evenly over the canvas.
func samplePoints(c Canvas, n int) [][2]float64 {
	if n <= 0 {
		return nil
	}
	step := math.Sqrt(float64(c.W) * float64(c.H) / float64(n))
	if step < 1 {
		step = 1
	}
	var out [][2]float64
	for y := step / 2; y < float64(c.H); y += step {
		for x := step / 2; x < float64(c.W); x += step {
			out = append(out, [2]float64{x, y})
		}
	}
	return out
}

// horizonCentre picks the middle of the arch: the midpoint of the SMALLEST arc of azimuth that holds
// every panel, and the middle of their altitudes.
//
// The smallest covering arc is found by looking for the largest GAP between neighbouring azimuths —
// whatever the panels do not cover is that gap, so the arc they do cover is its complement. This is
// what makes a session that straddles due north (say 350 degrees and 10 degrees) centre on north
// rather than on south, which averaging the numbers would do.
func horizonCentre(panels []Panel, siteLatDeg, lstDeg float64) (az0, alt0 float64) {
	az := make([]float64, 0, len(panels))
	minAlt, maxAlt := math.Inf(1), math.Inf(-1)
	for _, p := range panels {
		a, h := equatorialToHorizon(p.Cam.Axis(), siteLatDeg, lstDeg)
		az = append(az, a)
		minAlt, maxAlt = math.Min(minAlt, h), math.Max(maxAlt, h)
	}
	if len(az) == 0 {
		return 0, 0
	}
	alt0 = (minAlt + maxAlt) / 2
	if len(az) == 1 {
		return az[0], alt0
	}
	sorted := append([]float64(nil), az...)
	sort.Float64s(sorted)
	gap, at := sorted[0]+360-sorted[len(sorted)-1], len(sorted)-1
	for i := 1; i < len(sorted); i++ {
		if g := sorted[i] - sorted[i-1]; g > gap {
			gap, at = g, i-1
		}
	}
	// The covered arc runs from the azimuth after the gap round to the one before it.
	start := sorted[(at+1)%len(sorted)]
	az0 = math.Mod(start+(360-gap)/2+360, 360)
	return az0, alt0
}
