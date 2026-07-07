package optics

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// DetectDefects finds discrete optical defects in a mono detection plane. `fullScale` is the linear
// factor mapping a detection-plane pixel to full-resolution sensor pixels (the `scale` from
// LoadFlatPlane); all geometry in the returned Defects (CX/CY, X0..Y1, AreaPx, equivalent diameter)
// is multiplied by it so callers work in sensor coordinates. It also returns the raw deviation map
// d = 1 - plane/smooth at DETECTION scale (len w*h), which the artifact writer/tests can reuse.
//
// Pipeline: median-normalize the plane to ~1, subtract a Gaussian low-order model (lamp+vignette),
// threshold the fractional dip, dilate+label, then filter components by size and border contact.
func DetectDefects(plane []float32, w, h int, fullScale float64) ([]Defect, []float32) {
	n := w * h
	if n == 0 || len(plane) != n {
		return nil, nil
	}
	norm := medianNormalize(plane)
	sigma := math.Max(8, 0.015*float64(w))
	smooth := imgops.GaussianBlur(norm, w, h, sigma)

	dev := make([]float32, n)
	for i := range norm {
		if smooth[i] > 0.05 {
			dev[i] = 1 - norm[i]/smooth[i]
		}
	}

	mask := make([]bool, n)
	for i, d := range dev {
		if float64(d) > defectMinDepth {
			mask[i] = true
		}
	}
	mask = imgops.BinaryDilation(mask, w, h, 1)
	labels, ncomp := imgops.Label(mask, w, h)

	comps := collectComponents(labels, ncomp, dev, w, h)
	maxArea := defectMaxAreaFrac * float64(w) * float64(h)
	var out []Defect
	for i := range comps {
		c := &comps[i]
		if c.area < defectMinAreaPx || float64(c.area) > maxArea {
			continue
		}
		if bordersTouched(c, w, h) >= 2 {
			continue
		}
		out = append(out, buildDefect(c, dev, mask, w, h, fullScale))
	}
	return out, dev
}

// medianNormalize divides the plane by its median so the low-order model sits near 1. Guards a
// non-positive median by returning a copy.
func medianNormalize(plane []float32) []float32 {
	med := imgops.Percentile(imgops.Subsample(plane, 200000), 50)
	out := make([]float32, len(plane))
	if med <= 0 {
		copy(out, plane)
		return out
	}
	inv := float32(1.0 / med)
	for i, v := range plane {
		out[i] = v * inv
	}
	return out
}

// component accumulates one connected region's statistics in DETECTION coordinates.
type component struct {
	area           int
	sumX, sumY     int
	x0, y0, x1, y1 int
	maxDev         float64
	sumDev         float64
}

// collectComponents walks the label grid once, accumulating per-component area, centroid, bbox and
// deviation statistics.
func collectComponents(labels []int, ncomp int, dev []float32, w, h int) []component {
	comps := make([]component, ncomp)
	for i := range comps {
		comps[i] = component{x0: w, y0: h, x1: -1, y1: -1}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			lbl := labels[y*w+x]
			if lbl == 0 {
				continue
			}
			c := &comps[lbl-1]
			c.area++
			c.sumX += x
			c.sumY += y
			c.x0, c.y0 = min(c.x0, x), min(c.y0, y)
			c.x1, c.y1 = max(c.x1, x), max(c.y1, y)
			d := float64(dev[y*w+x])
			c.sumDev += d
			if d > c.maxDev {
				c.maxDev = d
			}
		}
	}
	return comps
}

// bordersTouched counts how many of the four plane edges the component's bbox touches; a component
// touching >=2 is treated as vignetting residue rather than a discrete defect.
func bordersTouched(c *component, w, h int) int {
	n := 0
	if c.x0 == 0 {
		n++
	}
	if c.y0 == 0 {
		n++
	}
	if c.x1 == w-1 {
		n++
	}
	if c.y1 == h-1 {
		n++
	}
	return n
}
