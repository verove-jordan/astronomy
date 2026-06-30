package lightpollution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

func TestProvider_At_DefaultWhenNoSources(t *testing.T) {
	p := New(&config.Config{WorkDir: t.TempDir(), DataDir: t.TempDir(), SkyDefaultSQM: 21.3})
	sq, warn := p.At(context.Background(), 48.0, 2.0)
	assert.Equal(t, "default", sq.Source)
	assert.InDelta(t, 21.3, sq.SQM, 0.001)
	assert.Equal(t, 4, sq.Bortle)
	assert.NotEmpty(t, warn)
}

func TestProvider_At_APISuccessThenCache(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"sqm": 21.95}`))
	}))
	defer srv.Close()

	p := New(&config.Config{
		WorkDir: t.TempDir(), DataDir: t.TempDir(), SkyDefaultSQM: 21.3,
		LightPollutionAPIURL: srv.URL + "?lat={lat}&lon={lon}",
	})

	sq, warn := p.At(context.Background(), 44.6, 6.5)
	require.Equal(t, "api", sq.Source)
	assert.InDelta(t, 21.95, sq.SQM, 0.001)
	assert.Equal(t, 2, sq.Bortle)
	assert.Empty(t, warn)

	// Second lookup at the same rounded location is served from cache — no second upstream hit.
	sq2, warn2 := p.At(context.Background(), 44.601, 6.499)
	assert.Equal(t, "api", sq2.Source)
	assert.Empty(t, warn2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

func TestProvider_At_FallsBackToAtlasWhenAPIFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	atlasBin := writeAtlas(t, atlasMeta{
		Rows: 2, Cols: 2, LatMin: 0, LatMax: 1, LonMin: 0, LonMax: 1, Unit: "sqm", NoData: -1,
	}, []float32{21, 21, 21, 21})

	p := New(&config.Config{
		WorkDir: t.TempDir(), DataDir: t.TempDir(), SkyDefaultSQM: 19.0,
		LightPollutionAPIURL: srv.URL + "?lat={lat}&lon={lon}",
		LightPollutionAtlas:  atlasBin,
	})

	sq, warn := p.At(context.Background(), 0.5, 0.5)
	assert.Equal(t, "atlas", sq.Source)
	assert.InDelta(t, 21.0, sq.SQM, 0.001)
	assert.Contains(t, warn, "offline atlas")
}

func TestProvider_FetchTile_NoSource(t *testing.T) {
	p := New(&config.Config{WorkDir: t.TempDir(), DataDir: t.TempDir()})
	_, err := p.FetchTile(context.Background(), 5, 1, 1)
	assert.ErrorIs(t, err, ErrNoTileSource)
}

func TestProvider_FetchTile_ProxiesAndCaches(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	p := New(&config.Config{
		WorkDir: t.TempDir(), DataDir: t.TempDir(),
		LightPollutionTileURL: srv.URL + "/{z}/{x}/{y}.png",
	})
	path1, err := p.FetchTile(context.Background(), 5, 15, 10)
	require.NoError(t, err)
	path2, err := p.FetchTile(context.Background(), 5, 15, 10)
	require.NoError(t, err)
	assert.Equal(t, path1, path2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits), "second fetch should hit the disk cache")
}
