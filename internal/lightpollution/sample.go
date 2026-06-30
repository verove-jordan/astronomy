package lightpollution

import (
	"context"
	"image/png"
	"math"
	"os"
)

// sampleZoom is the zoom level sampled for the per-site estimate. GIBS Black Marble's
// GoogleMapsCompatible matrix tops out at level 8 (≈600 m/pixel), enough to separate a town from the
// countryside around it.
const sampleZoom = 8

// sampleTileSQM estimates the sky brightness at (lat, lon) by reading the night-lights overlay tile that
// covers the site and converting the pixel brightness to SQM. It reuses the same keyless source and disk
// cache as the map overlay, so it needs no API key and no offline atlas. Returns ok=false on any
// network/decoding failure (the caller then falls back to the cached/default value).
func (p *Provider) sampleTileSQM(ctx context.Context, lat, lon float64) (float64, bool) {
	xt, yt, px, py := mercatorTilePixel(lat, lon, sampleZoom)
	path, err := p.FetchTile(ctx, sampleZoom, xt, yt)
	if err != nil {
		return 0, false
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		return 0, false
	}
	b := img.Bounds()
	if px >= b.Dx() || py >= b.Dy() {
		return 0, false
	}
	r, g, bl, _ := img.At(b.Min.X+px, b.Min.Y+py).RGBA() // each 0..65535
	lum := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257.0
	return blackMarbleToSQM(lum), true
}

// mercatorTilePixel maps (lat, lon) to the Web-Mercator (EPSG:3857, XYZ) tile column/row at zoom z and
// the pixel within that 256×256 tile.
func mercatorTilePixel(lat, lon float64, z int) (xtile, ytile, px, py int) {
	n := math.Exp2(float64(z))
	lat = clampf(lat, -85.05112878, 85.05112878)
	latRad := lat * math.Pi / 180
	xf := (lon + 180) / 360 * n
	yf := (1 - math.Asinh(math.Tan(latRad))/math.Pi) / 2 * n

	xtile = clampInt(int(math.Floor(xf)), 0, int(n)-1)
	ytile = clampInt(int(math.Floor(yf)), 0, int(n)-1)
	px = clampInt(int(math.Floor((xf-float64(xtile))*256)), 0, 255)
	py = clampInt(int(math.Floor((yf-float64(ytile))*256)), 0, 255)
	return xtile, ytile, px, py
}

// blackMarbleToSQM maps a VIIRS Black Marble night-lights brightness (0–255) to an approximate zenith
// sky brightness (mag/arcsec²): dark pixels → near-pristine, bright city pixels → strongly polluted. It
// is a coarse monotone estimate from an 8-bit visualization, not a calibrated radiometric value.
func blackMarbleToSQM(lum float64) float64 {
	b := clampf(lum/255, 0, 1)
	return clampf(pristineSQM-5.0*math.Sqrt(b), 16.5, pristineSQM)
}

func clampInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
