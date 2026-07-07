package optics

import "math"

// donutMinDiameter is the minimum full-res equivalent diameter (px) for a hollow component to be
// classified as a dust "donut"; smaller hollow rings are noise, not shadows.
const donutMinDiameter = 20

// buildDefect converts a detection-scale component into a Defect with full-resolution geometry. `scale`
// is the linear detection->full-res factor: linear quantities (centroid, bbox, equivalent diameter)
// are multiplied by it and area by its square. Depth/MeanDepth are scale-invariant fractions.
func buildDefect(c *component, dev []float32, mask []bool, w, h int, scale float64) Defect {
	meanDev := 0.0
	if c.area > 0 {
		meanDev = c.sumDev / float64(c.area)
	}
	cxDet := float64(c.sumX) / float64(c.area)
	cyDet := float64(c.sumY) / float64(c.area)
	eqDiam := 2 * math.Sqrt(float64(c.area)/math.Pi) * scale
	donut := hollowness(c, mask, w, h) > 0.5 && eqDiam > donutMinDiameter

	return Defect{
		CX:        int(math.Round(cxDet * scale)),
		CY:        int(math.Round(cyDet * scale)),
		X0:        int(math.Round(float64(c.x0) * scale)),
		Y0:        int(math.Round(float64(c.y0) * scale)),
		X1:        int(math.Round(float64(c.x1) * scale)),
		Y1:        int(math.Round(float64(c.y1) * scale)),
		AreaPx:    int(math.Round(float64(c.area) * scale * scale)),
		Depth:     c.maxDev,
		MeanDepth: meanDev,
		Donut:     donut,
		Shape:     cropShape(c, dev, w),
	}
}

// hollowness returns 1 - (masked fraction of the central 25%-of-bbox region). A dust donut has an
// unmasked hole in the middle, so hollowness is high; a solid blob is masked through the center, so
// it is low. The central region spans the middle 25% of the bbox in each axis (half-extent = bbox/8),
// sized to sit inside a typical donut hole rather than straddle its ring wall.
func hollowness(c *component, mask []bool, w, h int) float64 {
	cx := (c.x0 + c.x1) / 2
	cy := (c.y0 + c.y1) / 2
	hw := max(1, (c.x1-c.x0+1)/8)
	hh := max(1, (c.y1-c.y0+1)/8)
	total, masked := 0, 0
	for yy := cy - hh; yy <= cy+hh; yy++ {
		if yy < 0 || yy >= h {
			continue
		}
		for xx := cx - hw; xx <= cx+hw; xx++ {
			if xx < 0 || xx >= w {
				continue
			}
			total++
			if mask[yy*w+xx] {
				masked++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return 1 - float64(masked)/float64(total)
}

// cropShape returns the normalized deviation (d/Depth clamped to [0,1]) over the component's
// DETECTION-scale bbox, row-major. This is the repair kernel; RepairFrames re-samples it onto the
// full-res bbox by nearest sampling.
func cropShape(c *component, dev []float32, w int) []float32 {
	bw := c.x1 - c.x0 + 1
	bh := c.y1 - c.y0 + 1
	if bw <= 0 || bh <= 0 {
		return nil
	}
	depth := float32(c.maxDev)
	shape := make([]float32, bw*bh)
	for yy := 0; yy < bh; yy++ {
		for xx := 0; xx < bw; xx++ {
			var v float32
			if depth > 0 {
				v = dev[(c.y0+yy)*w+(c.x0+xx)] / depth
			}
			shape[yy*bw+xx] = float32(clampF(float64(v), 0, 1))
		}
	}
	return shape
}

// defectScore is the integrated defect burden Sum(meanDepth*areaPx)/totalPx — a scale-invariant
// fraction (both areaPx and totalPx are full-res). Used only to grade FlatQC.Status.
func defectScore(defects []Defect, totalPx float64) float64 {
	if totalPx <= 0 {
		return 0
	}
	sum := 0.0
	for i := range defects {
		sum += defects[i].MeanDepth * float64(defects[i].AreaPx)
	}
	return sum / totalPx
}

// maxDepth returns the deepest single-defect Depth, or 0 for none.
func maxDepth(defects []Defect) float64 {
	m := 0.0
	for i := range defects {
		if defects[i].Depth > m {
			m = defects[i].Depth
		}
	}
	return m
}

// equivDiameter is the diameter (px) of a circle with the same area.
func equivDiameter(areaPx int) float64 {
	return 2 * math.Sqrt(float64(areaPx)/math.Pi)
}
