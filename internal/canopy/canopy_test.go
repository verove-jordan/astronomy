package canopy

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/geogrid"
)

func writeCanopyAtlas(t *testing.T, dir string, m geogrid.Meta, cells []float32) string {
	t.Helper()
	bin := filepath.Join(dir, "atlas.bin")
	buf := make([]byte, len(cells)*4)
	for i, v := range cells {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	require.NoError(t, os.WriteFile(bin, buf, 0o644))
	mb, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "atlas.json"), mb, 0o644))
	return bin
}

func TestCanopyInactiveNoSources(t *testing.T) {
	p := New(&config.Config{WorkDir: t.TempDir()})
	assert.False(t, p.Active())
	m, warn := p.CanopyHeight(context.Background(), 45, 5)
	assert.Equal(t, 0.0, m) // absence = open sky, no warning
	assert.Empty(t, warn)
}

func TestCanopyAtlasHeights(t *testing.T) {
	dir := t.TempDir()
	// 2×2 over [44,46]×[4,6]; tallest cell 25 m (SE).
	m := geogrid.Meta{Rows: 2, Cols: 2, LatMin: 44, LatMax: 46, LonMin: 4, LonMax: 6, Unit: "meters", NoData: -1}
	bin := writeCanopyAtlas(t, dir, m, []float32{10, 10, 10, 25})
	p := New(&config.Config{WorkDir: t.TempDir(), CanopyAtlas: bin})
	require.True(t, p.Active())

	// A point inside coverage samples the worst-case (max) neighbour = 25 m.
	got, warn := p.CanopyHeight(context.Background(), 45, 5)
	assert.Empty(t, warn)
	assert.InDelta(t, 25.0, got, 0.01)

	// Batch: covered point → 25 m, out-of-coverage point → 0 (open sky).
	hs := p.CanopyHeights(context.Background(), []float64{45, 80}, []float64{5, 80})
	require.Len(t, hs, 2)
	assert.InDelta(t, 25.0, hs[0], 0.01)
	assert.Equal(t, 0.0, hs[1])
}

// canopyTileServer serves a solid tree-cover tile whose first channel encodes the cover %.
func canopyTileServer(t *testing.T, coverByte uint8) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		img := image.NewRGBA(image.Rect(0, 0, 256, 256))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i] = coverByte // R = tree-cover value
			img.Pix[i+3] = 0xff    // opaque
		}
		_ = png.Encode(w, img)
	}))
}

func TestCanopyTileTier(t *testing.T) {
	t.Run("dense cover maps to the assumed height", func(t *testing.T) {
		srv := canopyTileServer(t, 200) // ~78 % ≥ 30 % threshold
		defer srv.Close()
		p := New(&config.Config{WorkDir: t.TempDir(), CanopyTileURL: srv.URL + "/{z}/{x}/{y}.png"})
		require.True(t, p.Active())
		m, warn := p.CanopyHeight(context.Background(), 45, 5)
		assert.Empty(t, warn)
		assert.InDelta(t, 18.0, m, 0.01) // default assumed canopy height
	})

	t.Run("sparse cover is open sky", func(t *testing.T) {
		srv := canopyTileServer(t, 20) // ~8 % < 30 % threshold
		defer srv.Close()
		p := New(&config.Config{WorkDir: t.TempDir(), CanopyTileURL: srv.URL + "/{z}/{x}/{y}.png"})
		m, _ := p.CanopyHeight(context.Background(), 45, 5)
		assert.Equal(t, 0.0, m)
	})
}
