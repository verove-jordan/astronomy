package thumb

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePNG(t *testing.T, dir string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	p := filepath.Join(dir, "src.png")
	f, err := os.Create(p)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, img))
	require.NoError(t, f.Close())
	return p
}

func decodeDims(t *testing.T, data []byte) (int, int) {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

// TestJPEG_Resize covers the two cases that matter for gallery thumbnails: downscale to the long-side
// limit while preserving aspect ratio, and never upscaling an already-small image.
func TestJPEG_Resize(t *testing.T) {
	tests := []struct {
		name            string
		srcW, srcH, dim int
		wantW, wantH    int
	}{
		{"downscale landscape preserves aspect", 800, 600, 480, 480, 360},
		{"downscale portrait preserves aspect", 600, 800, 480, 360, 480},
		{"never upscales", 100, 80, 480, 100, 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := writePNG(t, t.TempDir(), tt.srcW, tt.srcH)
			data, err := JPEG(src, tt.dim, 80)
			require.NoError(t, err)
			w, h := decodeDims(t, data)
			assert.Equal(t, tt.wantW, w)
			assert.Equal(t, tt.wantH, h)
		})
	}
}

// TestCached_WritesAndReuses checks the disk cache writes one entry and returns identical bytes on a hit.
func TestCached_WritesAndReuses(t *testing.T) {
	dir := t.TempDir()
	src := writePNG(t, dir, 400, 300)
	cacheDir := filepath.Join(dir, "cache")

	first, err := Cached(cacheDir, src, 200, 80)
	require.NoError(t, err)

	entries, err := os.ReadDir(cacheDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "one cached thumbnail written")

	second, err := Cached(cacheDir, src, 200, 80)
	require.NoError(t, err)
	assert.Equal(t, first, second, "cache hit returns identical bytes")
}
