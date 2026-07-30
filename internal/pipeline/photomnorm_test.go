package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/photom"
)

// photomGroupFixture builds a lightGroup with n frames on the given night.
func photomGroupFixture(session string, current bool, n int) lightGroup {
	frames := make([]*inspect.Frame, n)
	for i := range frames {
		frames[i] = &inspect.Frame{Session: session}
	}
	return lightGroup{Session: session, Current: current, Frames: frames}
}

// TestMarkReferenceGroup pins the reference choice to the registration anchor's ranking, so every
// channel of a run normalizes onto the SAME night (task #354: per-channel most-frames references
// put R/G on a 15 s g450 zero-point while L sat on 120 s g0 — irreconcilable channel backgrounds).
func TestMarkReferenceGroup(t *testing.T) {
	tests := []struct {
		name        string
		groups      []lightGroup
		anchorNight string
		wantRef     int
	}{
		{
			name: "anchor night wins over a bigger group",
			groups: []lightGroup{
				photomGroupFixture("2019-08-02", true, 20), // biggest — the old (wrong) choice
				photomGroupFixture("2020-05-06", true, 10), // the run anchor
			},
			anchorNight: "2020-05-06",
			wantRef:     1,
		},
		{
			name: "channel absent from the anchor night falls back to its biggest current group",
			groups: []lightGroup{
				photomGroupFixture("2019-08-02", true, 5),
				photomGroupFixture("2020-04-15", true, 20),
			},
			anchorNight: "2020-05-06",
			wantRef:     1,
		},
		{
			name: "catalog-only merge (no current group) picks deterministically: frames then later night",
			groups: []lightGroup{
				photomGroupFixture("2020-04-15", false, 10),
				photomGroupFixture("2020-04-26", false, 10),
			},
			anchorNight: "",
			wantRef:     1, // equal frames → later night
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pgs := make([]photom.Group, len(tt.groups))
			markReferenceGroup(pgs, tt.groups, tt.anchorNight)
			for i := range pgs {
				assert.Equal(t, i == tt.wantRef, pgs[i].Ref, "group %d Ref flag", i)
			}
		})
	}
}

// TestPhotomStackWeight pins the -weight=noise switch for channels whose photometric normalization
// rewrote a group at a large scale (task #355: Ha groups applied at ×16–27 under wfwhm weighting
// drowned the reference night in amplified noise — sharpness weighting cannot down-weight noise).
func TestPhotomStackWeight(t *testing.T) {
	rec := func(scale float64, applied bool) photom.GroupRecord {
		return photom.GroupRecord{Label: "night", Scale: scale, Applied: applied}
	}
	tests := []struct {
		name       string
		preset     string
		records    []photom.GroupRecord
		wantWeight string
		wantNote   bool
	}{
		{"no records keeps preset", "wfwhm", nil, "wfwhm", false},
		{"small scales keep preset", "wfwhm", []photom.GroupRecord{rec(0.5, true), rec(3.9, true)}, "wfwhm", false},
		{"large up-scale switches", "wfwhm", []photom.GroupRecord{rec(16.6, true)}, "noise", true},
		{"large down-scale switches", "wfwhm", []photom.GroupRecord{rec(0.03, true)}, "noise", true},
		{"unapplied large scale keeps preset", "wfwhm", []photom.GroupRecord{rec(20, false)}, "wfwhm", false},
		{"zero scale ignored", "wfwhm", []photom.GroupRecord{rec(0, true)}, "wfwhm", false},
		{"boundary x4 keeps preset", "wfwhm", []photom.GroupRecord{rec(4, true)}, "wfwhm", false},
		{"already noise stays quiet", "noise", []photom.GroupRecord{rec(16.6, true)}, "noise", false},
		{"unweighted preset switches too", "", []photom.GroupRecord{rec(30, true)}, "noise", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			weight, note := photomStackWeight(tt.preset, tt.records)
			assert.Equal(t, tt.wantWeight, weight)
			if tt.wantNote {
				assert.Contains(t, note, "stack weighting switched to noise")
			} else {
				assert.Empty(t, note)
			}
		})
	}
}
