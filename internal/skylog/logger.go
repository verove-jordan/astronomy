package skylog

import (
	"context"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/weather"
)

// DefaultInterval is how often the sky is sampled. Hourly, because the feeds themselves are hourly:
// asking more often repeats the same numbers while spending a free-tier request budget that a long
// winter night can genuinely exhaust. The ephemeris half moves continuously, but it is exact at any
// instant and can be re-derived from at_ms afterwards.
const DefaultInterval = time.Hour

// minFinalGap suppresses the closing sample when the session ends right after a scheduled one — two
// rows a minute apart say nothing and would skew the medians toward the final minutes.
const minFinalGap = 5 * time.Minute

// finalWriteTimeout bounds the writes that happen after the run's context is already cancelled.
const finalWriteTimeout = 10 * time.Second

// Snapshot kinds for the archived forecasts.
const (
	KindStart = "start"
	KindEnd   = "end"
)

// Logger samples the sky for the length of one capture session.
//
// It never blocks and never fails the session: every sink error is remembered for the UI (the way
// capture.TrackMonitor remembers a failed solve) and then dropped. A night must not end because a
// weather API had a bad minute.
type Logger struct {
	src      Source
	sink     Sink
	interval time.Duration

	// tick replaces the internal ticker when non-nil, so tests drive the schedule by hand instead of
	// sleeping through it.
	tick <-chan time.Time
	// now is the clock, injectable for the same reason.
	now func() time.Time

	mu      sync.Mutex
	samples []Sample
	lastErr string
}

// New builds a logger. A nil source or sink disables it — conditions are a bonus record, never a
// precondition for capturing.
func New(src Source, sink Sink, interval time.Duration) *Logger {
	if src == nil || sink == nil {
		return nil
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Logger{src: src, sink: sink, interval: interval, now: time.Now}
}

// Stats reports how the recording is going, so a logbook with no chart can explain itself rather
// than looking broken.
type Stats struct {
	Samples int    `json:"samples"`
	LastErr string `json:"last_error,omitempty"`
}

// Stats is safe on a nil logger: recording simply is not running.
func (l *Logger) Stats() (Stats, bool) {
	if l == nil {
		return Stats{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return Stats{Samples: len(l.samples), LastErr: l.lastErr}, true
}

// Run records the session until done is closed or ctx is cancelled, then writes the closing sample,
// the closing forecast snapshot and the final summary. It blocks, so callers launch it in a
// goroutine; it is safe on a nil logger.
//
// statusOf lets each row carry the session's status at that instant — a paused stretch is usually
// the observer waiting out cloud, which makes those rows the interesting ones. A nil statusOf
// records everything as running.
func (l *Logger) Run(ctx context.Context, sessionID int64, site Site, tgt Target, done <-chan struct{}, statusOf func() string) {
	if l == nil || sessionID == 0 {
		return
	}
	if statusOf == nil {
		statusOf = func() string { return "running" }
	}

	// The opening sample doubles as the opening forecast snapshot: one fetch, two records.
	f := l.sample(ctx, sessionID, site, tgt, statusOf())
	l.snapshot(ctx, sessionID, KindStart, f)

	tick := l.tick
	if tick == nil {
		t := time.NewTicker(l.interval)
		defer t.Stop()
		tick = t.C
	}

	for {
		select {
		case <-tick:
			l.sample(ctx, sessionID, site, tgt, statusOf())
		case <-done:
			l.finish(ctx, sessionID, site, tgt, statusOf())
			return
		case <-ctx.Done():
			l.finish(ctx, sessionID, site, tgt, statusOf())
			return
		}
	}
}

// finish writes the closing record. It runs on a context detached from the one that just ended —
// otherwise the very cancellation that stops the session would also discard its final summary.
func (l *Logger) finish(ctx context.Context, sessionID int64, site Site, tgt Target, status string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalWriteTimeout)
	defer cancel()

	l.mu.Lock()
	var lastMs int64
	if n := len(l.samples); n > 0 {
		lastMs = l.samples[n-1].AtMs
	}
	l.mu.Unlock()

	// sample() is the only writer of the summary, so there is nothing to rewrite here: either a closing
	// sample was taken and refreshed it, or the run ended so soon after the previous one that the
	// summary is already current.
	var f weather.SiteForecast
	if lastMs == 0 || l.now().Sub(time.UnixMilli(lastMs)) >= minFinalGap {
		f = l.sample(ctx, sessionID, site, tgt, status)
	} else {
		f, _ = l.src.Forecast(ctx, site.Lat, site.Lon)
	}
	l.snapshot(ctx, sessionID, KindEnd, f)
}

// sample fetches, records and returns the forecast it used, so a caller that also wants to archive
// the full timeline does not pay for a second fetch.
func (l *Logger) sample(ctx context.Context, sessionID int64, site Site, tgt Target, status string) weather.SiteForecast {
	f, _ := l.src.Forecast(ctx, site.Lat, site.Lon)
	q, _ := l.src.Site(ctx, site.Lat, site.Lon)

	s := Observe(l.now(), site, tgt, f, q)
	s.SessionStatus = status

	l.mu.Lock()
	l.samples = append(l.samples, s)
	l.mu.Unlock()

	l.note(l.sink.AddSample(ctx, sessionID, s))
	l.writeSummary(ctx, sessionID)
	return f
}

// writeSummary rewrites the rolled-up record after every sample rather than once at the end, so a
// session killed mid-night still carries an accurate summary of the hours it did get.
func (l *Logger) writeSummary(ctx context.Context, sessionID int64) {
	l.mu.Lock()
	snapshot := make([]Sample, len(l.samples))
	copy(snapshot, l.samples)
	l.mu.Unlock()

	l.note(l.sink.SaveSummary(ctx, sessionID, Summarize(snapshot)))
}

// snapshot archives the whole hourly forecast. An empty one is skipped: storing a blank blob would
// make "the feed was down" indistinguishable from "nothing was ever recorded".
func (l *Logger) snapshot(ctx context.Context, sessionID int64, kind string, f weather.SiteForecast) {
	if len(f.Hours) == 0 {
		return
	}
	l.note(l.sink.SaveForecast(ctx, sessionID, kind, l.now().UnixMilli(), f))
}

// note remembers a failure for Stats and otherwise drops it — the sequencer must never learn that
// the logbook had trouble.
func (l *Logger) note(err error) {
	if err == nil {
		return
	}
	l.mu.Lock()
	l.lastErr = err.Error()
	l.mu.Unlock()
}
