package capture

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/verove-jordan/astronomy/internal/platesolve"
)

// A solver that reports a scripted sequence of positions, optionally slowly.
type scriptedSolver struct {
	mu       sync.Mutex
	pos      []platesolve.Result
	calls    int
	delay    time.Duration
	failEach int // fail every Nth call (0 = never)
}

func (s *scriptedSolver) Solve(_ context.Context, _ string, _ platesolve.Hint) (platesolve.Result, error) {
	s.mu.Lock()
	i := s.calls
	s.calls++
	s.mu.Unlock()
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.failEach > 0 && (i+1)%s.failEach == 0 {
		return platesolve.Result{}, fmt.Errorf("no stars detected")
	}
	if i >= len(s.pos) {
		return s.pos[len(s.pos)-1], nil
	}
	return s.pos[i], nil
}

// count reads the call counter safely: Solve runs on the monitor's goroutine.
func (s *scriptedSolver) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type memSink struct {
	mu      sync.Mutex
	samples []struct {
		t, ra, dec float64
		source     string
	}
}

func (m *memSink) AddTrackingSample(_ context.Context, _ int64, t, ra, dec float64, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = append(m.samples, struct {
		t, ra, dec float64
		source     string
	}{t, ra, dec, source})
	return nil
}

func (m *memSink) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.samples)
}

// waitFor polls until cond holds or the deadline passes — the monitor solves in the background, so
// there is nothing to synchronise on directly.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

// The core measurement: drift is reported relative to the first solved frame.
func TestTrackMonitor_MeasuresDriftFromTheFirstFrame(t *testing.T) {
	solver := &scriptedSolver{pos: []platesolve.Result{
		{RADeg: 10.0, DecDeg: 41.0},
		{RADeg: 10.001, DecDeg: 41.0005}, // ~2.7" east (×cos41°), 1.8" north
	}}
	sink := &memSink{}
	m := NewTrackMonitor(solver, sink, 1)
	start := time.Now()

	m.Observe(context.Background(), 7, "a.fit", start, platesolve.Hint{})
	waitFor(t, func() bool { return solver.count() == 1 })
	assert.Zero(t, sink.count(), "the first solved frame IS the origin — its own offset is meaningless")

	m.Observe(context.Background(), 7, "b.fit", start.Add(60*time.Second), platesolve.Hint{})
	waitFor(t, func() bool { return sink.count() == 1 })

	s := sink.samples[0]
	assert.InDelta(t, 60, s.t, 0.1, "time is measured from the first frame")
	assert.InDelta(t, 0.001*3600*0.7547, s.ra, 0.05, "RA drift must be scaled by cos(dec)")
	assert.InDelta(t, 1.8, s.dec, 0.05)
	assert.Equal(t, "solve", s.source)
}

// The contract that matters at the telescope: a slow solve must never hold up the next exposure.
func TestTrackMonitor_SkipsFramesRatherThanQueueing(t *testing.T) {
	solver := &scriptedSolver{pos: []platesolve.Result{{RADeg: 10, DecDeg: 41}}, delay: 300 * time.Millisecond}
	m := NewTrackMonitor(solver, &memSink{}, 1)
	start := time.Now()

	began := time.Now()
	for i := 0; i < 10; i++ {
		m.Observe(context.Background(), 7, "f.fit", start.Add(time.Duration(i)*time.Second), platesolve.Hint{})
	}
	assert.Less(t, time.Since(began), 100*time.Millisecond,
		"Observe must return immediately — the sequence cannot wait on a plate solve")

	waitFor(t, func() bool { return solver.count() >= 1 })
	time.Sleep(400 * time.Millisecond)
	assert.LessOrEqual(t, solver.count(), 2, "frames offered while busy are dropped, not queued")
}

// EveryNth exists so short subs cannot ask for solves faster than they complete.
func TestTrackMonitor_HonoursTheSolveCadence(t *testing.T) {
	solver := &scriptedSolver{pos: []platesolve.Result{{RADeg: 10, DecDeg: 41}}}
	m := NewTrackMonitor(solver, &memSink{}, 3)
	start := time.Now()

	for i := 0; i < 9; i++ {
		m.Observe(context.Background(), 7, "f.fit", start.Add(time.Duration(i)*time.Second), platesolve.Hint{})
		waitFor(t, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return !m.busy
		})
	}
	assert.Equal(t, 3, solver.count(), "one solve in three")
}

// A frame that will not solve is normal — cloud, a sparse field. It must be skipped quietly rather
// than aborting the measurement for the rest of the night.
func TestTrackMonitor_SurvivesFailedSolves(t *testing.T) {
	solver := &scriptedSolver{
		pos:      []platesolve.Result{{RADeg: 10, DecDeg: 41}, {RADeg: 10.001, DecDeg: 41}},
		failEach: 2,
	}
	sink := &memSink{}
	m := NewTrackMonitor(solver, sink, 1)
	start := time.Now()

	for i := 0; i < 6; i++ {
		m.Observe(context.Background(), 7, "f.fit", start.Add(time.Duration(i)*time.Minute), platesolve.Hint{})
		waitFor(t, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return !m.busy
		})
	}
	assert.Equal(t, 6, solver.count())
	assert.Positive(t, sink.count(), "failures must not stop later frames being measured")
}

// Measurement is a bonus, never a requirement.
func TestNewTrackMonitor_NilWithoutItsDependencies(t *testing.T) {
	assert.Nil(t, NewTrackMonitor(nil, &memSink{}, 1))
	assert.Nil(t, NewTrackMonitor(&scriptedSolver{}, nil, 1))
	// A nil monitor must be safe to use — the runner holds one unconditionally.
	var m *TrackMonitor
	assert.NotPanics(t, func() { m.Observe(context.Background(), 1, "x.fit", time.Now(), platesolve.Hint{}) })
}

func TestOffsetArcsec(t *testing.T) {
	// One degree of RA at the equator is 3600".
	ra, dec := OffsetArcsec(10, 0, 11, 0)
	assert.InDelta(t, 3600, ra, 1e-6)
	assert.InDelta(t, 0, dec, 1e-9)

	// The same degree at declination 60° covers half the sky distance.
	ra, _ = OffsetArcsec(10, 60, 11, 60)
	assert.InDelta(t, 1800, ra, 0.5)

	// Crossing 0h must take the short way round, not 359.9 degrees the wrong way.
	ra, _ = OffsetArcsec(359.95, 0, 0.05, 0)
	assert.InDelta(t, 360, ra, 1e-6)
	ra, _ = OffsetArcsec(0.05, 0, 359.95, 0)
	assert.InDelta(t, -360, ra, 1e-6)
}
