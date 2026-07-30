package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// The engine's window onto the device server. Everything under /api/device/* is reverse-proxied to
// the separate `astrostack device` process, so the browser talks to one origin and knows nothing
// about the split. The gzip wrapper only compresses application/json, so the live view's binary
// frames and its SSE stream pass through untouched.

// deviceProxyTimeout bounds ordinary device calls. It is deliberately absent for the streaming
// endpoints, which are meant to stay open (a 10-minute exposure or a live SSE feed).
const deviceProxyTimeout = 60 * time.Second

// deviceProxy lazily builds a reverse proxy to the configured device address.
type deviceProxy struct {
	addr string

	once  sync.Once
	proxy *httputil.ReverseProxy
	err   error
}

func newDeviceProxy(addr string) *deviceProxy { return &deviceProxy{addr: addr} }

func (d *deviceProxy) handler() (*httputil.ReverseProxy, error) {
	d.once.Do(func() {
		target, err := url.Parse("http://" + d.addr)
		if err != nil {
			d.err = fmt.Errorf("invalid ASTRO_DEVICE_ADDR %q: %w", d.addr, err)
			return
		}
		p := httputil.NewSingleHostReverseProxy(target)
		// FlushInterval −1 flushes as bytes arrive, which is what keeps the SSE stats stream and
		// the live frame endpoint responsive instead of buffered.
		p.FlushInterval = -1
		p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": deviceUnavailableMessage(err, d.addr),
				"code":  "device_server_unavailable",
			})
		}
		d.proxy = p
	})
	return d.proxy, d.err
}

// deviceUnavailableMessage turns a connection failure into the actionable sentence the UI shows:
// the device server is a separate process the user starts, so "not running" is a normal state.
func deviceUnavailableMessage(err error, addr string) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) || strings.Contains(err.Error(), "connection refused") {
		// The hard case to diagnose: a CONTAINERIZED engine dialing loopback is looking at itself, not
		// at the host. The device server can be running perfectly on the Mac and still be invisible.
		// Telling the user to "start it with `just device`" when they already have is worse than
		// useless — it sends them looking in the wrong place entirely.
		if inContainer() && isLoopback(addr) {
			return "this engine runs in a container, so " + addr + " is the container itself, not your " +
				"host — the device server cannot be reached there even when it is running. Set " +
				"ASTRO_DEVICE_ADDR=host.docker.internal:8084 for the engine service and restart it, " +
				"or run the engine on the host with `just dev`"
		}
		return "the device server is not running — start it with `just device` (or `just device-x86` " +
			"for real ZWO hardware on an Apple-Silicon Mac)"
	}
	return "device server error: " + err.Error()
}

// inContainer reports whether this process is running inside a container. Docker creates /.dockerenv;
// the cgroup check covers the podman/containerd cases.
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	txt := string(b)
	return strings.Contains(txt, "docker") || strings.Contains(txt, "containerd") ||
		strings.Contains(txt, "libpod")
}

// isLoopback reports whether a host:port targets this machine's own loopback.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// deviceRequest proxies one call. The /api/device prefix is stripped so the device server sees its
// own flat routes.
func (s *Server) deviceRequest(w http.ResponseWriter, r *http.Request) {
	proxy, err := s.devices.handler()
	if err != nil {
		serverError(w, err)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/device")
	if r2.URL.Path == "" {
		r2.URL.Path = "/"
	}
	// Streaming endpoints must not be cut short by a timeout; everything else gets one.
	if !isDeviceStream(r2.URL.Path) {
		ctx, cancel := context.WithTimeout(r.Context(), deviceProxyTimeout)
		defer cancel()
		r2 = r2.WithContext(ctx)
	}
	proxy.ServeHTTP(w, r2)
}

func isDeviceStream(path string) bool {
	return strings.HasPrefix(path, "/live/events") || strings.HasPrefix(path, "/live/frame")
}

// deviceHealth is a short-timeout probe used by the environment report, so a stopped device server
// shows up as "not running" rather than making the whole status page hang.
func (s *Server) deviceHealth(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.cfg.DeviceAddr+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New(deviceUnavailableMessage(err, s.cfg.DeviceAddr))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device server returned %s", resp.Status)
	}
	var out map[string]any
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// deviceStatus is the engine-side status endpoint: it answers even when the device server is down,
// which is what lets the UI explain the situation instead of showing a network error.
func (s *Server) deviceStatus(w http.ResponseWriter, r *http.Request) {
	health, err := s.deviceHealth(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"running": false,
			"addr":    s.cfg.DeviceAddr,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": true,
		"addr":    s.cfg.DeviceAddr,
		"health":  health,
	})
}
