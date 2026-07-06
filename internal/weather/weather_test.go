package weather

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// omHourlyBody is a small 3-hour Open-Meteo hourly block reused for the point and the grid fakes.
const omHourlyBody = `"time":["2026-06-30T20:00","2026-06-30T21:00","2026-06-30T22:00"],` +
	`"cloud_cover":[10,80,5],"cloud_cover_low":[5,40,2],"cloud_cover_mid":[3,25,1],` +
	`"cloud_cover_high":[8,60,4],"relative_humidity_2m":[60,85,55],` +
	`"dew_point_2m":[8,12,7],"temperature_2m":[15,14,13],"wind_speed_10m":[5,10,4],"wind_speed_300hPa":[80,90,70]`

func fakeOpenMeteo(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lat := r.URL.Query().Get("latitude")
		if strings.Contains(lat, ",") { // grid request → JSON array, one object per coordinate
			n := strings.Count(lat, ",") + 1
			objs := make([]string, n)
			for i := range objs {
				objs[i] = `{"latitude":0,"longitude":0,"hourly":{` + omHourlyBody + `}}`
			}
			_, _ = io.WriteString(w, "["+strings.Join(objs, ",")+"]")
			return
		}
		_, _ = io.WriteString(w, `{"latitude":48.86,"longitude":2.35,"hourly":{`+omHourlyBody+`}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func fakeJSON(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func testProvider(t *testing.T, omURL, aqURL, stURL, swpcURL string) *Provider {
	t.Helper()
	return New(&config.Config{
		WorkDir:              t.TempDir(),
		WeatherOpenMeteoURL:  omURL,
		WeatherAirQualityURL: aqURL,
		WeatherSevenTimerURL: stURL,
		WeatherSWPCURL:       swpcURL,
		WeatherGridSize:      4,
		WeatherGridRadiusDeg: 2,
		WeatherCacheTTLMin:   30,
	})
}

func TestForecast_AssemblesAllFeeds(t *testing.T) {
	om := fakeOpenMeteo(t)
	aq := fakeJSON(t, `{"hourly":{"time":["2026-06-30T20:00","2026-06-30T21:00","2026-06-30T22:00"],"aerosol_optical_depth":[0.08,0.5,0.05]}}`)
	st := fakeJSON(t, `{"init":"2026063018","dataseries":[{"timepoint":2,"seeing":3,"transparency":2},{"timepoint":5,"seeing":6,"transparency":4}]}`)
	swpc := fakeJSON(t, `[["time_tag","Kp","a","n"],["2026-06-30 18:00:00","2.00","5","8"],["2026-06-30 21:00:00","3.00","6","8"]]`)
	p := testProvider(t, om.URL, aq.URL, st.URL, swpc.URL)

	f, warn := p.Forecast(context.Background(), 48.86, 2.35)
	require.Empty(t, warn)
	require.Len(t, f.Hours, 3)

	assert.InDelta(t, 10, f.Hours[0].CloudPct, 0.01)
	assert.InDelta(t, 80, f.Hours[1].CloudPct, 0.01)
	assert.InDelta(t, 0.85, f.Hours[0].SeeingArcsec, 0.01, "7Timer seeing index 3 → ~0.85\"")
	assert.InDelta(t, 0.875, f.Hours[0].Transparency, 0.01, "7Timer transparency index 2")
	assert.InDelta(t, 0.08, f.Hours[0].AOD, 0.001)
	assert.Equal(t, "low", f.Hours[0].DewRisk, "spread 7°C")
	assert.Equal(t, "high", f.Hours[1].DewRisk, "spread 2°C")
	assert.Less(t, f.Hours[1].Verdict, f.Hours[0].Verdict, "80% cloud scores worse than 10%")

	require.NotNil(t, f.Kp)
	assert.InDelta(t, 3, f.Kp.Now, 0.01)
	assert.Subset(t, f.Sources, []string{"Open-Meteo", "Open-Meteo Air Quality", "7Timer! ASTRO", "NOAA SWPC"})
}

func TestForecast_CachesSecondCall(t *testing.T) {
	om := fakeOpenMeteo(t)
	p := testProvider(t, om.URL, "http://127.0.0.1:0", "http://127.0.0.1:0", "http://127.0.0.1:0")

	first, _ := p.Forecast(context.Background(), 10, 20)
	second, _ := p.Forecast(context.Background(), 10, 20)
	require.NotEmpty(t, first.Hours)
	assert.Equal(t, first.IssuedMs, second.IssuedMs, "second call must be served from cache")
}

func TestForecast_SoftFailsWhenBackboneDown(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)
	p := testProvider(t, down.URL, down.URL, down.URL, down.URL)

	f, warn := p.Forecast(context.Background(), 48.86, 2.35)
	assert.NotEmpty(t, warn, "a degraded result must carry a warning")
	assert.Empty(t, f.Hours, "no backbone feed → no timeline, but no panic")
}

func TestGrid_ShapeAndCells(t *testing.T) {
	om := fakeOpenMeteo(t)
	p := testProvider(t, om.URL, "", "", "")

	g, warn := p.Grid(context.Background(), 48.86, 2.35, 0, []string{"clouds"})
	require.Empty(t, warn)
	assert.Equal(t, 4, g.Nx)
	assert.Equal(t, 4, g.Ny)
	require.Len(t, g.Timesteps, 3)
	frames := g.Layers["clouds"]
	require.Len(t, frames, 3, "one frame per timestep")
	require.Len(t, frames[0], 16, "nx*ny cells per frame")
	assert.InDelta(t, 10, frames[0][0], 0.01, "frame 0 cloud cover")
	assert.InDelta(t, 80, frames[1][0], 0.01, "frame 1 cloud cover")

	// A larger viewport radius widens the sampled box, so the overlay follows a zoomed-out map.
	narrow, _ := p.Grid(context.Background(), 48.86, 2.35, 1, []string{"clouds"})
	wide, _ := p.Grid(context.Background(), 48.86, 2.35, 20, []string{"clouds"})
	assert.Greater(t, wide.BBox[2]-wide.BBox[0], narrow.BBox[2]-narrow.BBox[0], "radius widens the bbox")
}

func TestGrid_CloudsExpandToBands(t *testing.T) {
	om := fakeOpenMeteo(t)
	p := testProvider(t, om.URL, "", "", "")

	g, warn := p.Grid(context.Background(), 48.86, 2.35, 0, []string{"clouds"})
	require.Empty(t, warn)
	require.Len(t, g.Layers, 4, "\"clouds\" expands to total + low/mid/high bands")
	for layer, want := range map[string]float64{
		"clouds": 10, "clouds_low": 5, "clouds_mid": 3, "clouds_high": 8,
	} {
		frames := g.Layers[layer]
		require.Len(t, frames, 3, layer)
		require.Len(t, frames[0], 16, layer)
		assert.InDelta(t, want, frames[0][0], 0.01, layer)
	}
}

// TestGrid_ChunksLargeGrids: a 32×32 grid (1024 coords) must arrive as several chunked GETs and be
// reassembled cell-for-cell in request order. The fake echoes each coordinate's own lat·100+lon as its
// cloud value, so a chunk (or cell) landing out of order shows up as a value mismatch — deterministic
// even though the chunks are fetched concurrently.
func TestGrid_ChunksLargeGrids(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		lats := strings.Split(r.URL.Query().Get("latitude"), ",")
		lons := strings.Split(r.URL.Query().Get("longitude"), ",")
		require.Equal(t, len(lats), len(lons))
		require.LessOrEqual(t, len(lats), 401, "a chunk must stay within the URL budget (+1 for a folded tail)")
		objs := make([]string, len(lats))
		for i := range lats {
			la, err := strconv.ParseFloat(lats[i], 64)
			require.NoError(t, err)
			lo, err := strconv.ParseFloat(lons[i], 64)
			require.NoError(t, err)
			objs[i] = fmt.Sprintf(
				`{"latitude":%s,"longitude":%s,"hourly":{"time":["2026-06-30T20:00"],"cloud_cover":[%g]}}`,
				lats[i], lons[i], la*100+lo)
		}
		_, _ = io.WriteString(w, "["+strings.Join(objs, ",")+"]")
	}))
	t.Cleanup(s.Close)
	p := New(&config.Config{
		WorkDir:              t.TempDir(),
		WeatherOpenMeteoURL:  s.URL,
		WeatherGridSize:      32,
		WeatherGridRadiusDeg: 2,
		WeatherCacheTTLMin:   30,
	})

	g, warn := p.Grid(context.Background(), 48.86, 2.35, 0, []string{"clouds"})
	require.Empty(t, warn)
	assert.Greater(t, requests.Load(), int32(1), "1024 coords cannot fit one GET")
	require.Len(t, g.Layers["clouds"], 1, "one frame per timestep")
	frame := g.Layers["clouds"][0]
	require.Len(t, frame, 1024)

	// Recompute each cell's requested coordinate (the provider's grid maths + joinFloats' 3-decimal
	// trim) and check the fake's echo landed in that exact cell.
	west, east, south, north := 2.35-2.0, 2.35+2.0, 48.86-2.0, 48.86+2.0
	for j := 0; j < 32; j++ {
		la := parse3(t, north-(north-south)*float64(j)/31)
		for i := 0; i < 32; i++ {
			lo := parse3(t, west+(east-west)*float64(i)/31)
			assert.InDelta(t, la*100+lo, float64(frame[j*32+i]), 0.005, "cell (%d,%d)", i, j)
		}
	}
}

// parse3 mirrors joinFloats' 3-decimal formatting plus the fake server's ParseFloat round-trip.
func parse3(t *testing.T, f float64) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 3, 64), 64)
	require.NoError(t, err)
	return v
}

func TestExpandGridLayers(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"clouds expands to bands", []string{"clouds"},
			[]string{"clouds", "clouds_low", "clouds_mid", "clouds_high"}},
		{"other layers pass through", []string{"humidity", "precip"},
			[]string{"humidity", "precip"}},
		{"mixed keeps request order", []string{"humidity", "clouds", "precip"},
			[]string{"humidity", "clouds", "clouds_low", "clouds_mid", "clouds_high", "precip"}},
		{"dedup keeps first position", []string{"clouds_low", "clouds"},
			[]string{"clouds_low", "clouds", "clouds_mid", "clouds_high"}},
		{"already expanded is unchanged", []string{"clouds", "clouds_low", "clouds_mid", "clouds_high"},
			[]string{"clouds", "clouds_low", "clouds_mid", "clouds_high"}},
		{"empty", nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, expandGridLayers(tt.in))
		})
	}
}

func TestGridChunks(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want []gridChunk
	}{
		{"fits one chunk", 400, []gridChunk{{0, 400}}},
		{"exact multiple", 800, []gridChunk{{0, 400}, {400, 800}}},
		{"default 32x32 grid", 1024, []gridChunk{{0, 400}, {400, 800}, {800, 1024}}},
		{"size-1 tail folds into the previous chunk", 401, []gridChunk{{0, 401}}},
		{"size-2 tail stays its own chunk", 402, []gridChunk{{0, 400}, {400, 402}}},
		{"single point stays alone", 1, []gridChunk{{0, 1}}},
		{"empty", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, gridChunks(tt.n, 400))
		})
	}
}
