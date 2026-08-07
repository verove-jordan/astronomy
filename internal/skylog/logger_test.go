package skylog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// clock is a hand-wound time source: the logger reads it from its own goroutine while the test
// advances it, so it must be guarded.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type fakeSource struct {
	mu sync.Mutex
	f  weather.SiteForecast
	q  lightpollution.SiteQuality
	n  int
}

func (s *fakeSource) Forecast(context.Context, float64, float64) (weather.SiteForecast, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.f, ""
}

func (s *fakeSource) Site(context.Context, float64, float64) (lightpollution.SiteQuality, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.q, ""
}

type archived struct {
	kind  string
	atMs  int64
	hours int
}

type fakeSink struct {
	mu        sync.Mutex
	samples   []Sample
	summaries []Summary
	forecasts []archived
	err       error

	added chan struct{} // signalled after every recorded sample
}

func newFakeSink() *fakeSink { return &fakeSink{added: make(chan struct{}, 32)} }

func (s *fakeSink) AddSample(_ context.Context, _ int64, sample Sample) error {
	s.mu.Lock()
	s.samples = append(s.samples, sample)
	err := s.err
	s.mu.Unlock()
	select {
	case s.added <- struct{}{}:
	default:
	}
	return err
}

func (s *fakeSink) SaveSummary(_ context.Context, _ int64, sum Summary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summaries = append(s.summaries, sum)
	return s.err
}

func (s *fakeSink) SaveForecast(_ context.Context, _ int64, kind string, atMs int64, f weather.SiteForecast) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forecasts = append(s.forecasts, archived{kind: kind, atMs: atMs, hours: len(f.Hours)})
	return s.err
}

func (s *fakeSink) counts() (samples, summaries int, forecasts []archived) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.samples), len(s.summaries), append([]archived(nil), s.forecasts...)
}

// waitForSample blocks until the sink has recorded another sample, failing the test rather than
// hanging the suite if the logger never gets there.
func waitForSample(t *testing.T, sink *fakeSink) {
	t.Helper()
	select {
	case <-sink.added:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a sample")
	}
}

// newTestLogger wires a logger onto hand-driven time and a hand-driven schedule, so no test sleeps.
func newTestLogger(src Source, sink Sink, c *clock, tick <-chan time.Time) *Logger {
	lg := New(src, sink, time.Hour)
	lg.now = c.now
	lg.tick = tick
	return lg
}

func TestNew_NilDependenciesDisableRecording(t *testing.T) {
	assert.Nil(t, New(nil, newFakeSink(), time.Hour))
	assert.Nil(t, New(&fakeSource{}, nil, time.Hour))

	t.Run("a nil logger is safe to use", func(t *testing.T) {
		var lg *Logger
		assert.NotPanics(t, func() {
			lg.Run(context.Background(), 1, Site{}, Target{}, nil, nil)
		})
		_, ok := lg.Stats()
		assert.False(t, ok)
	})
}

func TestLogger_Run(t *testing.T) {
	base := testTime.Truncate(time.Hour)

	t.Run("opens with a sample and a start snapshot, closes with an end snapshot", func(t *testing.T) {
		src := &fakeSource{f: forecastAt(base, 4)}
		sink := newFakeSink()
		c := &clock{at: base}
		tick := make(chan time.Time)
		lg := newTestLogger(src, sink, c, tick)

		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			lg.Run(context.Background(), 7, testSite, Target{}, done, nil)
			close(finished)
		}()

		waitForSample(t, sink)
		c.advance(time.Hour) // the closing sample is due
		close(done)
		<-finished

		samples, summaries, forecasts := sink.counts()
		assert.Equal(t, 2, samples, "one at the start, one at the end")
		assert.Equal(t, samples, summaries, "the summary is rewritten after every sample")
		require.Len(t, forecasts, 2)
		assert.Equal(t, KindStart, forecasts[0].kind)
		assert.Equal(t, KindEnd, forecasts[1].kind)
		assert.Equal(t, 4, forecasts[0].hours)
	})

	t.Run("every tick records another sample", func(t *testing.T) {
		src := &fakeSource{f: forecastAt(base, 4)}
		sink := newFakeSink()
		c := &clock{at: base}
		tick := make(chan time.Time)
		lg := newTestLogger(src, sink, c, tick)

		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			lg.Run(context.Background(), 7, testSite, Target{}, done, nil)
			close(finished)
		}()

		waitForSample(t, sink)
		for i := 1; i <= 3; i++ {
			c.advance(time.Hour)
			tick <- time.Time{}
			waitForSample(t, sink)
		}
		c.advance(time.Hour) // so the closing sample is due rather than suppressed
		close(done)
		<-finished

		samples, _, _ := sink.counts()
		assert.Equal(t, 5, samples, "opening + three ticks + closing")

		stats, ok := lg.Stats()
		require.True(t, ok)
		assert.Equal(t, 5, stats.Samples)
		assert.Empty(t, stats.LastErr)
	})

	t.Run("a session ending right after a sample does not record a duplicate", func(t *testing.T) {
		src := &fakeSource{f: forecastAt(base, 4)}
		sink := newFakeSink()
		c := &clock{at: base}
		tick := make(chan time.Time)
		lg := newTestLogger(src, sink, c, tick)

		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			lg.Run(context.Background(), 7, testSite, Target{}, done, nil)
			close(finished)
		}()

		waitForSample(t, sink)
		c.advance(time.Minute) // well inside minFinalGap
		close(done)
		<-finished

		samples, _, forecasts := sink.counts()
		assert.Equal(t, 1, samples, "the closing sample is suppressed")
		require.Len(t, forecasts, 2, "the end snapshot is still archived")
	})

	t.Run("a cancelled context still writes the closing record", func(t *testing.T) {
		src := &fakeSource{f: forecastAt(base, 4)}
		sink := newFakeSink()
		c := &clock{at: base}
		lg := newTestLogger(src, sink, c, make(chan time.Time))

		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			lg.Run(ctx, 7, testSite, Target{}, nil, nil)
			close(finished)
		}()

		waitForSample(t, sink)
		c.advance(time.Hour)
		cancel()
		<-finished

		samples, summaries, forecasts := sink.counts()
		assert.Equal(t, 2, samples)
		assert.NotZero(t, summaries)
		require.Len(t, forecasts, 2)
		assert.Equal(t, KindEnd, forecasts[1].kind)
	})

	t.Run("the session status rides on every row", func(t *testing.T) {
		src := &fakeSource{f: forecastAt(base, 4)}
		sink := newFakeSink()
		c := &clock{at: base}
		lg := newTestLogger(src, sink, c, make(chan time.Time))

		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			lg.Run(context.Background(), 7, testSite, Target{}, done, func() string { return "paused" })
			close(finished)
		}()

		waitForSample(t, sink)
		close(done)
		<-finished

		sink.mu.Lock()
		defer sink.mu.Unlock()
		require.NotEmpty(t, sink.samples)
		assert.Equal(t, "paused", sink.samples[0].SessionStatus)
	})

	t.Run("a failing sink is remembered but never propagated", func(t *testing.T) {
		src := &fakeSource{f: forecastAt(base, 4)}
		sink := newFakeSink()
		sink.err = errors.New("database is down")
		c := &clock{at: base}
		lg := newTestLogger(src, sink, c, make(chan time.Time))

		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			lg.Run(context.Background(), 7, testSite, Target{}, done, nil)
			close(finished)
		}()

		waitForSample(t, sink)
		close(done)

		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("a sink failure must not stall the logger")
		}

		stats, ok := lg.Stats()
		require.True(t, ok)
		assert.Equal(t, "database is down", stats.LastErr)
	})

	t.Run("a dead weather feed still produces rows, and archives no empty forecast", func(t *testing.T) {
		src := &fakeSource{} // no hours at all
		sink := newFakeSink()
		c := &clock{at: base}
		lg := newTestLogger(src, sink, c, make(chan time.Time))

		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			lg.Run(context.Background(), 7, testSite, Target{}, done, nil)
			close(finished)
		}()

		waitForSample(t, sink)
		c.advance(time.Hour)
		close(done)
		<-finished

		samples, _, forecasts := sink.counts()
		assert.Equal(t, 2, samples)
		assert.Empty(t, forecasts, "a blank blob would hide that the feed was down")

		sink.mu.Lock()
		defer sink.mu.Unlock()
		assert.Equal(t, SourceUnavailable, sink.samples[0].Source)
		assert.NotZero(t, sink.samples[0].MoonPhaseAngleDeg, "the ephemeris is computed locally")
	})
}
