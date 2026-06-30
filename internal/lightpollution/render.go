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
// tiles_bortle_v{N} directory) rather than reused.
const coloredCacheVersion = 1

// minGradientSQM is the bright end of the gradient. blackMarbleToSQM bottoms out at 17.0 (22 − 5·√1), so
// a fully-lit pixel maps to the brightest palette colour and a dark pixel to the darkest.
const minGradientSQM = 17.0

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
		t := clampf((pristineSQM-sqm)/(pristineSQM-minGradientSQM), 0, 1) // 0 = darkest … 1 = brightest
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

// ColoredTile returns a local path to the Bortle-recolored overlay tile at z/x/y. It reuses the raw tile
// (and its disk cache) via FetchTile, recolors once, and caches the result under tiles_bortle_v{N}/. The
// raw tiles/ cache and the per-site At() sampler are untouched.
func (p *Provider) ColoredTile(ctx context.Context, z, x, y int) (string, error) {
	out := filepath.Join(p.cacheDir, fmt.Sprintf("tiles_bortle_v%d", coloredCacheVersion),
		strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+".png")
	if _, err := os.Stat(out); err == nil {
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
