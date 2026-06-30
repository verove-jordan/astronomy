package lightpollution

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

func TestBortlePalette(t *testing.T) {
	assert.Len(t, bortlePalette, 9)
	assert.Equal(t, color.NRGBA{0x0b, 0x10, 0x26, 0xff}, bortlePalette[0], "Bortle 1 = darkest")
	assert.Equal(t, color.NRGBA{0xf3, 0xf3, 0xf3, 0xff}, bortlePalette[8], "Bortle 9 = brightest")
	for _, c := range bortlePalette {
		assert.Equal(t, uint8(0xff), c.A, "palette colors are opaque")
	}
}

func TestGradientLUT(t *testing.T) {
	assert.Equal(t, bortlePalette[0], gradientLUT[0], "darkest luminance → darkest Bortle color")
	assert.Equal(t, bortlePalette[8], gradientLUT[255], "brightest luminance → brightest Bortle color")
	for _, c := range gradientLUT {
		assert.Equal(t, uint8(0xff), c.A, "gradient colors are opaque")
	}
	mid := gradientLUT[128]
	assert.NotEqual(t, gradientLUT[0], mid, "midtones differ from the dark end")
	assert.NotEqual(t, gradientLUT[255], mid, "midtones differ from the bright end")
}

func TestRecolorTile(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{0, 0, 0, 0xff})          // black → darkest
	src.Set(1, 0, color.RGBA{0xff, 0xff, 0xff, 0xff}) // white → brightest
	dst := recolorTile(src)
	assert.Equal(t, bortlePalette[0], dst.NRGBAAt(0, 0))
	assert.Equal(t, bortlePalette[8], dst.NRGBAAt(1, 0))
	assert.Equal(t, uint8(0xff), dst.NRGBAAt(0, 0).A, "output is opaque (no transparent gaps)")
}

func solidPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestProvider_ColoredTile_RendersAndCaches(t *testing.T) {
	black := solidPNG(t, color.RGBA{0, 0, 0, 0xff})
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(black)
	}))
	defer srv.Close()

	p := New(&config.Config{
		WorkDir: t.TempDir(), DataDir: t.TempDir(),
		LightPollutionTileURL: srv.URL + "/{z}/{x}/{y}.png",
	})

	path1, err := p.ColoredTile(context.Background(), 5, 15, 10)
	require.NoError(t, err)
	assert.Contains(t, path1, filepath.Join("tiles_bortle_v1", "5", "15"))

	// The rendered tile is the all-dark Bortle color, opaque.
	f, err := os.Open(path1)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	require.NoError(t, err)
	r, g, b, a := img.At(0, 0).RGBA()
	got := color.NRGBA{uint8(r / 257), uint8(g / 257), uint8(b / 257), uint8(a / 257)}
	assert.Equal(t, bortlePalette[0], got)

	// Second call serves the cached colored tile — no extra upstream fetch.
	path2, err := p.ColoredTile(context.Background(), 5, 15, 10)
	require.NoError(t, err)
	assert.Equal(t, path1, path2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

func TestProvider_ColoredTile_NoSource(t *testing.T) {
	p := New(&config.Config{WorkDir: t.TempDir(), DataDir: t.TempDir()})
	_, err := p.ColoredTile(context.Background(), 5, 1, 1)
	assert.ErrorIs(t, err, ErrNoTileSource)
}
