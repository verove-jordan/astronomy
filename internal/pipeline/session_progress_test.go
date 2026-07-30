package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/photom"
)

// TestSessionLine_EventShape: per-session sub-step lines ride at the pinned slot (never moving the
// bar) and carry the session key; the photom event carries the record.
func TestSessionLine_EventShape(t *testing.T) {
	var events []Progress
	o := Options{OnProgress: func(p Progress) { events = append(events, p) }}
	ref := stepRef{Name: "grading + stacking M66 L (3 groups)", Index: 4, Total: 9}

	o.sessionLine(ref, "2023-02-27", "▶ 2023-02-27 · L — calibrate (12 frames)")
	o.sessionPhotom(ref, "2023-02-27", photom.GroupRecord{Session: "2023-02-27", Scale: 5.62, Applied: true})

	require.Len(t, events, 2)
	line := events[0]
	assert.Equal(t, ref.Name, line.Step)
	assert.Equal(t, 4, line.Index)
	assert.Equal(t, 9, line.Total)
	assert.Equal(t, "2023-02-27", line.Session)
	assert.Contains(t, line.Line, "calibrate")

	rec := events[1]
	require.NotNil(t, rec.Photom)
	assert.Equal(t, 5.62, rec.Photom.Scale)
	assert.Equal(t, "2023-02-27", rec.Session)
}

// TestLightStepSlots: the bar's channel-slot count normalizes the capture night OUT — a multi-night
// split must not inflate the total (per-night groups stack inside one channel step), and a
// single-night scan keeps the historical set count exactly.
func TestLightStepSlots(t *testing.T) {
	key := func(filter, night string) inspect.SetKey {
		return inspect.SetKey{Type: inspect.Light, Filter: filter, ExposureMs: 30000, Gain: 250, Session: night}
	}
	set := func(k inspect.SetKey) inspect.Set { return inspect.Set{Key: k, Count: 1} }

	single := &inspect.Inventory{Sets: []inspect.Set{set(key("L", "")), set(key("R", ""))}}
	assert.Equal(t, 2, lightStepSlots(single), "single-night: one slot per set, as before")

	multi := &inspect.Inventory{Sets: []inspect.Set{
		set(key("L", "2023-02-27")), set(key("L", "2023-03-15")),
		set(key("R", "2023-02-27")), set(key("R", "2023-03-15")),
	}}
	assert.Equal(t, 2, lightStepSlots(multi), "per-night splits collapse to one slot per channel config")
}

// TestGroupSessionLabel names groups for the journal: night key first, then catalog session, then
// the current capture.
func TestGroupSessionLabel(t *testing.T) {
	assert.Equal(t, "2023-02-27", groupSessionLabel(lightGroup{Session: "2023-02-27", SessionID: 7}))
	assert.Equal(t, "session 7", groupSessionLabel(lightGroup{SessionID: 7}))
	assert.Equal(t, "current session", groupSessionLabel(lightGroup{Current: true}))
}

// TestPhotomLine renders the journal line for a normalization record.
func TestPhotomLine(t *testing.T) {
	applied := photom.GroupRecord{Label: "Ha g250", Scale: 5.62, Offset: 0.001, Applied: true, MetaSeeded: true}
	assert.Contains(t, photomLine(applied), "×5.62")
	assert.Contains(t, photomLine(applied), "applied")
	assert.Contains(t, photomLine(applied), "scale from headers")

	ref := photom.GroupRecord{Label: "L g250", Scale: 1, Ref: true}
	assert.Contains(t, photomLine(ref), "reference")
}
