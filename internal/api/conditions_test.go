package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/capture"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/skylog"
)

// Which site a night's conditions are attributed to. This has to be pinned: the engine's configured
// location is right at home and wrong on every trip to a dark sky, so the browser's picked site wins
// — but only when it actually sent one.
func TestCaptureSite(t *testing.T) {
	s := &Server{cfg: &config.Config{LatDeg: 48.8566, LonDeg: 2.3522, ElevationM: 35}}

	cases := []struct {
		name     string
		body     captureStartBody
		wantLat  float64
		wantLon  float64
		wantElev float64
	}{
		{
			name:     "the browser's site wins",
			body:     captureStartBody{LatDeg: 44.1, LonDeg: 5.5},
			wantLat:  44.1,
			wantLon:  5.5,
			wantElev: 35,
		},
		{
			name:     "an older client that sends nothing falls back to the configured site",
			body:     captureStartBody{},
			wantLat:  48.8566,
			wantLon:  2.3522,
			wantElev: 35,
		},
		{
			name:     "a nonsense latitude falls back rather than being recorded",
			body:     captureStartBody{LatDeg: 200, LonDeg: 5.5},
			wantLat:  48.8566,
			wantLon:  2.3522,
			wantElev: 35,
		},
		{
			name:     "a nonsense longitude falls back too",
			body:     captureStartBody{LatDeg: 44.1, LonDeg: -400},
			wantLat:  48.8566,
			wantLon:  2.3522,
			wantElev: 35,
		},
		{
			name:     "a site on the equator or the prime meridian is still a real site",
			body:     captureStartBody{LatDeg: 0, LonDeg: -18.1},
			wantLat:  0,
			wantLon:  -18.1,
			wantElev: 35,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.captureSite(tc.body)
			assert.Equal(t, tc.wantLat, got.Lat)
			assert.Equal(t, tc.wantLon, got.Lon)
			assert.Equal(t, tc.wantElev, got.ElevationM, "elevation always comes from config")
		})
	}
}

// A session with no coordinates must not claim a Moon separation of zero degrees.
func TestCaptureTarget(t *testing.T) {
	t.Run("coordinates make the target valid", func(t *testing.T) {
		got := captureTarget(captureStartBody{RADeg: 10.68, DecDeg: 41.27})
		assert.Equal(t, skylog.Target{RADeg: 10.68, DecDeg: 41.27, Valid: true}, got)
	})

	t.Run("no coordinates leaves it invalid", func(t *testing.T) {
		assert.Equal(t, skylog.Target{}, captureTarget(captureStartBody{}))
	})

	t.Run("a declination alone is still a pointing", func(t *testing.T) {
		got := captureTarget(captureStartBody{DecDeg: 89.26}) // Polaris sits at RA ~2.5h, but 0 is legal
		assert.True(t, got.Valid)
	})
}

func TestIsTerminalCaptureStatus(t *testing.T) {
	cases := []struct {
		status capture.Status
		want   bool
	}{
		{capture.StatusCompleted, true},
		{capture.StatusAborted, true},
		{capture.StatusFailed, true},
		{capture.StatusRunning, false},
		{capture.StatusPaused, false},
		{capture.StatusIdle, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			assert.Equal(t, tc.want, isTerminalCaptureStatus(tc.status))
		})
	}
}

func TestConditionsInterval(t *testing.T) {
	t.Run("defaults to hourly", func(t *testing.T) {
		s := &Server{cfg: &config.Config{}}
		assert.Equal(t, skylog.DefaultInterval, s.conditionsInterval())
	})

	t.Run("a configured interval is honoured", func(t *testing.T) {
		s := &Server{cfg: &config.Config{ConditionsIntervalMin: 5}}
		assert.Equal(t, 5*time.Minute, s.conditionsInterval())
	})

	t.Run("a negative interval falls back rather than spinning", func(t *testing.T) {
		s := &Server{cfg: &config.Config{ConditionsIntervalMin: -3}}
		assert.Equal(t, skylog.DefaultInterval, s.conditionsInterval())
	})
}

// The sampler must be safe to build against a server with no providers at all — that is exactly the
// shape every handler test uses, and a panic here would mean a capture could fail because the
// logbook could not start.
func TestAttachConditionsLogger_NoProvidersIsSafe(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	assert.NotPanics(t, func() {
		s.attachConditionsLogger(1, skylog.Site{}, skylog.Target{})
	})
	_, ok := s.conditionsLog.Load().Stats()
	assert.False(t, ok, "nothing is recording")
}
