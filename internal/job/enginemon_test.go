package job

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/verove-jordan/astronomy/internal/sysmon"
)

func TestEngineMonitor_PublishesPerJobAtLivePosition(t *testing.T) {
	var events []Event
	em := newEngineMonitor(func(e Event) { events = append(events, e) })
	em.cores = 12

	started := &liveStats{}
	started.setProgress(42, "stacking L")
	idle := &liveStats{} // no progress yet — must be skipped (a pct-0 event would yank its bar)
	em.jobs[1] = &engineJob{ls: started}
	em.jobs[2] = &engineJob{ls: idle}

	em.onSample(sysmon.Sample{RSSBytes: 6 << 30, CPUPercent: 1078})
	if assert.Len(t, events, 1) {
		e := events[0]
		assert.Equal(t, int64(1), e.JobID)
		assert.Equal(t, 42, e.Progress)
		assert.Equal(t, "stacking L", e.Step)
		assert.Equal(t, int64(6<<30), e.RSSBytes)
		assert.Equal(t, float64(1078), e.CPUPercent)
		assert.Equal(t, 12, e.CPUCores)
	}

	// Peak is job-wide: a lower later reading keeps the higher peak.
	em.onSample(sysmon.Sample{RSSBytes: 2 << 30, CPUPercent: 300})
	last := events[len(events)-1]
	assert.Equal(t, int64(2<<30), last.RSSBytes)
	assert.Equal(t, int64(6<<30), last.PeakRSSBytes)

	// All-zero samples (subtree momentarily unreadable) are dropped entirely.
	n := len(events)
	em.onSample(sysmon.Sample{})
	assert.Len(t, events, n)

	assert.Contains(t, em.liveNote(), "/12 cores · rss ")
}

func TestEngineMonitor_Refcount(t *testing.T) {
	em := newEngineMonitor(func(Event) {})
	em.register(1, &liveStats{})
	em.register(2, &liveStats{})
	assert.NotNil(t, em.stopFn, "first register starts the sampler")
	em.release(1)
	assert.NotNil(t, em.stopFn, "sampler keeps running while a job remains")
	em.release(2)
	assert.Nil(t, em.stopFn, "last release stops the sampler")
}

func TestLiveStats_ToolPeak(t *testing.T) {
	ls := &liveStats{}
	ls.toolSample(100)
	ls.toolSample(700)
	ls.toolSample(300)
	assert.Equal(t, int64(700), ls.takeToolPeak())
	assert.Zero(t, ls.takeToolPeak(), "taking resets the peak")
}

func TestMirrorToStdout(t *testing.T) {
	assert.True(t, mirrorToStdout("▶ composite (GIMP)"))
	assert.True(t, mirrorToStdout("✓ export done in 4s"))
	assert.True(t, mirrorToStdout("⚠ Ha: only 2/10 frames registered — 8 dropped (no stars matched)"))
	assert.True(t, mirrorToStdout("✗ job failed: boom"))
	assert.False(t, mirrorToStdout("log: Integration of 10 images"))
	assert.False(t, mirrorToStdout("still running: export"))
}
