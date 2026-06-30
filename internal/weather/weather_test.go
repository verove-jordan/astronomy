package weather

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// omHourlyBody is a small 3-hour Open-Meteo hourly block reused for the point and the grid fakes.
const omHourlyBody = `"time":["2026-06-30T20:00","2026-06-30T21:00","2026-06-30T22:00"],` +
	`"cloud_cover":[10,80,5],"cloud_cover_low":[5,40,2],"relative_humidity_2m":[60,85,55],` +
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

	g, warn := p.Grid(context.Background(), 48.86, 2.35, []string{"clouds"})
	require.Empty(t, warn)
	assert.Equal(t, 4, g.Nx)
	assert.Equal(t, 4, g.Ny)
	require.Len(t, g.Timesteps, 3)
	frames := g.Layers["clouds"]
	require.Len(t, frames, 3, "one frame per timestep")
	require.Len(t, frames[0], 16, "nx*ny cells per frame")
	assert.InDelta(t, 10, frames[0][0], 0.01, "frame 0 cloud cover")
	assert.InDelta(t, 80, frames[1][0], 0.01, "frame 1 cloud cover")
}
