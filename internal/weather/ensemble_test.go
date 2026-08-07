package weather

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// fakeEnsemble serves an ensemble block: the control run plus one series per extra member, each with
// the given constant cloud cover.
func fakeEnsemble(t *testing.T, memberClouds []float64) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		times := hourRange(r.URL.Query().Get("start_hour"), r.URL.Query().Get("end_hour"))
		parts := []string{`"time":["` + strings.Join(times, `","`) + `"]`}
		for i, cloud := range memberClouds {
			name := "cloud_cover"
			if i > 0 {
				name = fmt.Sprintf("cloud_cover_member%02d", i)
			}
			vals := make([]string, len(times))
			for j := range vals {
				vals[j] = fmt.Sprintf("%g", cloud)
			}
			parts = append(parts, fmt.Sprintf(`"%s":[%s]`, name, strings.Join(vals, ",")))
		}
		_, _ = io.WriteString(w, `{"hourly":{`+strings.Join(parts, ",")+`}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func ensembleProvider(t *testing.T, ensembleURL string) *Provider {
	t.Helper()
	return New(&config.Config{
		WorkDir:              t.TempDir(),
		WeatherEnsembleURL:   ensembleURL,
		WeatherEnsembleModel: "icon_eu",
		WeatherCacheTTLMin:   30,
		WeatherForecastDays:  7,
	})
}

func TestNightConfidence_CountsTheClearMembers(t *testing.T) {
	// 6 of 10 members forecast a clear night.
	srv, _ := fakeEnsemble(t, []float64{5, 10, 15, 20, 25, 30, 60, 70, 80, 90})
	p := ensembleProvider(t, srv.URL)
	startMs, endMs := testNight()

	c := p.NightConfidence(context.Background(), 44, 5, startMs, endMs)

	require.NotNil(t, c)
	assert.Equal(t, 10, c.Members)
	assert.Equal(t, 6, c.ClearMembers)
	assert.InDelta(t, 0.6, c.Agreement, 0.001)
	assert.InDelta(t, 40.5, c.MeanCloudPct, 0.1)
	assert.Greater(t, c.SpreadPct, 0.0)
	assert.Equal(t, "icon_eu", c.Model)
}

func TestNightConfidence_UnanimityHasNoSpread(t *testing.T) {
	srv, _ := fakeEnsemble(t, []float64{4, 4, 4, 4, 4})
	p := ensembleProvider(t, srv.URL)
	startMs, endMs := testNight()

	c := p.NightConfidence(context.Background(), 44, 5, startMs, endMs)

	require.NotNil(t, c)
	assert.Equal(t, 1.0, c.Agreement)
	assert.Zero(t, c.SpreadPct)
}

func TestNightConfidence_CachesPerNight(t *testing.T) {
	srv, calls := fakeEnsemble(t, []float64{5, 10, 80, 90})
	p := ensembleProvider(t, srv.URL)
	startMs, endMs := testNight()

	require.NotNil(t, p.NightConfidence(context.Background(), 44, 5, startMs, endMs))
	require.NotNil(t, p.NightConfidence(context.Background(), 44, 5, startMs, endMs))

	assert.Equal(t, int32(1), calls.Load())
}

// Confidence is a bonus. Anything that stops it working must leave the ranking untouched and silent —
// never a warning banner about a figure nobody asked for.
func TestNightConfidence_UnavailableIsNilNotAnError(t *testing.T) {
	single, _ := fakeEnsemble(t, []float64{20}) // a deterministic run carries no agreement
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(dead.Close)
	startMs, endMs := testNight()
	far := time.Now().UTC().AddDate(0, 0, 30)

	tests := []struct {
		name           string
		url            string
		startMs, endMs int64
	}{
		{name: "feature disabled", url: "", startMs: startMs, endMs: endMs},
		{name: "upstream down", url: dead.URL, startMs: startMs, endMs: endMs},
		{name: "not an ensemble", url: single.URL, startMs: startMs, endMs: endMs},
		{name: "beyond horizon", url: single.URL, startMs: far.UnixMilli(), endMs: far.Add(8 * time.Hour).UnixMilli()},
		{name: "empty window", url: single.URL, startMs: endMs, endMs: startMs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ensembleProvider(t, tt.url)

			assert.Nil(t, p.NightConfidence(context.Background(), 44, 5, tt.startMs, tt.endMs))
		})
	}
}
