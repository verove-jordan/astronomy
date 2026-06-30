// Package thumb generates small JPEG thumbnails of run output images so the web gallery loads fast
// (a ~480px JPEG instead of the multi-megabyte full-resolution PNG). It is deliberately format-light:
// run previews are PNG/JPEG, decoded with the standard library and scaled with golang.org/x/image.
package thumb

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"  // registers the JPEG decoder and provides Encode
	_ "image/png" // register the PNG decoder for image.Decode
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
)

// JPEG decodes the image at path, scales it so its longest side is at most maxDim (never upscaling),
// and returns a JPEG encoded at the given quality.
func JPEG(path string, maxDim, quality int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scale(src, maxDim), &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Cached returns a JPEG thumbnail of src, memoized on disk under cacheDir. The cache key includes the
// source's modification time and size, so editing or replacing the source naturally invalidates it
// (the stale entry is simply orphaned). A cache miss generates the thumbnail and writes it atomically.
func Cached(cacheDir, src string, maxDim, quality int) ([]byte, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s|%d|%d|%d|%d", src, info.ModTime().UnixNano(), info.Size(), maxDim, quality)
	sum := sha1.Sum([]byte(key))
	cacheFile := filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".jpg")
	if data, err := os.ReadFile(cacheFile); err == nil {
		return data, nil
	}
	data, err := JPEG(src, maxDim, quality)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err == nil {
		tmp := cacheFile + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, cacheFile) // atomic publish; ignore failures (cache is best-effort)
		}
	}
	return data, nil
}

// scale returns src resized so its longest side is at most maxDim, preserving aspect ratio. src is
// returned unchanged when it already fits (thumbnails never upscale). High-quality Catmull-Rom resampling.
func scale(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxDim <= 0 || (w <= maxDim && h <= maxDim) {
		return src
	}
	nw, nh := w, h
	if w >= h {
		nw, nh = maxDim, h*maxDim/w
	} else {
		nw, nh = w*maxDim/h, maxDim
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}
