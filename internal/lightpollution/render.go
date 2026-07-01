package lightpollution

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/fsutil"
)

// coloredCacheVersion namespaces the recolored-tile cache. Bump it whenever bortlePalette, the gradient
// mapping, blackMarbleToSQM or sqmToBortle change, so stale colored tiles are ignored (served from a new
// tiles_bortle_v{N} directory) rather than reused. v2 = gradient follows the Bortle class boundaries
// (sqmToBortleF). v3 = tiles are rendered from the offline atlas where it covers the pixel (GIBS only
// outside coverage), so the map matches the accurate per-site badge.
const coloredCacheVersion = 3

// tileSize is the XYZ overlay tile edge in pixels (Leaflet + GIBS Black Marble are 256).
const tileSize = 256

// bortlePalette mirrors frontend/src/utils/bortle.ts BORTLE_COLORS: index 0 = Bortle 1 (darkest) …
// index 8 = Bortle 9 (brightest). A unit test pins these values so the overlay and the legend can't drift.
var bortlePalette = [9]color.NRGBA{
	{0x0b, 0x10, 0x26, 0xff}, // 1 pristine
	{0x13, 0x20, 0x5a, 0xff}, // 2 truly dark
	{0x1f, 0x4e, 0xa3, 0xff}, // 3 rural
	{0x2e, 0x8b, 0x57, 0xff}, // 4 rural/suburban
	{0xc9, 0xc4, 0x3e, 0xff}, // 5 suburban
	{0xe0, 0x92, 0x2e, 0xff}, // 6 bright suburban
	{0xd8, 0x44, 0x2c, 0xff}, // 7 suburban/urban
	{0xcf, 0x6f, 0xae, 0xff}, // 8 city
	{0xf3, 0xf3, 0xf3, 0xff}, // 9 inner city
}

// gradientLUT maps an 8-bit luminance to a smooth Bortle-gradient colour, precomputed once. The chain
// luminance → blackMarbleToSQM → palette position depends only on luminance, so the per-pixel cost of a
// tile recolor collapses to a single array read.
var gradientLUT = buildGradientLUT()

func buildGradientLUT() [256]color.NRGBA {
	var lut [256]color.NRGBA
	for l := 0; l < 256; l++ {
		sqm := blackMarbleToSQM(float64(l))
		// Map through the Bortle scale (not linearly over SQM) so the palette position equals the pixel's
		// Bortle class: t=0 → Bortle 1 (darkest), t=1 → Bortle 9 (brightest). A linear SQM ramp diverged
		// from the discrete sqmToBortle badge — painting Bortle-5 skies a Bortle-2/3 blue.
		t := clampf((sqmToBortleF(sqm)-1)/8, 0, 1)
		lut[l] = gradientColor(t)
	}
	return lut
}

// gradientColor interpolates the Bortle palette at t in [0,1]: t=0 → palette[0] (darkest), t=1 →
// palette[8] (brightest), lerping linearly between the two nearest stops.
func gradientColor(t float64) color.NRGBA {
	p := clampf(t, 0, 1) * float64(len(bortlePalette)-1)
	lo := int(math.Floor(p))
	hi := lo + 1
	if hi > len(bortlePalette)-1 {
		hi = len(bortlePalette) - 1
	}
	f := p - float64(lo)
	a, b := bortlePalette[lo], bortlePalette[hi]
	return color.NRGBA{
		R: lerp8(a.R, b.R, f),
		G: lerp8(a.G, b.G, f),
		B: lerp8(a.B, b.B, f),
		A: 0xff,
	}
}

func lerp8(a, b uint8, f float64) uint8 {
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*f))
}

// recolorTile turns a raw night-lights tile into an opaque smooth Bortle-gradient choropleth, 1:1 per
// pixel (web-mercator alignment preserved — no reprojection). Every pixel is opaque, so there are no
// transparent gaps: dark countryside / ocean render as Bortle-1 blue, never as holes.
func recolorTile(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := src.At(x, y).RGBA() // 16-bit, premultiplied; opaque source ⇒ straight
			lum := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257.0
			dst.SetNRGBA(x, y, gradientLUT[clampInt(int(lum+0.5), 0, 255)])
		}
	}
	return dst
}

// ColoredTile returns a local path to the Bortle-recolored overlay tile at z/x/y, caching the result under
// tiles_bortle_v{N}/. When the offline atlas covers (part of) the tile it renders each pixel from the
// atlas's accurate SQM — so the map matches the per-site badge; pixels outside coverage fall back to the
// coarse GIBS Black Marble recolor. With no atlas installed it is the plain GIBS recolor as before.
func (p *Provider) ColoredTile(ctx context.Context, z, x, y int) (string, error) {
	out := filepath.Join(p.cacheDir, fmt.Sprintf("tiles_bortle_v%d", coloredCacheVersion),
		strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+".png")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	if a := p.currentAtlas(); a != nil && tileIntersectsAtlas(z, x, y, a.meta) {
		img, err := p.renderAtlasTile(ctx, a, z, x, y)
		if err != nil {
			return "", err
		}
		if err := writePNGAtomic(out, img); err != nil {
			return "", err
		}
		return out, nil
	}

	rawPath, err := p.FetchTile(ctx, z, x, y)
	if err != nil {
		return "", err
	}
	src, err := decodePNG(rawPath)
	if err != nil {
		return "", err
	}
	if err := writePNGAtomic(out, recolorTile(src)); err != nil {
		return "", err
	}
	return out, nil
}

// renderAtlasTile paints a 256×256 tile from the atlas SQM (via the same sqmToBortleF gradient as the
// badge). Pixels the atlas does not cover fall back to the GIBS luminance recolor when a raw GIBS tile is
// available (fetched only when the tile is not wholly inside coverage), else to the darkest palette colour.
func (p *Provider) renderAtlasTile(ctx context.Context, a *atlas, z, x, y int) (*image.NRGBA, error) {
	a.ensureRAM() // fast per-pixel sampling; no-op (keeps ReadAt) for a too-large grid

	var gibs image.Image
	if !tileInsideAtlas(z, x, y, a.meta) && p.tileURL != "" {
		if rawPath, err := p.FetchTile(ctx, z, x, y); err == nil {
			gibs, _ = decodePNG(rawPath) // best-effort; nil → darkest fallback below
		}
	}

	dst := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	for py := 0; py < tileSize; py++ {
		for px := 0; px < tileSize; px++ {
			lat, lon := pixelToLatLon(z, x, y, px, py)
			if sqm, ok := a.sampleSQM(lat, lon); ok {
				t := clampf((sqmToBortleF(sqm)-1)/8, 0, 1)
				dst.SetNRGBA(px, py, gradientColor(t))
				continue
			}
			dst.SetNRGBA(px, py, gibsFallback(gibs, px, py))
		}
	}
	return dst, nil
}

// gibsFallback recolors one out-of-atlas pixel from the GIBS tile luminance, or the darkest palette colour
// when no GIBS tile is available.
func gibsFallback(gibs image.Image, px, py int) color.NRGBA {
	if gibs == nil {
		return bortlePalette[0]
	}
	b := gibs.Bounds()
	if px >= b.Dx() || py >= b.Dy() {
		return bortlePalette[0]
	}
	r, g, bl, _ := gibs.At(b.Min.X+px, b.Min.Y+py).RGBA()
	lum := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257.0
	return gradientLUT[clampInt(int(lum+0.5), 0, 255)]
}

// pixelToLatLon inverts the Web-Mercator (XYZ) tiling: the centre-less top-left mapping of a pixel (px,py)
// within tile z/x/y to geographic degrees. Inverse of mercatorTilePixel (sample.go).
func pixelToLatLon(z, x, y, px, py int) (lat, lon float64) {
	n := math.Exp2(float64(z))
	xf := (float64(x) + float64(px)/tileSize) / n
	yf := (float64(y) + float64(py)/tileSize) / n
	lon = xf*360.0 - 180.0
	lat = math.Atan(math.Sinh(math.Pi*(1-2*yf))) * 180.0 / math.Pi
	return lat, lon
}

// tileLatLonBounds returns the geographic bbox a tile covers (north/south/west/east degrees).
func tileLatLonBounds(z, x, y int) (n, s, w, e float64) {
	n, w = pixelToLatLon(z, x, y, 0, 0)
	s, e = pixelToLatLon(z, x, y, tileSize, tileSize)
	return n, s, w, e
}

// tileIntersectsAtlas reports whether the tile overlaps the atlas coverage at all (worth rendering from it).
func tileIntersectsAtlas(z, x, y int, m atlasMeta) bool {
	n, s, w, e := tileLatLonBounds(z, x, y)
	return s <= m.LatMax && n >= m.LatMin && w <= m.LonMax && e >= m.LonMin
}

// tileInsideAtlas reports whether the whole tile is within coverage (so no GIBS fallback fetch is needed).
func tileInsideAtlas(z, x, y int, m atlasMeta) bool {
	n, s, w, e := tileLatLonBounds(z, x, y)
	return s >= m.LatMin && n <= m.LatMax && w >= m.LonMin && e <= m.LonMax
}

func decodePNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode tile %s: %w", path, err)
	}
	return img, nil
}

// writePNGAtomic encodes img to a temp file and renames it into place, so a concurrent reader never sees
// a half-written tile (mirrors FetchTile).
func writePNGAtomic(path string, img image.Image) error {
	if err := fsutil.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
