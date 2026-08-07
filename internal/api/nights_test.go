package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/darksky"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/weather"
)

func nightsServer(nights int) *Server {
	return &Server{cfg: &config.Config{LatDeg: 43.6, LonDeg: 5.1, Timezone: "Europe/Paris", DarkSkyNights: nights}}
}

func getNights(t *testing.T, s *Server, query string) nightsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/nights"+query, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var got nightsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

func TestSkyNights_ListsOneEntryPerNight(t *testing.T) {
	got := getNights(t, nightsServer(7), "?lat=43.6&lon=5.1")

	require.Len(t, got.Nights, 7)
	assert.Equal(t, "Europe/Paris", got.Timezone)
	for i, n := range got.Nights {
		assert.Equal(t, i, n.Index)
		assert.Greater(t, n.EndMs, n.StartMs, "night %d", i)
		assert.Greater(t, n.DarkHours, 0.0, "night %d", i)
		assert.NotEmpty(t, n.MoonPhase, "night %d", i)
		assert.NotEmpty(t, n.DateLocal, "night %d", i)
		assert.LessOrEqual(t, n.MoonUpHours, n.DarkHours+1e-6, "night %d", i)
		if i > 0 {
			gap := time.UnixMilli(n.StartMs).Sub(time.UnixMilli(got.Nights[i-1].StartMs))
			assert.Greater(t, gap, 20*time.Hour, "night %d must be a different night", i)
			assert.Less(t, gap, 28*time.Hour, "night %d must be the very next one", i)
		}
	}
}

// Beyond a few days a cloud forecast is close to climatology. The list still offers those nights, but
// flags them so the UI can say so rather than presenting a guess as a plan.
func TestSkyNights_FlagsTheLowConfidenceTail(t *testing.T) {
	got := getNights(t, nightsServer(7), "")

	require.Len(t, got.Nights, 7)
	for i, n := range got.Nights {
		assert.Equal(t, i >= skyNightsConfidentDays, n.LowConfidence, "night %d", i)
	}
}

func TestSkyNights_DaysClampedToTheConfiguredHorizon(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "default is the configured count", query: "", want: 5},
		{name: "fewer on request", query: "?days=2", want: 2},
		{name: "more is clamped", query: "?days=99", want: 5},
		{name: "zero is clamped up", query: "?days=0", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Len(t, getNights(t, nightsServer(5), tt.query).Nights, tt.want)
		})
	}
}

func TestSkyNights_RejectsAnImpossibleLocation(t *testing.T) {
	rec := httptest.NewRecorder()
	nightsServer(7).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/nights?lat=120&lon=5", nil))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// A weather provider caps the picker: offering nights nothing can forecast would be a dead control.
func TestNightCount_NeverExceedsTheForecastHorizon(t *testing.T) {
	s := nightsServer(7)
	s.weather = weather.New(&config.Config{WorkDir: t.TempDir(), WeatherForecastDays: 3})

	assert.Equal(t, 3, s.nightCount())
}

// recordingFinder captures the Query the handler built, so the wire contract is checked at the edge.
type recordingScanner struct{ q darksky.Query }

func (r *recordingScanner) ScanArea(_ context.Context, _ lightpollution.Bbox, _, _ int) []lightpollution.Cell {
	return []lightpollution.Cell{{Lat: 44, Lon: 4, SQM: 21.7, Bortle: 2}}
}

func TestDarkSites_WeatherParams(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantWeight float64
		wantNight  bool
	}{
		{name: "weather is on by default", query: "", wantWeight: 0.3, wantNight: true},
		{name: "explicitly off", query: "&weather=0", wantWeight: 0, wantNight: false},
		{name: "slider weight honoured", query: "&weather=1&weather_weight=0.6", wantWeight: 0.6, wantNight: true},
		{name: "weight clamped to the ceiling", query: "&weather=1&weather_weight=5", wantWeight: 0.8, wantNight: true},
		{name: "a later night", query: "&night=3", wantWeight: 0.3, wantNight: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{
				cfg: &config.Config{LatDeg: 48, LonDeg: 2, DarkSkyNights: 7},
				darksky: darksky.New(&recordingScanner{}, nil, 4000, 10,
					darksky.WithWeather(stubNightScanner{}, 16)),
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/sky/darksites?min_lat=43&min_lon=3&max_lat=45&max_lon=5"+tt.query, nil))
			require.Equal(t, http.StatusOK, rec.Code)

			var got darksky.Result
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tt.wantWeight, got.WeatherWeight)
			assert.Equal(t, tt.wantNight, got.Night != nil)
		})
	}
}

// A night index past the picker's range must be clamped, not rejected: a stale bookmark should still
// return a usable answer for a night that exists.
func TestDarkSites_NightIndexClampedToTheHorizon(t *testing.T) {
	s := &Server{
		cfg: &config.Config{LatDeg: 48, LonDeg: 2, DarkSkyNights: 3},
		darksky: darksky.New(&recordingScanner{}, nil, 4000, 10,
			darksky.WithWeather(stubNightScanner{}, 16)),
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/sky/darksites?min_lat=43&min_lon=3&max_lat=45&max_lon=5&night=99", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var got darksky.Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.Night)
	assert.Equal(t, 2, got.Night.Index, "clamped to the last night the horizon covers")
}

type stubNightScanner struct{}

func (stubNightScanner) NightScan(_ context.Context, pts []weather.Point, startMs, endMs int64, _ weather.NightOpts) ([]weather.NightOutlook, string) {
	out := make([]weather.NightOutlook, len(pts))
	for i := range out {
		out[i] = weather.NightOutlook{StartMs: startMs, EndMs: endMs, SampleHours: 8, Score: 70}
	}
	return out, ""
}

func (stubNightScanner) NightConfidence(_ context.Context, _, _ float64, _, _ int64) *weather.Confidence {
	return nil
}
