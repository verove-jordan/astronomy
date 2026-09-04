package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device/nexstar"
)

// fakeSidecar serves the two endpoints findMountSidecar consults.
func fakeSidecar(t *testing.T, mountConnected bool, driver, path string, routes map[string]http.HandlerFunc) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":        true,
			"connected": map[string]bool{"mount": mountConnected},
		})
	})
	mux.HandleFunc("GET /mount", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connected": mountConnected,
			"mount":     map[string]any{"name": "Advanced VX", "driver": driver},
			"link":      map[string]any{"path": path},
		})
	})
	for pattern, h := range routes {
		mux.HandleFunc(pattern, h)
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

// Which process reads the mount is not a detail: macOS gives the serial port to one process, so
// getting this wrong is the difference between an audit and "Serial port busy".
func TestFindMountSidecar(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		driver    string
		want      bool
	}{
		{"a real hand controller is the one to ask", true, "nexstar", true},
		{"no mount attached means the port is free", false, "nexstar", false},
		{"a simulated mount holds no port, so open the real one", true, "sim", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := fakeSidecar(t, tt.connected, tt.driver, "/dev/cu.usbserial-11120", nil)
			got := findMountSidecar(context.Background(), addr)
			if !tt.want {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, "/dev/cu.usbserial-11120", got.path)
			assert.Contains(t, got.describe(), "Advanced VX")
		})
	}
}

// A device server that is not running is the ordinary case on a bench, and must read as "open the
// port yourself", never as a failure.
func TestFindMountSidecar_StoppedServerIsNotAnError(t *testing.T) {
	assert.Nil(t, findMountSidecar(context.Background(), "127.0.0.1:1"))
	assert.Nil(t, findMountSidecar(context.Background(), ""))
}

func TestMountSidecar_AuditReturnsTheReport(t *testing.T) {
	addr := fakeSidecar(t, true, "nexstar", "/dev/cu.usbserial-11120", map[string]http.HandlerFunc{
		"GET /mount/audit": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"connected": true,
				"audit": nexstar.AuditReport{
					Port:     "/dev/cu.usbserial-11120",
					Identity: nexstar.IdentityAudit{Model: "Advanced VX", Firmware: "5.31"},
					PEC:      nexstar.PECAudit{Supported: true, Read: true, Bins: 88, AllZero: true},
				},
			})
		},
	})
	sc := findMountSidecar(context.Background(), addr)
	require.NotNil(t, sc)

	r, err := sc.audit(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Advanced VX", r.Identity.Model)
	assert.True(t, r.PEC.AllZero)
	assert.Contains(t, r.String(), "ALL ZERO")
}

// The server's own sentence is what the user needs; "the device server answered 409 Conflict" is not.
func TestMountSidecar_AuditSurfacesTheServersError(t *testing.T) {
	addr := fakeSidecar(t, true, "nexstar", "/dev/cu.usbserial-11120", map[string]http.HandlerFunc{
		"GET /mount/audit": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "mount is not connected", "code": "not_connected",
			})
		},
	})
	sc := findMountSidecar(context.Background(), addr)
	require.NotNil(t, sc)

	_, err := sc.audit(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mount is not connected")
}

// A dry run must stay a dry run across the wire: `apply` is the inverse of DryRun, and getting that
// backwards would write to somebody's mount when they asked for a preview.
func TestMountSidecar_ResetSendsApplyAsTheInverseOfDryRun(t *testing.T) {
	var got map[string]any
	addr := fakeSidecar(t, true, "nexstar", "/dev/cu.usbserial-11120", map[string]http.HandlerFunc{
		"POST /mount/reset": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_ = json.NewEncoder(w).Encode(nexstar.RestoreResult{DryRun: true})
		},
	})
	sc := findMountSidecar(context.Background(), addr)
	require.NotNil(t, sc)

	_, err := sc.reset(context.Background(), nexstar.RestoreOptions{DryRun: true, PEC: true})
	require.NoError(t, err)
	assert.Equal(t, false, got["apply"], "a dry run must never ask the server to apply")
	assert.Equal(t, true, got["pec"])
}
