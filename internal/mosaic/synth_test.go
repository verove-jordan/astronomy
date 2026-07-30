package mosaic

// Shared synthetic-mosaic test fixtures: real TAN WCS panels photographing one analytic sky scene
// (linear tangent-plane gradient + Gaussian stars pinned to sky coordinates) through per-panel
// injected gain/offset/noise, with optional edge-elongated star optics.

import (
	"math"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// testScale is the synthetic plate scale: 2 arcseconds per pixel, in degrees.
const testScale = 2.0 / 3600

// tanWCS builds a TAN solution centered on (ra,dec): pixel scale sDeg, rotation paDeg, east-left
// (det<0) parity, CRPIX at the frame center.
func tanWCS(t *testing.T, w, h int, ra, dec, sDeg, paDeg float64) fits.WCS {
	t.Helper()
	sin, cos := math.Sincos(paDeg * math.Pi / 180)
	cd := [2][2]float64{{-sDeg * cos, -sDeg * sin}, {-sDeg * sin, sDeg * cos}}
	wcs, ok := fits.NewTanWCS(ra, dec, (float64(w)+1)/2, (float64(h)+1)/2, cd)
	require.True(t, ok, "synthetic WCS must not be degenerate")
	return wcs
}

type testStar struct {
	ra, dec float64
	flux    float64
}

// testScene is the analytic sky: base + gxi·ξ + geta·η (ξ,η in degrees at the scene center),
// plus Gaussian stars at fixed sky positions.
type testScene struct {
	ra0, dec0       float64
	base, gxi, geta float64
	stars           []testStar
}

func (s testScene) skyAt(ra, dec float64) float64 {
	xi, eta, _ := astro.TangentPlane(s.ra0, s.dec0, ra, dec)
	return s.base + s.gxi*xi + s.geta*eta
}

// renderPanel photographs the scene through a panel WCS: pixel = (sky+stars)·gain + offset +
// noise. Stars landing within edgeElongPx of a panel edge render elongated (σ 2.6×1.2 instead of
// the round 1.5), emulating edge optics.
func renderPanel(w, h int, wcs fits.WCS, sc testScene, gain, offset, noise float64, rng *rand.Rand, edgeElongPx int) *fits.Image {
	im := fits.NewImage(w, h, 1)
	pix := im.Pix[0]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ra, dec := wcs.PixToSky(float64(x), float64(y))
			v := sc.skyAt(ra, dec)*gain + offset
			if noise > 0 {
				v += rng.NormFloat64() * noise
			}
			pix[y*w+x] = float32(v)
		}
	}
	for _, st := range sc.stars {
		sx, sy, ok := wcs.SkyToPix(st.ra, st.dec)
		if !ok || sx < -8 || sy < -8 || sx > float64(w-1)+8 || sy > float64(h-1)+8 {
			continue
		}
		sigX, sigY := 1.5, 1.5
		if edgeElongPx > 0 && nearEdge(sx, sy, w, h, float64(edgeElongPx)) {
			sigX, sigY = 2.6, 1.2
		}
		addStar(pix, w, h, sx, sy, st.flux*gain, sigX, sigY)
	}
	return im
}

func nearEdge(x, y float64, w, h int, d float64) bool {
	return x < d || y < d || x > float64(w-1)-d || y > float64(h-1)-d
}

func addStar(pix []float32, w, h int, sx, sy, flux, sigX, sigY float64) {
	x0, x1 := clampInt(int(sx)-8, 0, w-1), clampInt(int(sx)+8, 0, w-1)
	y0, y1 := clampInt(int(sy)-8, 0, h-1), clampInt(int(sy)+8, 0, h-1)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			dx, dy := float64(x)-sx, float64(y)-sy
			pix[y*w+x] += float32(flux * math.Exp(-(dx*dx/(2*sigX*sigX) + dy*dy/(2*sigY*sigY))))
		}
	}
}

// gridWCS lays an nx×ny mosaic of w×h panels around (ra0,dec0) with the given overlap fraction:
// panel (gx,gy) centers at tangent-plane offsets so that increasing gx runs along +x pixels and
// increasing gy along +y pixels of its neighbors. Row-major order.
func gridWCS(t *testing.T, ra0, dec0 float64, nx, ny, w, h int, overlap float64) []fits.WCS {
	t.Helper()
	stepX := (1 - overlap) * float64(w) * testScale
	stepY := (1 - overlap) * float64(h) * testScale
	out := make([]fits.WCS, 0, nx*ny)
	for gy := 0; gy < ny; gy++ {
		for gx := 0; gx < nx; gx++ {
			xi := -(float64(gx) - float64(nx-1)/2) * stepX // CD1_1<0: +x pixels run east-to-west (ξ down)
			eta := (float64(gy) - float64(ny-1)/2) * stepY
			ra, dec := astro.TangentSky(ra0, dec0, xi, eta)
			out = append(out, tanWCS(t, w, h, ra, dec, testScale, 0))
		}
	}
	return out
}

// peakNear finds the brightest pixel within ±r of (x,y).
func peakNear(pix []float32, w, h int, x, y float64, r int) (px, py int) {
	cx, cy := int(math.Round(x)), int(math.Round(y))
	best := float32(math.Inf(-1))
	for yy := clampInt(cy-r, 0, h-1); yy <= clampInt(cy+r, 0, h-1); yy++ {
		for xx := clampInt(cx-r, 0, w-1); xx <= clampInt(cx+r, 0, w-1); xx++ {
			if pix[yy*w+xx] > best {
				best, px, py = pix[yy*w+xx], xx, yy
			}
		}
	}
	return px, py
}

// medianWindow is the median pixel value of the (2r+1)² window around (cx,cy).
func medianWindow(pix []float32, w, h, cx, cy, r int) float64 {
	var vals []float64
	for y := clampInt(cy-r, 0, h-1); y <= clampInt(cy+r, 0, h-1); y++ {
		for x := clampInt(cx-r, 0, w-1); x <= clampInt(cx+r, 0, w-1); x++ {
			vals = append(vals, float64(pix[y*w+x]))
		}
	}
	sort.Float64s(vals)
	return quantileSorted(vals, 50)
}

// axisRatioAt measures the minor/major axis ratio of the star nearest (x,y): background-subtracted
// second moments over a 13×13 window (background = median of the window border). 1 = round.
func axisRatioAt(pix []float32, w, h int, x, y float64) float64 {
	px, py := peakNear(pix, w, h, x, y, 4)
	const r = 6
	var border []float64
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx > -r && dx < r && dy > -r && dy < r {
				continue
			}
			border = append(border, float64(pix[clampInt(py+dy, 0, h-1)*w+clampInt(px+dx, 0, w-1)]))
		}
	}
	bg := medianF64(border)
	var m00, mx, my float64
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			q := float64(pix[clampInt(py+dy, 0, h-1)*w+clampInt(px+dx, 0, w-1)]) - bg
			if q <= 0 {
				continue
			}
			m00 += q
			mx += q * float64(dx)
			my += q * float64(dy)
		}
	}
	if m00 <= 0 {
		return 0
	}
	cx, cy := mx/m00, my/m00
	var ixx, iyy, ixy float64
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			q := float64(pix[clampInt(py+dy, 0, h-1)*w+clampInt(px+dx, 0, w-1)]) - bg
			if q <= 0 {
				continue
			}
			ex, ey := float64(dx)-cx, float64(dy)-cy
			ixx += q * ex * ex
			iyy += q * ey * ey
			ixy += q * ex * ey
		}
	}
	tr, d := ixx+iyy, math.Sqrt((ixx-iyy)*(ixx-iyy)+4*ixy*ixy)
	hi, lo := (tr+d)/2, (tr-d)/2
	if hi <= 0 || lo < 0 {
		return 0
	}
	return math.Sqrt(lo / hi)
}
