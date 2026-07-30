package weather

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// countingOpenMeteo is a switchable fake upstream: mode "ok" answers like fakeOpenMeteo, "429" answers
// the rate-limit envelope, "500" a bare server error. Every request increments calls.
type countingOpenMeteo struct {
	srv   *httptest.Server
	calls atomic.Int32
	mode  atomic.Value // string
	delay time.Duration
}

func newCountingOpenMeteo(t *testing.T, delay time.Duration) *countingOpenMeteo {
	t.Helper()
	c := &countingOpenMeteo{delay: delay}
	c.mode.Store("ok")
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.calls.Add(1)
		if c.delay > 0 {
			time.Sleep(c.delay)
		}
		switch c.mode.Load().(string) {
		case "429":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":true,"reason":"Minutely API request limit exceeded. Please try again in one minute."}`)
		case "500":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			lat := r.URL.Query().Get("latitude")
			n := 1
			for _, ch := range lat {
				if ch == ',' {
					n++
				}
			}
			body := "["
			for i := 0; i < n; i++ {
				if i > 0 {
					body += ","
				}
				body += `{"latitude":0,"longitude":0,"hourly":{` + omHourlyBody + `}}`
			}
			_, _ = io.WriteString(w, body+"]")
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// ageGridCache back-dates the cube's memo entry AND its disk file so cachedGrid misses but staleGrid
// can still (or, past the grace, can no longer) serve it.
func ageGridCache(t *testing.T, p *Provider, key string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	p.mu.Lock()
	if c, ok := p.memoGrid[key]; ok {
		c.at = old
		p.memoGrid[key] = c
	}
	p.mu.Unlock()
	require.NoError(t, os.Chtimes(p.gridPath(key), old, old))
}

func (p *Provider) testGridKey(lat, lon, radius float64) string {
	return gridKey(p.snapGrid(lat, lon, radius), gridSupersetLayers)
}

// One cube serves every metric: after the first fetch, requests for other layer lists are cache hits.
func TestGrid_SharedCubeAcrossMetrics_OneFetch(t *testing.T) {
	up := newCountingOpenMeteo(t, 0)
	p := testProvider(t, up.srv.URL, "", "", "")

	var issued []int64
	for _, layers := range [][]string{{"clouds"}, {"humidity"}, {"precip"}, nil} {
		g, warn := p.Grid(context.Background(), 48.86, 2.35, 0, layers)
		require.Empty(t, warn)
		require.Len(t, g.Layers, len(gridSupersetLayers))
		issued = append(issued, g.IssuedMs)
	}
	assert.Equal(t, int32(1), up.calls.Load(), "clouds+humidity+precip+frames must share one upstream fetch")
	for _, ms := range issued[1:] {
		assert.Equal(t, issued[0], ms, "every metric sees the same cached cube")
	}
}

// A 429 serves the stale cube with a warning, returns the rate-limit sentinel from getJSON, and opens
// the breaker: the next call inside the cooldown makes NO upstream attempt.
func TestGrid_429_StaleAndBreaker(t *testing.T) {
	up := newCountingOpenMeteo(t, 0)
	p := testProvider(t, up.srv.URL, "", "", "")

	g, warn := p.Grid(context.Background(), 48.86, 2.35, 0, nil) // warm the cube
	require.Empty(t, warn)
	require.NotEmpty(t, g.Timesteps)
	key := p.testGridKey(48.86, 2.35, 2)
	ageGridCache(t, p, key, p.ttl+time.Minute) // expired, but well inside the stale grace

	up.mode.Store("429")
	var probe any
	err := p.getJSON(context.Background(), up.srv.URL, &probe)
	require.ErrorIs(t, err, ErrRateLimited, "a 429 must carry the rate-limit sentinel")

	before := up.calls.Load()
	stale, warn := p.Grid(context.Background(), 48.86, 2.35, 0, nil)
	assert.Equal(t, before+1, up.calls.Load(), "the first miss attempts one fetch")
	assert.NotEmpty(t, warn, "degraded result must carry a warning")
	assert.Equal(t, g.IssuedMs, stale.IssuedMs, "the stale cube is served, not an empty one")

	again, warn2 := p.Grid(context.Background(), 48.86, 2.35, 0, nil)
	assert.Equal(t, before+1, up.calls.Load(), "breaker open → no second upstream attempt")
	assert.NotEmpty(t, warn2)
	assert.Equal(t, g.IssuedMs, again.IssuedMs)
}

// Past ttl+staleGrace a dead upstream reads as honestly empty, not as day-old frames.
func TestGrid_StaleGraceBounded(t *testing.T) {
	up := newCountingOpenMeteo(t, 0)
	p := testProvider(t, up.srv.URL, "", "", "")

	g, _ := p.Grid(context.Background(), 48.86, 2.35, 0, nil)
	require.NotEmpty(t, g.Timesteps)
	key := p.testGridKey(48.86, 2.35, 2)
	ageGridCache(t, p, key, p.ttl+staleGrace+time.Minute)

	up.mode.Store("500")
	empty, warn := p.Grid(context.Background(), 48.86, 2.35, 0, nil)
	assert.Empty(t, empty.Timesteps, "a too-old cube must not be served")
	assert.Equal(t, "cloud map currently unavailable", warn)
}

// Concurrent misses for the same cube collapse into one upstream fetch (a Leaflet tile burst).
func TestGrid_SingleflightDedupes(t *testing.T) {
	up := newCountingOpenMeteo(t, 100*time.Millisecond)
	p := testProvider(t, up.srv.URL, "", "", "")

	const n = 8
	grids := make([]Grid, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			grids[i], _ = p.Grid(context.Background(), 48.86, 2.35, 0, nil)
		}(i)
	}
	wg.Wait()
	assert.Equal(t, int32(1), up.calls.Load(), "8 concurrent misses → 1 fetch")
	for i := 1; i < n; i++ {
		assert.Equal(t, grids[0].IssuedMs, grids[i].IssuedMs)
	}
}

// A just-failed cube is not re-attempted by the next request (negative memo), so a tile burst after a
// failure cannot retry-storm the upstream.
func TestGrid_NegativeCacheStopsRetryStorm(t *testing.T) {
	up := newCountingOpenMeteo(t, 0)
	up.mode.Store("500")
	p := testProvider(t, up.srv.URL, "", "", "")

	_, warn := p.Grid(context.Background(), 48.86, 2.35, 0, nil)
	assert.NotEmpty(t, warn)
	_, _ = p.Grid(context.Background(), 48.86, 2.35, 0, nil)
	assert.Equal(t, int32(1), up.calls.Load(), "second call inside the failure memo makes no attempt")
}

// The point budget keeps every real cube to a single chunked GET — the property that makes one fetch
// fit Open-Meteo's minutely quota.
func TestSnapGrid_BudgetSingleChunk(t *testing.T) {
	p := New(&config.Config{
		WorkDir:              t.TempDir(),
		WeatherGridSize:      32, // production default
		WeatherGridRadiusDeg: 4,
		WeatherCacheTTLMin:   30,
	})
	for _, radius := range []float64{0.5, 2, 5.625, 11.25, 24} {
		geom := p.snapGrid(48.86, 2.35, radius)
		points := geom.nx * geom.ny
		assert.LessOrEqual(t, points, maxGridPoints, "radius %v", radius)
		assert.Len(t, gridChunks(points, gridChunkMaxPoints), 1, "radius %v must fit one GET", radius)
	}
}
