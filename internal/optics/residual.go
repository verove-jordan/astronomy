package optics

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// residualActionable is the minimum |Rel| for a consistent residual to be worth repairing/flagging.
const residualActionable = 0.015

// Residual is a per-defect measurement of how much of a defect survives into the calibrated light
// frames. Rel is the relative dip still present (1 - core/annulus); Consistent is true when the
// per-frame measurements agree, i.e. the residual is a real optical artifact rather than noise.
type Residual struct {
	Defect     int     `json:"defect"`
	Rel        float64 `json:"rel"`
	Consistent bool    `json:"consistent"`
}

// MeasureResiduals samples up to five evenly-spaced light frames and measures each defect's residual
// dip. For every defect it compares the median of the deep core (Shape>0.5) against the median of the
// surrounding annulus (between 1.2x and 2x the bbox). It returns one Residual per defect (index in
// Defect). Soft-fails: unreadable frames are skipped; with no readable frames it returns zero
// residuals and an error.
func MeasureResiduals(defects []Defect, framePaths []string) ([]Residual, error) {
	out := make([]Residual, len(defects))
	for i := range out {
		out[i].Defect = i
	}
	frames := pickEven(framePaths, 5)
	if len(frames) == 0 {
		return out, fmt.Errorf("optics: no frames to measure residuals")
	}

	perDefect := make([][]float64, len(defects))
	read := 0
	for _, fp := range frames {
		im, err := fits.ReadImage(fp)
		if err != nil {
			continue
		}
		read++
		plane := monoView(im)
		for i := range defects {
			if rel, ok := measureOne(&defects[i], plane, im.W, im.H); ok {
				perDefect[i] = append(perDefect[i], rel)
			}
		}
	}
	if read == 0 {
		return out, fmt.Errorf("optics: no readable frames among %d", len(frames))
	}
	for i := range defects {
		rels := perDefect[i]
		if len(rels) == 0 {
			continue
		}
		med := medianF(rels)
		out[i].Rel = med
		out[i].Consistent = madF(rels) < math.Max(0.005, math.Abs(med)/2)
	}
	return out, nil
}

// measureOne returns the residual dip (1 - core/annulus) for one defect on one mono frame, or ok=false
// when the geometry falls outside the frame or a region is empty.
func measureOne(d *Defect, plane []float32, w, h int) (rel float64, ok bool) {
	sw, sh := shapeDims(d)
	fw, fh := d.X1-d.X0+1, d.Y1-d.Y0+1
	if sw == 0 || fw <= 0 || fh <= 0 {
		return 0, false
	}
	var core []float32
	for ry := 0; ry < fh; ry++ {
		fy := d.Y0 + ry
		if fy < 0 || fy >= h {
			continue
		}
		for rx := 0; rx < fw; rx++ {
			fx := d.X0 + rx
			if fx < 0 || fx >= w {
				continue
			}
			if sampleShape(d, sw, sh, rx, ry, fw, fh) > 0.5 {
				core = append(core, plane[fy*w+fx])
			}
		}
	}
	ann := annulusValues(d, plane, w, h)
	if len(core) == 0 || len(ann) == 0 {
		return 0, false
	}
	a := medianF32(ann)
	if a <= 0 {
		return 0, false
	}
	return 1 - float64(medianF32(core))/float64(a), true
}

// annulusValues collects frame pixels in the ring between 1.2x and 2x the bbox (about its center),
// excluding the bbox itself.
func annulusValues(d *Defect, plane []float32, w, h int) []float32 {
	cx := float64(d.X0+d.X1) / 2
	cy := float64(d.Y0+d.Y1) / 2
	fw := float64(d.X1 - d.X0 + 1)
	fh := float64(d.Y1 - d.Y0 + 1)
	ox0, oy0 := int(cx-fw), int(cy-fh)
	ox1, oy1 := int(cx+fw), int(cy+fh)
	var vals []float32
	for fy := oy0; fy <= oy1; fy++ {
		if fy < 0 || fy >= h {
			continue
		}
		for fx := ox0; fx <= ox1; fx++ {
			if fx < 0 || fx >= w {
				continue
			}
			inInner := math.Abs(float64(fx)-cx) <= 0.6*fw && math.Abs(float64(fy)-cy) <= 0.6*fh
			inBox := fx >= d.X0 && fx <= d.X1 && fy >= d.Y0 && fy <= d.Y1
			if inInner || inBox {
				continue
			}
			vals = append(vals, plane[fy*w+fx])
		}
	}
	return vals
}

// shapeDims recovers the detection-scale (width,height) of a defect's Shape kernel from its length and
// the full-res bbox aspect ratio. Because both bbox dimensions scale by the same factor, the recovered
// dims multiply back to len(Shape); a divisor search fixes any rounding.
func shapeDims(d *Defect) (sw, sh int) {
	l := len(d.Shape)
	if l == 0 {
		return 0, 0
	}
	fw := math.Max(1, float64(d.X1-d.X0+1))
	fh := math.Max(1, float64(d.Y1-d.Y0+1))
	sh = int(math.Round(math.Sqrt(float64(l) * fh / fw)))
	if sh < 1 {
		sh = 1
	}
	for sh > 1 && l%sh != 0 {
		sh--
	}
	sw = l / sh
	if sw*sh != l {
		return l, 1
	}
	return sw, sh
}

// sampleShape nearest-samples the Shape kernel at full-res bbox offset (rx,ry).
func sampleShape(d *Defect, sw, sh, rx, ry, fw, fh int) float32 {
	sx, sy := 0, 0
	if fw > 1 {
		sx = int(math.Round(float64(rx) * float64(sw-1) / float64(fw-1)))
	}
	if fh > 1 {
		sy = int(math.Round(float64(ry) * float64(sh-1) / float64(fh-1)))
	}
	return d.Shape[clampInt(sy, 0, sh-1)*sw+clampInt(sx, 0, sw-1)]
}

// monoView returns a mono view of an image: channel 0 for mono, else the per-pixel channel mean.
func monoView(im *fits.Image) []float32 {
	if im.C == 1 {
		return im.Pix[0]
	}
	return channelMean(im)
}

// pickEven returns up to n evenly-spaced elements of paths (including first and last).
func pickEven(paths []string, n int) []string {
	if len(paths) <= n {
		return paths
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, paths[i*(len(paths)-1)/(n-1)])
	}
	return out
}

func medianF(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func medianF32(v []float32) float32 {
	s := append([]float32(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// madF is the median absolute deviation from the median.
func madF(v []float64) float64 {
	med := medianF(v)
	dev := make([]float64, len(v))
	for i, x := range v {
		dev[i] = math.Abs(x - med)
	}
	return medianF(dev)
}
