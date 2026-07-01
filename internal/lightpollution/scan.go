package lightpollution

import (
	"context"
	"image"
	"image/png"
	"os"
)

// Bbox is a geographic bounding box in degrees.
type Bbox struct {
	MinLat, MinLon, MaxLat, MaxLon float64
}

// Cell is one sampled grid point: its location and resolved sky brightness.
type Cell struct {
	Lat, Lon float64
	SQM      float64
	Bortle   int
}

// ScanArea samples sky brightness on an nx×ny grid spanning bbox and returns every cell where a value
// could be resolved. It uses the offline atlas where available, otherwise the keyless z8 night-lights
// tiles (each covering tile decoded at most once), so a whole-region scan is a handful of cached
// fetches — never a per-cell network call. Cells with no data (outside coverage / decode failure) are
// skipped. ScanArea does NOT touch the per-point cache/API chain (point-keyed and rate-limited).
func (p *Provider) ScanArea(ctx context.Context, bbox Bbox, nx, ny int) []Cell {
	if nx < 1 {
		nx = 1
	}
	if ny < 1 {
		ny = 1
	}
	tiles := map[[2]int]image.Image{} // decoded z8 tiles, keyed by (x,y); nil marks a failed fetch
	out := make([]Cell, 0, nx*ny)
	for j := 0; j < ny; j++ {
		lat := gridCoord(bbox.MinLat, bbox.MaxLat, ny, j)
		for i := 0; i < nx; i++ {
			if ctx.Err() != nil {
				return out
			}
			lon := gridCoord(bbox.MinLon, bbox.MaxLon, nx, i)
			sqm, ok := p.scanSQM(ctx, lat, lon, tiles)
			if !ok {
				continue
			}
			sqm = clampf(sqm, 14.0, pristineSQM)
			out = append(out, Cell{Lat: lat, Lon: lon, SQM: round2(sqm), Bortle: sqmToBortle(sqm)})
		}
	}
	return out
}

// scanSQM resolves SQM for one grid cell: the offline atlas first, else a pixel from the (decode-once)
// night-lights tile covering the point.
func (p *Provider) scanSQM(ctx context.Context, lat, lon float64, tiles map[[2]int]image.Image) (float64, bool) {
	if a := p.currentAtlas(); a != nil {
		if sqm, ok := a.sampleSQM(lat, lon); ok {
			return sqm, true
		}
	}
	if p.tileURL == "" {
		return 0, false
	}
	xt, yt, px, py := mercatorTilePixel(lat, lon, sampleZoom)
	key := [2]int{xt, yt}
	img, seen := tiles[key]
	if !seen {
		img = p.decodeTile(ctx, xt, yt) // nil on any failure
		tiles[key] = img
	}
	if img == nil {
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

// decodeTile fetches (cached) and decodes one z8 night-lights tile, returning nil on any failure.
func (p *Provider) decodeTile(ctx context.Context, xt, yt int) image.Image {
	path, err := p.FetchTile(ctx, sampleZoom, xt, yt)
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// gridCoord returns the coordinate of grid index i (0..n-1) spanning [lo, hi]; n==1 → the midpoint.
func gridCoord(lo, hi float64, n, i int) float64 {
	if n <= 1 {
		return (lo + hi) / 2
	}
	return lo + (hi-lo)*float64(i)/float64(n-1)
}
