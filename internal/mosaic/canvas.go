package mosaic

import (
	"errors"
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	// canvasPadPx frames the union bbox so feathered panel edges and the resampling stencil never
	// touch the canvas border.
	canvasPadPx = 16
	// bboxMarginPx absorbs the tiny TAN curvature between the sampled boundary points of a panel.
	bboxMarginPx = 2
	// maxCanvasDim / maxCanvasPixels are the sanity caps: a wild plate solve must fail loudly
	// here, not OOM the engine allocating a gigapixel canvas.
	maxCanvasDim    = 65536
	maxCanvasPixels = int64(32768) * int64(32768)
)

// PanelImage is one panel's channel master ready for assembly. WCS must come from ParseWCS or
// NewTanWCS (a hand-built literal lacks the precomputed inverse and projects everything onto
// CRPIX).
type PanelImage struct {
	Label string
	Image *fits.Image // C==1 (channel masters are mono)
	WCS   fits.WCS
}

// CanvasSpec is the target mosaic canvas: north-up/east-left TAN grid.
type CanvasSpec struct {
	W, H int
	WCS  fits.WCS
}

// PlanCanvas sizes the canvas: tangent point = planCenter when hasPlanCenter, else the unit-vector
// centroid of the panel WCS centers; pixel scale = the scale of the panel nearest the tangent
// point (the anchor); orientation north-up/east-left (CD = [[-s,0],[0,s]], det<0 sky parity);
// bbox = the padded integer union of every panel's corners+edge-midpoints projected through
// panelWCS.PixToSky → canvasWCS.SkyToPix. Errors when no panel projects (all beyond 90° — corrupt
// solves) or the computed canvas exceeds the sanity caps.
func PlanCanvas(panels []PanelImage, planCenterRA, planCenterDec float64, hasPlanCenter bool) (CanvasSpec, error) {
	if len(panels) == 0 {
		return CanvasSpec{}, errors.New("mosaic: no panels to plan a canvas for")
	}
	if err := checkPanels(panels); err != nil {
		return CanvasSpec{}, err
	}
	ra0, dec0 := planCenterRA, planCenterDec
	if !hasPlanCenter {
		ra0, dec0 = centroidOfCenters(panels)
	}
	scale := panels[nearestPanel(panels, ra0, dec0)].WCS.ScaleArcsecPerPix() / 3600
	if scale <= 0 || math.IsNaN(scale) {
		return CanvasSpec{}, errors.New("mosaic: anchor panel has a degenerate pixel scale")
	}
	cd := [2][2]float64{{-scale, 0}, {0, scale}}
	prov, ok := fits.NewTanWCS(ra0, dec0, 1, 1, cd)
	if !ok {
		return CanvasSpec{}, errors.New("mosaic: degenerate canvas CD matrix")
	}
	minX, minY, maxX, maxY, projected := unionBounds(panels, prov)
	if !projected {
		return CanvasSpec{}, errors.New("mosaic: no panel projects onto the canvas tangent plane (corrupt plate solves?)")
	}
	x0 := int(math.Floor(minX)) - canvasPadPx
	y0 := int(math.Floor(minY)) - canvasPadPx
	w := int(math.Ceil(maxX)) - x0 + 1 + canvasPadPx
	h := int(math.Ceil(maxY)) - y0 + 1 + canvasPadPx
	if w > maxCanvasDim || h > maxCanvasDim || int64(w)*int64(h) > maxCanvasPixels {
		return CanvasSpec{}, fmt.Errorf("mosaic: computed canvas %dx%d exceeds the sanity cap (%d per axis, %d px total) — check the panel plate solves",
			w, h, maxCanvasDim, maxCanvasPixels)
	}
	wcs, ok := fits.NewTanWCS(ra0, dec0, float64(1-x0), float64(1-y0), cd)
	if !ok {
		return CanvasSpec{}, errors.New("mosaic: degenerate canvas WCS")
	}
	return CanvasSpec{W: w, H: h, WCS: wcs}, nil
}

// unionBounds is the float union of every panel's projected bbox on the provisional canvas grid;
// ok=false when no panel projects at all.
func unionBounds(panels []PanelImage, prov fits.WCS) (minX, minY, maxX, maxY float64, ok bool) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, p := range panels {
		x0, y0, x1, y1, k := projectBounds(p, prov)
		if !k {
			continue
		}
		ok = true
		minX, minY = math.Min(minX, x0), math.Min(minY, y0)
		maxX, maxY = math.Max(maxX, x1), math.Max(maxY, y1)
	}
	return minX, minY, maxX, maxY, ok
}

// checkPanels validates the shared PanelImage contract: a non-nil mono master with real dimensions.
func checkPanels(panels []PanelImage) error {
	for i, p := range panels {
		if p.Image == nil || p.Image.W <= 0 || p.Image.H <= 0 || len(p.Image.Pix) == 0 {
			return fmt.Errorf("mosaic: panel %d (%s): empty image", i, p.Label)
		}
		if p.Image.C != 1 {
			return fmt.Errorf("mosaic: panel %d (%s): expected a mono channel master, got C=%d", i, p.Label, p.Image.C)
		}
	}
	return nil
}

// panelCenterSky is the sky position of a panel's central pixel (more honest than CRVAL, which may
// sit anywhere in the frame).
func panelCenterSky(p PanelImage) (raDeg, decDeg float64) {
	return p.WCS.PixToSky(float64(p.Image.W-1)/2, float64(p.Image.H-1)/2)
}

// centroidOfCenters is the unit-vector mean of the panel centers.
func centroidOfCenters(panels []PanelImage) (raDeg, decDeg float64) {
	var sx, sy, sz float64
	for _, p := range panels {
		ra, dec := panelCenterSky(p)
		v := raDecVec(ra, dec)
		sx, sy, sz = sx+v[0], sy+v[1], sz+v[2]
	}
	if ra, dec, ok := vecRADec(sx, sy, sz); ok {
		return ra, dec
	}
	return panelCenterSky(panels[0]) // antipodal degenerate — projection will fail loudly later
}

// nearestPanel picks the panel whose center is angularly closest to (ra,dec).
func nearestPanel(panels []PanelImage, raDeg, decDeg float64) int {
	best, bestSep := 0, math.Inf(1)
	for i, p := range panels {
		ra, dec := panelCenterSky(p)
		if sep := astro.AngularSeparation(ra, dec, raDeg, decDeg); sep < bestSep {
			best, bestSep = i, sep
		}
	}
	return best
}

// projectBounds maps the panel's boundary (4 corners + 4 edge midpoints + center) through
// panelWCS.PixToSky → canvasWCS.SkyToPix and returns the enclosing canvas-space float bbox.
// ok=false when no boundary point projects (panel beyond the tangent horizon).
func projectBounds(p PanelImage, canvasWCS fits.WCS) (minX, minY, maxX, maxY float64, ok bool) {
	w, h := float64(p.Image.W-1), float64(p.Image.H-1)
	pts := [9][2]float64{
		{0, 0}, {w, 0}, {0, h}, {w, h},
		{w / 2, 0}, {w / 2, h}, {0, h / 2}, {w, h / 2}, {w / 2, h / 2},
	}
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, pt := range pts {
		ra, dec := p.WCS.PixToSky(pt[0], pt[1])
		x, y, k := canvasWCS.SkyToPix(ra, dec)
		if !k {
			continue
		}
		ok = true
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
	}
	return minX, minY, maxX, maxY, ok
}

// panelCanvasBBox is the integer canvas-space window a panel can write: projectBounds expanded by
// bboxMarginPx and clamped to the canvas. x1/y1 are EXCLUSIVE. ok=false when the panel misses the
// canvas entirely.
func panelCanvasBBox(p PanelImage, canvas CanvasSpec) (x0, y0, x1, y1 int, ok bool) {
	fx0, fy0, fx1, fy1, k := projectBounds(p, canvas.WCS)
	if !k {
		return 0, 0, 0, 0, false
	}
	x0 = max(int(math.Floor(fx0))-bboxMarginPx, 0)
	y0 = max(int(math.Floor(fy0))-bboxMarginPx, 0)
	x1 = min(int(math.Ceil(fx1))+bboxMarginPx+1, canvas.W)
	y1 = min(int(math.Ceil(fy1))+bboxMarginPx+1, canvas.H)
	if x0 >= x1 || y0 >= y1 {
		return 0, 0, 0, 0, false
	}
	return x0, y0, x1, y1, true
}
