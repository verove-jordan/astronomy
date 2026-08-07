package devsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/nexstar"
)

// Supervision of the mount link: is it alive, is it still in step, and what does the UI get to see.
//
// The driver owns the mechanism — flush, resynchronise, reconnect, stop a runaway axis. This owns
// the CADENCE and the policy, which is the same split as pecSession ↔ nexstar/pec.go. It also owns
// the one piece of state the sidecar has to remember across restarts: which port the hand controller
// was on.

const (
	// heartbeatIdle is how long the link may be silent before it is asked to prove itself.
	//
	// It is an IDLE timer, not a ticker, and that distinction is the whole design. mount.go:70 exists
	// because a periodic-error run reads the worm several times a second and every command queues
	// behind one mutex on a 9600-baud link; a fixed-rate heartbeat would re-introduce exactly the
	// theft that comment prevents. While anything is working, its own traffic is the proof of life
	// and this costs nothing. Only real silence provokes a ping.
	heartbeatIdle = 5 * time.Second
	// heartbeatTick is how often idleness is examined — cheap, since it touches the port only when
	// the link has actually gone quiet.
	heartbeatTick = time.Second
	// mountEventInterval is the SSE cadence. One second is plenty for a slewing indicator and keeps
	// the panel far away from the port's own budget.
	mountEventInterval = time.Second
	// linkStateFile remembers the last hand controller that worked.
	linkStateFile = "device-link.json"
)

// linkReporter is the part of the NexStar driver the supervisor needs. It is an optional interface:
// the simulator does not implement it, and a simulated mount that reported serial-link health would
// be lying.
type linkReporter interface {
	Health() nexstar.LinkHealth
	IdleFor() time.Duration
	Ping(context.Context) error
}

// savedLink is what survives a restart of the device server.
type savedLink struct {
	Driver string `json:"driver"`
	Port   string `json:"port"`
	Model  string `json:"model,omitempty"`
	// SavedAt is a millisecond timestamp, per the house convention for stored times.
	SavedAt int64 `json:"saved_at"`
}

// mountLink is the supervisor.
type mountLink struct {
	srv *Server

	mu        sync.RWMutex
	suspended int
	stop      chan struct{}
	started   bool
}

func newMountLink(s *Server) *mountLink {
	return &mountLink{srv: s, stop: make(chan struct{})}
}

// start launches the heartbeat once a mount is connected. Lazily, because a device server that never
// connects anything should have no background work at all.
func (l *mountLink) start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started {
		return
	}
	l.started = true
	go l.heartbeat(l.stop)
}

func (l *mountLink) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started {
		close(l.stop)
		l.started = false
	}
}

// Suspend hands out a release function; while any are outstanding the heartbeat stays off the port.
// The idle timer already achieves this for a busy owner, but a run that pauses between measurements
// would otherwise look idle at exactly the wrong moment.
func (l *mountLink) Suspend() func() {
	l.mu.Lock()
	l.suspended++
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.suspended--
			l.mu.Unlock()
		})
	}
}

func (l *mountLink) isSuspended() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.suspended > 0
}

func (l *mountLink) heartbeat(stop <-chan struct{}) {
	t := time.NewTicker(heartbeatTick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if l.isSuspended() {
			continue
		}
		reporter, ok := l.srv.currentMount().(linkReporter)
		if !ok || reporter.IdleFor() < heartbeatIdle {
			continue
		}
		// A failure here needs no handling: the driver has already started its own reconnect, and the
		// counters it keeps are what the UI and the soak report read.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = reporter.Ping(ctx)
		cancel()
	}
}

// health returns the link's own report, or a minimal one for a driver that does not keep counters.
func (l *mountLink) health() (nexstar.LinkHealth, bool) {
	reporter, ok := l.srv.currentMount().(linkReporter)
	if !ok {
		return nexstar.LinkHealth{}, false
	}
	return reporter.Health(), true
}

// --- persistence ---------------------------------------------------------------------------------

func (l *mountLink) statePath() string {
	if l.srv.cfg == nil || l.srv.cfg.WorkDir == "" {
		return ""
	}
	return filepath.Join(l.srv.cfg.WorkDir, linkStateFile)
}

// remember records the link that just worked.
//
// A file rather than the app_settings table the filter slots use, because this process deliberately
// has no database — that separation is the reason the device server exists. One source of truth,
// owned by the process that has to act on it.
func (l *mountLink) remember(driver, port, model string) {
	path := l.statePath()
	if path == "" || port == "" {
		return
	}
	b, err := json.MarshalIndent(savedLink{
		Driver: driver, Port: port, Model: model, SavedAt: time.Now().UnixMilli(),
	}, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, b, 0o644)
}

func (l *mountLink) recall() (savedLink, bool) {
	path := l.statePath()
	if path == "" {
		return savedLink{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return savedLink{}, false
	}
	var s savedLink
	if err := json.Unmarshal(b, &s); err != nil || s.Port == "" {
		return savedLink{}, false
	}
	return s, true
}

// restore reconnects to the hand controller that worked last time, in the background.
//
// Non-fatal in every direction: a mount that is not plugged in tonight, or a port that has moved,
// must leave the device server running normally rather than failing to start. The UI can always
// connect by hand.
func (l *mountLink) restore() {
	saved, ok := l.recall()
	if !ok {
		return
	}
	go func() {
		l.srv.mu.Lock()
		if l.srv.mount != nil { // somebody connected first; leave them alone
			l.srv.mu.Unlock()
			return
		}
		mountPort = saved.Port
		mount, err := l.srv.openMount(saved.Driver)
		l.srv.mu.Unlock()
		if err != nil {
			return
		}
		if porter, ok := mount.(interface{ SetPort(string) }); ok {
			porter.SetPort(saved.Port)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := mount.Connect(ctx); err != nil {
			_ = mount.Close()
			return
		}
		l.srv.mu.Lock()
		if l.srv.mount == nil {
			l.srv.mount = mount
			l.srv.mu.Unlock()
			l.start()
			return
		}
		l.srv.mu.Unlock()
		_ = mount.Close()
	}()
}

// --- HTTP ----------------------------------------------------------------------------------------

// mountLinkStatus reports what is remembered, so the UI can show which hand controller it will
// reconnect to without opening the port to find out.
func (s *Server) mountLinkStatus(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{}
	if saved, ok := s.link.recall(); ok {
		out["saved"] = saved
	}
	if h, ok := s.link.health(); ok {
		out["link"] = h
	}
	writeJSON(w, http.StatusOK, out)
}

// mountDiagnose answers "why can this Mac not see the hand controller" from the device server, so
// the same diagnosis is available in the UI as on the command line.
func (s *Server) mountDiagnose(w http.ResponseWriter, r *http.Request) {
	// Probing opens ports exclusively, so it is refused while this process is itself holding one —
	// the probe would fail on our own port and report a busy device we are the cause of.
	probe := r.URL.Query().Get("probe") != "" && s.currentMount() == nil
	writeJSON(w, http.StatusOK, nexstar.Diagnose(r.Context(), probe))
}

// mountEvents streams state and link health, in the same shape as the live-view stream so the
// frontend's existing EventSource handling applies.
//
// Without this the mount panel only ever updated as a side effect of a button press: `slewing` never
// went back to false on its own, and a link that died at 2am looked healthy until somebody clicked
// something.
func (s *Server) mountEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	t := time.NewTicker(mountEventInterval)
	defer t.Stop()
	for {
		payload, err := json.Marshal(s.mountSnapshot(r.Context()))
		if err == nil {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

// mountSnapshot is what both GET /mount and the stream serve, so they can never disagree.
func (s *Server) mountSnapshot(ctx context.Context) map[string]any {
	mount := s.currentMount()
	if mount == nil {
		return map[string]any{"connected": false}
	}
	out := map[string]any{"connected": true}
	if h, ok := s.link.health(); ok {
		out["link"] = h
		// A link that is between reconnect attempts still has a last-known position, and blanking the
		// panel at 3am tells the user less than showing it with a warning does.
		if h.Reconnecting {
			out["reconnecting"] = true
		}
	}
	// While a periodic-error run owns the port, its own cached state is served rather than stealing a
	// command from it — see mount.go.
	if st, ok := s.pecTrain.cachedMountState(); ok {
		out["mount"], out["cached"] = st, true
		return out
	}
	st, err := mount.State(ctx)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["mount"] = st
	return out
}

// mountSite and mountClock push what the app already knows into the hand controller.
//
// The values come from the request body when the UI sends them and from the server's configured site
// and clock otherwise, because the observing site the user actually edits lives in the browser's
// local storage where the server cannot read it.
func (s *Server) mountSite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LatDeg *float64 `json:"lat_deg"`
		LonDeg *float64 `json:"lon_deg"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	sitter, ok := s.currentMount().(interface {
		SetSite(context.Context, nexstar.Site) (nexstar.Site, error)
	})
	if !ok {
		deviceError(w, device.ErrUnsupported)
		return
	}
	site := nexstar.Site{}
	if s.cfg != nil {
		site.LatDeg, site.LonDeg = s.cfg.LatDeg, s.cfg.LonDeg
	}
	if body.LatDeg != nil {
		site.LatDeg = *body.LatDeg
	}
	if body.LonDeg != nil {
		site.LonDeg = *body.LonDeg
	}
	got, err := sitter.SetSite(r.Context(), site)
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"site": got})
}

func (s *Server) mountClock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// UTC is optional; the host clock is the default, and is the one thing on this machine most
		// likely to already be right.
		UTC string `json:"utc"`
		// Zone names the IANA zone the mount's local time should be expressed in. Empty means the
		// host's own zone.
		Zone string `json:"zone"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	clocker, ok := s.currentMount().(interface {
		SetClock(context.Context, time.Time) (nexstar.Clock, error)
	})
	if !ok {
		deviceError(w, device.ErrUnsupported)
		return
	}
	when := time.Now()
	if body.UTC != "" {
		parsed, err := time.Parse(time.RFC3339, body.UTC)
		if err != nil {
			badRequest(w, fmt.Sprintf("utc must be RFC3339: %v", err))
			return
		}
		when = parsed
	}
	loc := time.Local
	if body.Zone != "" {
		l, err := time.LoadLocation(body.Zone)
		if err != nil {
			badRequest(w, fmt.Sprintf("unknown time zone %q", body.Zone))
			return
		}
		loc = l
	}
	got, err := clocker.SetClock(r.Context(), when.In(loc))
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clock": got})
}
