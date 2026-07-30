package inspect

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func subsetFixture() *Inventory {
	mk := func(path, filter, session string, t FrameType) *Frame {
		return &Frame{Path: path, Type: t, Filter: filter, ExposureMs: 60_000, Gain: 200, Session: session}
	}
	frames := []*Frame{
		mk("/cap/p01/L1.fit", "L", "2026-01-01", Light),
		mk("/cap/p01/L2.fit", "L", "2026-01-02", Light),
		mk("/cap/p02/L3.fit", "L", "2026-01-01", Light),
		mk("/cap/p02/L4.fit", "L", "2026-01-02", Light),
		mk("/cap/darks/D1.fit", "", "", Dark),
	}
	inv := &Inventory{
		Root:     "/cap",
		Frames:   frames,
		Videos:   []*Frame{mk("/cap/p01/v.ser", "", "", Video)},
		Warnings: []string{"w1"},
	}
	inv.Sets = buildSets(frames)
	inv.Sessions = sessionSummary(frames)
	return inv
}

func TestSubset(t *testing.T) {
	keepPanel := func(prefix string) func(*Frame) bool {
		return func(f *Frame) bool { return f.Type != Light || strings.HasPrefix(f.Path, prefix) }
	}
	tests := []struct {
		name       string
		keep       func(*Frame) bool
		wantFrames int
		wantVideos int
		wantSplit  bool // light sets keep their per-night Session key
	}{
		{"panel subset keeps multi-night split", keepPanel("/cap/p01/"), 3, 1, true},
		{"single-night subset collapses the split", func(f *Frame) bool {
			return f.Type != Light || f.Session == "2026-01-01"
		}, 3, 1, false},
		{"nil keep copies everything", nil, 5, 1, true},
		{"keep nothing", func(*Frame) bool { return false }, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := subsetFixture()
			out := Subset(inv, tt.keep)
			require.NotNil(t, out)
			assert.Equal(t, "/cap", out.Root)
			assert.Len(t, out.Frames, tt.wantFrames)
			assert.Len(t, out.Videos, tt.wantVideos)
			for _, s := range out.SetsOfType(Light) {
				if tt.wantSplit {
					assert.NotEmpty(t, s.Key.Session, "multi-night subset must keep per-night sets")
				} else {
					assert.Empty(t, s.Key.Session, "single-night subset must not split by night")
				}
			}
			// The input inventory is untouched.
			assert.Len(t, inv.Frames, 5)
			assert.Len(t, inv.Videos, 1)
			assert.Equal(t, []string{"w1"}, inv.Warnings)
		})
	}
}

func TestSubset_CopiesDoNotAlias(t *testing.T) {
	inv := subsetFixture()
	out := Subset(inv, nil)
	require.NotEmpty(t, out.Warnings)
	out.Warnings[0] = "mutated"
	assert.Equal(t, "w1", inv.Warnings[0], "warnings must be copied, not aliased")
	assert.Nil(t, Subset(nil, nil))
}
