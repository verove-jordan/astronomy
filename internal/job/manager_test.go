package job

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/store"
)

// TestStepPercent pins the progress mapping that fixes the "100% while still working" bar: a running
// step is half-complete, so a step never reads 100% (that is reserved for the job's "done" event), and
// the bar still advances at every step boundary.
func TestStepPercent(t *testing.T) {
	tests := []struct {
		name         string
		index, total int
		want         int
	}{
		{"no step count (planetary)", 0, 0, 0},
		{"first of four starts above zero", 1, 4, 12},
		{"second of four", 2, 4, 37},
		{"final combine of four never hits 100", 4, 4, 87},
		{"final combine of seven never hits 100", 7, 7, 92},
		{"large run final step caps below 100", 50, 50, 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stepPercent(tt.index, tt.total))
		})
	}
}

// TestStepPercent_NeverReports100WhileRunning is the invariant behind the fix: for any plausible step
// count, the last running step stays below 100% so the bar can't look finished mid-run.
func TestStepPercent_NeverReports100WhileRunning(t *testing.T) {
	for total := 1; total <= 200; total++ {
		for index := 1; index <= total; index++ {
			got := stepPercent(index, total)
			assert.Less(t, got, 100, "total=%d index=%d", total, index)
			assert.GreaterOrEqual(t, got, 0, "total=%d index=%d", total, index)
		}
	}
}

// TestPreviewSnapshot_RetainsAndClears pins the reload fix: the latest preview + accumulated milestone
// previews are retained per running job (so a page reloaded mid-run re-hydrates them from the SSE
// snapshot) and are cleared when the job finishes.
func TestPreviewSnapshot_RetainsAndClears(t *testing.T) {
	m := &Manager{
		lastPreview:   map[int64]string{},
		stagePreviews: map[int64][]postprocess.StagePreview{},
	}
	const id = int64(7)

	m.publish(Event{JobID: id, Status: store.JobRunning, Preview: "/o/0.png",
		StagePreview: &postprocess.StagePreview{Index: 0, Stage: "stacked", PngPath: "/o/0.png"}})
	m.publish(Event{JobID: id, Status: store.JobRunning, Preview: "/o/1.png",
		StagePreview: &postprocess.StagePreview{Index: 1, Stage: "combined", PngPath: "/o/1.png"}})
	// Re-emit index 0 → upsert in place, not appended.
	m.publish(Event{JobID: id, Status: store.JobRunning,
		StagePreview: &postprocess.StagePreview{Index: 0, Stage: "stacked", PngPath: "/o/0b.png"}})

	prev, stages := m.PreviewSnapshot(id)
	assert.Equal(t, "/o/1.png", prev, "latest preview retained")
	require.Len(t, stages, 2, "two distinct milestones (index 0 upserted, not appended)")
	assert.Equal(t, "/o/0b.png", stages[0].PngPath)
	assert.Equal(t, "/o/1.png", stages[1].PngPath)

	// The returned slice is a copy — mutating it must not corrupt the retained state.
	stages[0].PngPath = "mutated"
	_, again := m.PreviewSnapshot(id)
	assert.Equal(t, "/o/0b.png", again[0].PngPath, "snapshot returns a copy")

	// A terminal event clears retention so nothing lingers after the job finishes.
	m.publish(Event{JobID: id, Status: store.JobSucceeded, Done: true})
	prev, stages = m.PreviewSnapshot(id)
	assert.Empty(t, prev)
	assert.Empty(t, stages)
}
