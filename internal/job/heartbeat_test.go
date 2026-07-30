package job

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHeartbeat(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	now := func() time.Time { return clock }
	h := newHeartbeat(now)
	h.touch("stacking L", 40)

	advance := func(d time.Duration) { clock = clock.Add(d) }

	// Quiet but not long enough: no beat.
	advance(30 * time.Second)
	line, _, _ := h.beat()
	assert.Empty(t, line)

	// Past the silence window: first beat, at the current step/pct, with both durations.
	advance(20 * time.Second) // 50s of silence
	line, step, pct := h.beat()
	assert.Contains(t, line, "still running: stacking L")
	assert.Contains(t, line, "no output for 50s")
	assert.Equal(t, "stacking L", step)
	assert.Equal(t, 40, pct)

	// Repeats are rate-limited.
	advance(10 * time.Second)
	line, _, _ = h.beat()
	assert.Empty(t, line, "second beat before the repeat interval")
	advance(25 * time.Second)
	line, _, _ = h.beat()
	assert.Contains(t, line, "no output for 1m25s")

	// Fresh output resets both the silence window and the repeat cadence; a step change restarts
	// the per-step clock.
	h.touch("AI colour denoise (GraXpert)", 72)
	advance(44 * time.Second)
	line, _, _ = h.beat()
	assert.Empty(t, line)
	advance(2 * time.Second)
	line, step, pct = h.beat()
	assert.Contains(t, line, "still running: AI colour denoise (GraXpert)")
	assert.Contains(t, line, "46s into this step")
	assert.Equal(t, 72, pct)
	assert.Equal(t, "AI colour denoise (GraXpert)", step)

	// The enrich hook appends live resource context.
	h.enrich = func() string { return "cpu 10.8 cores · rss 6.7 GB" }
	h.touch("AI colour denoise (GraXpert)", 72)
	advance(50 * time.Second)
	line, _, _ = h.beat()
	assert.Contains(t, line, "· cpu 10.8 cores · rss 6.7 GB")
}
