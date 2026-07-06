package canopy

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fsutil"
)

// canopyTileZoom is the XYZ zoom sampled for tree-cover tiles. z=12 ≈ 20–40 m/pixel at mid-latitudes —
// fine enough for the near-field horizon ring, which starts ~30 m out.
const canopyTileZoom = 12
const canopyTilePx = 256

// tileCache decodes each XYZ tile at most once per lookup batch (a horizon ring repeatedly hits the same
// few tiles). It is NOT safe for concurrent use — make a fresh one per CanopyHeights call.
type tileCache struct {
	imgs map[string]image.Image
}

func newTileCache() *tileCache { return &tileCache{imgs: map[string]image.Image{}} }

// sampleTiles resolves canopy height from the keyless tree-cover-% tile tier: it samples the pixel covering
// (lat, lon), reads its tree-cover percentage (the 8-bit value of the first channel, 0–255 → 0–100 %), and
// maps a pixel at or above treeCover % to assumedM metres, else 0 (open). Returns 0 when no tile URL is
// configured, so with neither an atlas nor tiles the horizon stays terrain-only.
func (p *Provider) sampleTiles(ctx context.Context, lat, lon float64, tc *tileCache) (float64, string) {
	if p.tileURL == "" {
		return 0, ""
	}
	x, y, px, py := lonLatToTilePixel(lat, lon, canopyTileZoom)
	img, err := p.tileImage(ctx, canopyTileZoom, x, y, tc)
	if err != nil || img == nil {
		return 0, "tree-cover tiles unavailable — horizon uses terrain only here"
	}
	b := img.Bounds()
	if px >= b.Dx() || py >= b.Dy() {
		return 0, ""
	}
	r, _, _, _ := img.At(b.Min.X+px, b.Min.Y+py).RGBA() // 16-bit; r>>8 is the original 8-bit value
	coverPct := float64(r>>8) / 255 * 100
	if coverPct >= p.treeCover {
		return p.assumedM, ""
	}
	return 0, ""
}

// tileImage returns the decoded tile at z/x/y, decoding each tile at most once per batch (tc) and caching the
// raw PNG on disk so a re-run does not refetch.
func (p *Provider) tileImage(ctx context.Context, z, x, y int, tc *tileCache) (image.Image, error) {
	key := fmt.Sprintf("%d/%d/%d", z, x, y)
	if tc != nil {
		if img, ok := tc.imgs[key]; ok {
			return img, nil
		}
	}
	path := filepath.Join(p.cacheDir, "tiles", strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+".png")
	img, err := decodePNGFile(path)
	if err != nil {
		if err = p.fetchTile(ctx, z, x, y, path); err != nil {
			return nil, err
		}
		if img, err = decodePNGFile(path); err != nil {
			return nil, err
		}
	}
	if tc != nil {
		tc.imgs[key] = img
	}
	return img, nil
}

func (p *Provider) fetchTile(ctx context.Context, z, x, y int, dest string) error {
	url := strings.NewReplacer(
		"{z}", strconv.Itoa(z), "{x}", strconv.Itoa(x), "{y}", strconv.Itoa(y),
	).Replace(p.tileURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("canopy tile %d/%d/%d status %d", z, x, y, resp.StatusCode)
	}
	if err := fsutil.EnsureDir(filepath.Dir(dest)); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func decodePNGFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return png.Decode(f)
}

// lonLatToTilePixel maps (lat, lon) to its Web-Mercator XYZ tile and the pixel within that tile.
func lonLatToTilePixel(lat, lon float64, z int) (x, y, px, py int) {
	n := math.Exp2(float64(z))
	latRad := lat * math.Pi / 180
	xf := (lon + 180) / 360 * n
	yf := (1 - math.Log(math.Tan(latRad)+1/math.Cos(latRad))/math.Pi) / 2 * n
	x, y = int(xf), int(yf)
	px = int((xf - float64(x)) * canopyTilePx)
	py = int((yf - float64(y)) * canopyTilePx)
	return x, y, px, py
}
