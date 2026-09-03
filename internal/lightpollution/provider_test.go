package lightpollution

import (
	"context"
	"math"
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

// With no keyed API configured the atlas is the primary source, and it must be read at the coordinate
// asked for — NOT through the ~1 km cache key. Two points inside one cache cell but at different places
// on the sky-brightness gradient have to come back different; collapsing them onto one value is exactly
// the precision loss the ladder's step 0 exists to avoid.
func TestProvider_At_AtlasKeepsSubCacheCellPrecision(t *testing.T) {
	// A steep west→east gradient: SQM 22 (pristine) at lon 0, 17 (inner city) at lon 1.
	atlasBin := writeAtlas(t, atlasMeta{
		Rows: 2, Cols: 2, LatMin: 0, LatMax: 1, LonMin: 0, LonMax: 1, Unit: "sqm", NoData: -1,
	}, []float32{22, 17, 22, 17})

	p := New(&config.Config{
		WorkDir: t.TempDir(), DataDir: t.TempDir(), SkyDefaultSQM: 19.0,
		LightPollutionAtlas: atlasBin,
	})

	// Both round to the same cacheKey ("+0.50_+0.50"), so a cached answer would be identical.
	require.Equal(t, cacheKey(0.5, 0.500), cacheKey(0.5, 0.504))
	a, _ := p.At(context.Background(), 0.5, 0.500)
	b, _ := p.At(context.Background(), 0.5, 0.504)
	assert.Equal(t, "atlas", a.Source)
	assert.NotEqual(t, a.SQM, b.SQM, "two points in one cache cell must not share one atlas reading")
	assert.Greater(t, a.SQM, b.SQM, "brightness rises eastward, so the eastern point is less dark")
}

// BortleF carries the resolution the integer class throws away, and the badge shows them together — the
// swatch coloured on Bortle, the text printed from BortleF. So the fraction must always sit within half a
// class of the integer, or the pair reads as a contradiction. (Their rounding agreement away from the
// exact class boundaries is pinned in TestSqmToBortleF_RoundsToDiscrete; at a boundary the half-integer
// is a genuine tie and either neighbour is honest.)
func TestSiteQuality_BortleFAgreesWithClass(t *testing.T) {
	for sqm := 17.0; sqm <= 22.0; sqm += 0.01 {
		sq := newSiteQuality(sqm, "atlas")
		require.GreaterOrEqual(t, sq.BortleF, 1.0)
		require.LessOrEqual(t, sq.BortleF, 9.0)
		assert.LessOrEqualf(t, math.Abs(sq.BortleF-float64(sq.Bortle)), 0.5+1e-9,
			"SQM %.2f: class %d vs fractional %.2f", sqm, sq.Bortle, sq.BortleF)
	}
}

// Inside one class the fraction still moves — otherwise it adds a decimal point and no information.
func TestSiteQuality_BortleFVariesInsideAClass(t *testing.T) {
	dark, bright := newSiteQuality(21.6, "atlas"), newSiteQuality(21.3, "atlas")
	require.Equal(t, 4, dark.Bortle)
	require.Equal(t, 4, bright.Bortle)
	assert.Less(t, dark.BortleF, bright.BortleF)
}
