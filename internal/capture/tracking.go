package capture

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/platesolve"
)

// Measuring how the mount actually tracked, from the subs the session is already taking.
//
// Every light frame is a measurement of where the telescope really pointed. Solving them and
// differencing against the first gives the drift over the night — and folded on the worm period,
// the mount's periodic error. It costs no extra exposure time; the only cost is a plate solve, and
// that runs while the next frame is already exposing.
//
// The monitor deliberately SKIPS frames rather than queueing them. A backlog would end the night
// solving frames from hours ago, and the analysis wants samples spread across the run, not a dense
// clump at the start.

// TrackSink persists one measurement. Implemented by the API layer over the store.
type TrackSink interface {
	AddTrackingSample(ctx context.Context, sessionID int64, tSec, raArcsec, decArcsec float64, source string) error
}

// TrackMonitor turns saved frames into tracking samples.
type TrackMonitor struct {
	solver Solver
	sink   TrackSink

	// EveryNth solves one frame in N. Every frame is affordable at 60 s subs; short subs would
	// otherwise ask for a solve every few seconds.
	EveryNth int

	mu       sync.Mutex
	busy     bool
	seen     int
	attempts int
	recorded int
	lastErr  string
	originT  time.Time
	// originRA/Dec is the first solved position: samples are drift RELATIVE to the run's start, so
	// the analysis never has to care where in the sky the run happened.
	originRA, originDec float64
	hasOrigin           bool
}

// NewTrackMonitor builds a monitor. A nil solver disables it — measurement is a bonus, never a
// reason for a capture session to fail.
func NewTrackMonitor(solver Solver, sink TrackSink, everyNth int) *TrackMonitor {
	if solver == nil || sink == nil {
		return nil
	}
	if everyNth <= 0 {
		everyNth = 1
	}
	return &TrackMonitor{solver: solver, sink: sink, EveryNth: everyNth}
}

// Observe hands a freshly saved light frame to the monitor. It returns immediately: the solve runs
// in the background so the sequence never waits on it.
func (m *TrackMonitor) Observe(ctx context.Context, sessionID int64, path string, midExposure time.Time, hint platesolve.Hint) {
	if m == nil || sessionID == 0 || path == "" {
		return
	}
	m.mu.Lock()
	m.seen++
	due := m.seen%m.EveryNth == 0
	if !due || m.busy {
		m.mu.Unlock()
		return // still solving the last one, or not this frame's turn
	}
	m.busy = true
	if m.originT.IsZero() {
		m.originT = midExposure
	}
	origin := m.originT
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.busy = false
			m.mu.Unlock()
		}()
		m.solveAndRecord(ctx, sessionID, path, midExposure.Sub(origin).Seconds(), hint)
	}()
}

// solveAndRecord does the work. Every failure is silent by design: a frame that will not solve
// (cloud, a sparse field, a satellite) is simply not a sample.
func (m *TrackMonitor) solveAndRecord(ctx context.Context, sessionID int64, path string, tSec float64, hint platesolve.Hint) {
	// A solve that outlives the frame cadence is worthless — the next one is already due.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	m.mu.Lock()
	m.attempts++
	m.mu.Unlock()

	res, err := m.solver.Solve(ctx, path, hint)
	if err != nil {
		// Silent to the SEQUENCE — a frame that will not solve must never interrupt a night — but not
		// silent to the user. Without this, "measure tracking" that produces nothing is indis-
		// tinguishable from "measure tracking" that was never running.
		m.mu.Lock()
		m.lastErr = err.Error()
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	if !m.hasOrigin {
		m.originRA, m.originDec, m.hasOrigin = res.RADeg, res.DecDeg, true
		m.mu.Unlock()
		// The first solved frame IS the origin, so its own offset is zero by construction — recording
		// it would add a meaningless point that anchors the drift fit.
		return
	}
	originRA, originDec := m.originRA, m.originDec
	m.mu.Unlock()

	ra, dec := OffsetArcsec(originRA, originDec, res.RADeg, res.DecDeg)
	if err := m.sink.AddTrackingSample(ctx, sessionID, tSec, ra, dec, "solve"); err != nil {
		m.mu.Lock()
		m.lastErr = err.Error()
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	m.recorded++
	m.lastErr = ""
	m.mu.Unlock()
}

// TrackStats reports how the measurement is going, so the UI can explain an empty report.
type TrackStats struct {
	Attempts int    `json:"attempts"`
	Recorded int    `json:"recorded"`
	LastErr  string `json:"last_error,omitempty"`
}

// Stats is safe on a nil monitor: measurement simply is not running.
func (m *TrackMonitor) Stats() (TrackStats, bool) {
	if m == nil {
		return TrackStats{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return TrackStats{Attempts: m.attempts, Recorded: m.recorded, LastErr: m.lastErr}, true
}

// OffsetArcsec converts a difference between two sky positions into true angles on the sky. The RA
// difference is scaled by cos(dec) — an hour of RA covers far less sky near the pole than at the
// equator, and comparing unscaled RA to Dec would misreport the drift by that factor.
func OffsetArcsec(fromRA, fromDec, toRA, toDec float64) (raArcsec, decArcsec float64) {
	dRA := math.Mod(toRA-fromRA+540, 360) - 180 // shortest way round, so 359.9° → -0.1°
	cosDec := math.Cos(fromDec * math.Pi / 180)
	return dRA * 3600 * cosDec, (toDec - fromDec) * 3600
}
