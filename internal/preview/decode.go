package preview

import (
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // register JPEG decoder for image.Decode
	_ "image/png"  // register PNG decoder for image.Decode
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/tiff"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/rawconv"
)

var (
	fitsExts = map[string]bool{".fits": true, ".fit": true, ".fts": true}
	// rawExts are one-shot-color camera raws that Siril's libraw often can't read; transcoded via sips.
	rawExts = map[string]bool{
		".dng": true, ".heic": true, ".heif": true,
		".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true,
	}
	// stillExts are formats the Go image stack decodes directly (TIFF via x/image).
	stillExts = map[string]bool{".tif": true, ".tiff": true, ".png": true, ".jpg": true, ".jpeg": true}
)

// decodeSource decodes path into a lazily-addressed pixel source, dispatching by format: FITS in pure
// Go; camera raws transcoded to 16-bit TIFF via sips (reusing rawconv) then decoded; everything else
// (TIFF/PNG/JPEG) through the Go image stack. maxEdge bounds the raw transcode so a 48 MP DNG develops
// at preview size (fast), not full resolution.
func decodeSource(ctx context.Context, path string, maxEdge int) (pixelSource, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case fitsExts[ext]:
		im, err := fits.ReadImage(path)
		if err != nil {
			return nil, err
		}
		return &fitsSource{im: im}, nil

	case rawExts[ext]:
		tif, cleanup, err := transcodeRaw(ctx, path, maxEdge)
		if err != nil {
			return nil, err
		}
		defer cleanup() // safe: decodeImageFile reads the TIFF fully into memory before we return
		im, err := decodeImageFile(tif)
		if err != nil {
			return nil, err
		}
		return newImageSource(im), nil

	case stillExts[ext]:
		im, err := decodeImageFile(path)
		if err != nil {
			return nil, err
		}
		return newImageSource(im), nil

	default:
		return nil, fmt.Errorf("preview: unsupported file type %q", ext)
	}
}

// transcodeRaw develops a camera raw to a temporary, DOWNSCALED 16-bit TIFF via rawconv (macOS sips),
// returning the TIFF path and a cleanup func for the temp dir. Developing at maxEdge (not full
// resolution) is what keeps a hover/preview of a 48 MP DNG near-instant instead of ~5 s.
func transcodeRaw(ctx context.Context, path string, maxEdge int) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "astro-preview-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	dst := filepath.Join(tmp, "preview.png")
	if err := rawconv.Thumbnail(ctx, path, dst, maxEdge); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("preview: decode raw %s: %w", filepath.Base(path), err)
	}
	return dst, cleanup, nil
}

// decodeImageFile decodes a TIFF/PNG/JPEG file into an in-memory image.Image.
func decodeImageFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tif", ".tiff":
		return tiff.Decode(f)
	default:
		im, _, err := image.Decode(f)
		return im, err
	}
}

// fitsSource adapts a decoded FITS image (planar float32, channel-major) to pixelSource.
type fitsSource struct{ im *fits.Image }

func (s *fitsSource) dims() (int, int, int) { return s.im.W, s.im.H, s.im.C }
func (s *fitsSource) at(c, x, y int) float64 {
	return float64(s.im.Pix[c][y*s.im.W+x])
}

// imageSource adapts a Go image.Image to pixelSource, reading 16-bit samples via RGBA(). Mono images
// (Gray/Gray16) expose one channel; everything else exposes RGB.
type imageSource struct {
	im      image.Image
	w, h, c int
	ox, oy  int
}

func newImageSource(im image.Image) *imageSource {
	b := im.Bounds()
	c := 3
	switch im.ColorModel() {
	case color.GrayModel, color.Gray16Model:
		c = 1
	}
	return &imageSource{im: im, w: b.Dx(), h: b.Dy(), c: c, ox: b.Min.X, oy: b.Min.Y}
}

func (s *imageSource) dims() (int, int, int) { return s.w, s.h, s.c }
func (s *imageSource) at(c, x, y int) float64 {
	r, g, b, _ := s.im.At(s.ox+x, s.oy+y).RGBA() // 16-bit, alpha-premultiplied (opaque captures: true value)
	if s.c == 1 {
		return float64(r) // gray: r == g == b
	}
	switch c {
	case 0:
		return float64(r)
	case 1:
		return float64(g)
	default:
		return float64(b)
	}
}
