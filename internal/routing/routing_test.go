package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

func TestDriveMatrixDisabled(t *testing.T) {
	p := New(&config.Config{WorkDir: t.TempDir(), RoutingURL: ""})
	got, warn := p.DriveMatrix(context.Background(), 48.85, 2.35, []float64{48.9}, []float64{2.4})
	require.Len(t, got, 1)
	assert.False(t, got[0].OK)
	assert.Empty(t, warn) // disabled is not an error
}

func TestDriveMatrixParsesOSRM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// distances[0] / durations[0] = source→[source, dest0, dest1]; dest1 is unroutable (null).
		_, _ = w.Write([]byte(`{"code":"Ok","distances":[[0,7900.5,null]],"durations":[[0,600,null]]}`))
	}))
	defer srv.Close()

	p := New(&config.Config{WorkDir: t.TempDir(), RoutingURL: srv.URL})
	got, warn := p.DriveMatrix(context.Background(), 48.85, 2.35, []float64{48.9, 49.0}, []float64{2.4, 2.5})
	assert.Empty(t, warn)
	require.Len(t, got, 2)
	assert.True(t, got[0].OK)
	assert.InDelta(t, 7.9005, got[0].DistanceKm, 1e-6)
	assert.InDelta(t, 10.0, got[0].DurationMin, 1e-6) // 600 s / 60
	assert.False(t, got[1].OK)                        // null → unroutable
}

func TestDriveMatrixSoftFailsOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := New(&config.Config{WorkDir: t.TempDir(), RoutingURL: srv.URL})
	got, warn := p.DriveMatrix(context.Background(), 48.85, 2.35, []float64{48.9}, []float64{2.4})
	require.Len(t, got, 1)
	assert.False(t, got[0].OK)
	assert.NotEmpty(t, warn) // soft-fail → warning, no panic/error
}
