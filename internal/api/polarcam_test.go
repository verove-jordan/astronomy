package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/polaralign"
)

func polarAPIServer() *Server {
	return &Server{cfg: &config.Config{
		LatDeg: 48.8566, LonDeg: 2.3522,
		FocalLenMM: 740, PixelSizeUm: 3.8,
		DeviceAddr: "127.0.0.1:1", // nothing listening: these tests never reach the hardware
	}}
}

// Without Siril there is nothing to measure with, and the panel has to be told that BEFORE it asks the
// user to point a telescope somewhere and start turning it.
func TestStartPolar_SaysSoWhenSirilIsMissing(t *testing.T) {
	s := polarAPIServer()
	rec := httptest.NewRecorder()
	s.startPolar(rec, httptest.NewRequest(http.MethodPost, "/api/capture/polar/start", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "solver_unavailable", body["code"])
	assert.Contains(t, body["error"], "Siril")
}

// Steps taken out of order are the client's mistake, so they get a status the client can branch on
// rather than a state that quietly looks like success.
func TestPolarSteps_RejectOutOfOrderRequests(t *testing.T) {
	s := polarAPIServer()
	for _, c := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"next", s.nextPolar},
		{"adjust", s.adjustPolar},
		{"refresh", s.refreshPolar},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c.handler(rec, httptest.NewRequest(http.MethodPost, "/api/capture/polar/"+c.name, nil))

			require.Equal(t, http.StatusConflict, rec.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, "polar_not_running", body["code"])
			assert.NotNil(t, body["state"], "the panel still needs to know where the session is")
		})
	}
}

// The panel renders straight from the snapshot, so an idle server has to answer with a usable one
// rather than a null the frontend has to special-case.
func TestPolarStatus_IsUsableBeforeAnythingHasHappened(t *testing.T) {
	s := polarAPIServer()
	rec := httptest.NewRecorder()
	s.polarStatus(rec, httptest.NewRequest(http.MethodGet, "/api/capture/polar", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		State struct {
			Phase      string  `json:"phase"`
			Points     int     `json:"points"`
			StepArcDeg float64 `json:"step_arc_deg"`
		} `json:"state"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "idle", body.State.Phase)
	assert.GreaterOrEqual(t, body.State.Points, 3)
	assert.Positive(t, body.State.StepArcDeg, "the panel tells the user how far to turn")
}

// Latitude and longitude follow the same rule as everywhere else: use what the browser picked, fall
// back to the configured site, and read a zeroed pair as "not sent" rather than as the Gulf of Guinea.
func TestPolarSite_FallsBackToTheConfiguredSite(t *testing.T) {
	s := polarAPIServer()

	assert.Equal(t, polaralign.Site{LatDeg: 48.8566, LonDeg: 2.3522}, s.polarSite(polarStartBody{}))
	assert.Equal(t, polaralign.Site{LatDeg: -33.87, LonDeg: 151.2},
		s.polarSite(polarStartBody{LatDeg: -33.87, LonDeg: 151.2}))
	assert.Equal(t, polaralign.Site{LatDeg: 48.8566, LonDeg: 2.3522},
		s.polarSite(polarStartBody{LatDeg: 999, LonDeg: 999}), "out of range is not a site")
}

// Every route has to be reachable; a handler nobody registered is a feature nobody can use. Note that
// GET /api/capture/polar and POST /api/capture/polar/start are neighbouring patterns, which is exactly
// the sort of pair a mux quietly gets wrong.
func TestPolarRoutes_AreRegistered(t *testing.T) {
	handler := polarAPIServer().Handler()

	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/capture/polar/start"},
		{http.MethodPost, "/api/capture/polar/next"},
		{http.MethodPost, "/api/capture/polar/adjust"},
		{http.MethodPost, "/api/capture/polar/refresh"},
		{http.MethodPost, "/api/capture/polar/stop"},
		{http.MethodGet, "/api/capture/polar"},
		{http.MethodGet, "/api/capture/polar/events"},
	} {
		// The stream runs until its request is cancelled, so it is handed one that already is.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil).WithContext(ctx))
		assert.NotEqual(t, http.StatusNotFound, rec.Code, "%s %s is not routed", c.method, c.path)
		assert.NotEqual(t, http.StatusMethodNotAllowed, rec.Code, "%s %s", c.method, c.path)
	}
}

// A reconnecting panel must never see a blank screen, so the stream leads with a snapshot rather than
// waiting for something to happen.
func TestPolarEvents_LeadsWithASnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	polarAPIServer().polarEvents(rec,
		httptest.NewRequest(http.MethodGet, "/api/capture/polar/events", nil).WithContext(ctx))

	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"phase":"idle"`)
}
