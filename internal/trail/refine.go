package trail

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// wpt is a full-resolution swath sample: position and its background-subtracted flux weight.
type wpt struct{ x, y, wgt float64 }

// refineSegment upgrades the pooled peak line to a full-resolution Segment: a flux-weighted least-
// squares (principal-axis) line fit, a Gaussian width from the perpendicular flux profile, and the
// masked extent with endpoint extension. It never fails — on degenerate weights it keeps the pooled
// line and a minimum width.
func refineSegment(plane []float32, w, h, f, gw int, inliers []int, cosT, sinT, rhoVal float64) Segment {
	seg := Segment{Nx: cosT, Ny: sinT, C: rhoVal * float64(f), Width: 2}
	localMed := fullMedian(plane)
	pts := gatherSwath(plane, w, h, seg, inliers, gw, f, localMed)
	if len(pts) >= 8 {
		if nx, ny, c, ok := fitLine(pts); ok {
			seg.Nx, seg.Ny, seg.C = nx, ny, c
		}
		seg.Width = profileWidth(pts, seg)
	}
	setExtent(&seg, inliers, gw, f, w, h)
	return seg
}

// gatherSwath collects full-res pixels within ⊥±8f of the (pooled) line and inside the inlier extent
// ±5%, weighted by max(0, v−localMed). It scans only the axis-aligned bounding box of that band.
func gatherSwath(plane []float32, w, h int, seg Segment, inliers []int, gw, f int, localMed float64) []wpt {
	dirx, diry := seg.dirVec()
	tmin, tmax := math.Inf(1), math.Inf(-1)
	xmin, xmax, ymin, ymax := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	half := float64(f) / 2
	for _, i := range inliers {
		fx, fy := float64((i%gw)*f)+half, float64((i/gw)*f)+half
		t := fx*dirx + fy*diry
		tmin, tmax = math.Min(tmin, t), math.Max(tmax, t)
		xmin, xmax = math.Min(xmin, fx), math.Max(xmax, fx)
		ymin, ymax = math.Min(ymin, fy), math.Max(ymax, fy)
	}
	band := 8.0 * float64(f)
	tlo, thi := tmin-0.05*(tmax-tmin), tmax+0.05*(tmax-tmin)
	x0, x1 := clampi(int(xmin-band), 0, w-1), clampi(int(xmax+band), 0, w-1)
	y0, y1 := clampi(int(ymin-band), 0, h-1), clampi(int(ymax+band), 0, h-1)
	var pts []wpt
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			fx, fy := float64(x), float64(y)
			if math.Abs(fx*seg.Nx+fy*seg.Ny-seg.C) > band {
				continue
			}
			if t := fx*dirx + fy*diry; t < tlo || t > thi {
				continue
			}
			if wv := float64(plane[y*w+x]) - localMed; wv > 0 {
				pts = append(pts, wpt{fx, fy, wv})
			}
		}
	}
	return pts
}

// fitLine returns the flux-weighted principal-axis line of pts as unit normal (nx,ny) and offset c
// (c = n·centroid). ok is false when the total weight is zero.
func fitLine(pts []wpt) (nx, ny, c float64, ok bool) {
	var sw, sx, sy float64
	for _, p := range pts {
		sw += p.wgt
		sx += p.wgt * p.x
		sy += p.wgt * p.y
	}
	if sw <= 0 {
		return 0, 0, 0, false
	}
	cx, cy := sx/sw, sy/sw
	var sxx, syy, sxy float64
	for _, p := range pts {
		dx, dy := p.x-cx, p.y-cy
		sxx += p.wgt * dx * dx
		syy += p.wgt * dy * dy
		sxy += p.wgt * dx * dy
	}
	phi := 0.5 * math.Atan2(2*sxy, sxx-syy) // major-axis angle of the covariance
	nx, ny = -math.Sin(phi), math.Cos(phi)
	return nx, ny, nx*cx + ny*cy, true
}

// profileWidth returns the Gaussian FWHM (2.355·σ⊥) of the flux-weighted perpendicular profile, at
// least 2 px.
func profileWidth(pts []wpt, seg Segment) float64 {
	var sw, sd, sd2 float64
	for _, p := range pts {
		d := seg.perpDist(p.x, p.y)
		sw += p.wgt
		sd += p.wgt * d
		sd2 += p.wgt * d * d
	}
	if sw <= 0 {
		return 2
	}
	mean := sd / sw
	varr := sd2/sw - mean*mean
	if varr <= 0 {
		return 2
	}
	return math.Max(2, 2.355*math.Sqrt(varr))
}

// setExtent projects the inliers onto the line to get [tmin,tmax], then sets [T0,T1]: extended to the
// image borders when the streak already spans ≥60% of the min dimension, otherwise padded by
// min(0.15·minDim, max(64, 0.5·span)).
func setExtent(seg *Segment, inliers []int, gw, f, w, h int) {
	dirx, diry := seg.dirVec()
	half := float64(f) / 2
	tmin, tmax := math.Inf(1), math.Inf(-1)
	for _, i := range inliers {
		fx, fy := float64((i%gw)*f)+half, float64((i/gw)*f)+half
		t := fx*dirx + fy*diry
		tmin, tmax = math.Min(tmin, t), math.Max(tmax, t)
	}
	minDim := w
	if h < w {
		minDim = h
	}
	span := tmax - tmin
	if span >= 0.6*float64(minDim) {
		if t0, t1, ok := borderT(*seg, w, h); ok {
			seg.T0, seg.T1 = t0, t1
			return
		}
	}
	ext := math.Max(64, 0.5*span)
	ext = math.Min(0.15*float64(minDim), ext)
	seg.T0, seg.T1 = tmin-ext, tmax+ext
}

// fullMedian is the robust median of the plane, from an evenly-spaced subsample for speed.
func fullMedian(plane []float32) float64 {
	return imgops.Percentile(imgops.Subsample(plane, 100000), 50)
}
