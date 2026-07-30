package inspect

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetKeyID_Stable pins the exclusion-token format: analysis-time IDs are matched again at run
// time (RunRequest.exclude_sets), so any change here breaks queued/stored exclusions.
func TestSetKeyID_Stable(t *testing.T) {
	cases := []struct {
		name string
		key  SetKey
		want string
	}{
		{
			"light with night and temp",
			SetKey{Type: Light, Object: "M101", Filter: "R", ExposureMs: 120000, Gain: 139, Offset: 21, Bin: 1, TempBucket: -10, Session: "2026-06-14"},
			"LIGHT|M101|R|e120000|g139o21b1|i0|t-10|s:2026-06-14",
		},
		{
			"bias zero fields stay explicit",
			SetKey{Type: Bias, Gain: 139, Offset: 21, Bin: 1},
			"BIAS|||e0|g139o21b1|i0|t0|s:",
		},
		{
			"iso still camera",
			SetKey{Type: Light, Object: "MilkyWay", ExposureMs: 10000, ISO: 3200, Bin: 1},
			"LIGHT|MilkyWay||e10000|g0o0b1|i3200|t0|s:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.key.ID())
		})
	}
}

func lightFrames(filter string, n int) []*Frame {
	frames := make([]*Frame, 0, n)
	for i := 0; i < n; i++ {
		frames = append(frames, nightFrame(Light, filter, "", 120000, 139))
	}
	return frames
}

// TestInventory_ExcludeSets drops one whole set by ID and leaves the siblings intact.
func TestInventory_ExcludeSets(t *testing.T) {
	build := func() *Inventory {
		inv := &Inventory{}
		inv.Frames = append(inv.Frames, lightFrames("R", 3)...)
		inv.Frames = append(inv.Frames, lightFrames("L", 4)...)
		inv.Frames = append(inv.Frames, nightFrame(Dark, "", "", 120000, 139))
		inv.Sets = buildSets(inv.Frames)
		return inv
	}
	idByFilter := func(inv *Inventory, filter string) string {
		for _, s := range inv.Sets {
			if s.Key.Type == Light && s.Key.Filter == filter {
				return s.Key.ID()
			}
		}
		t.Fatalf("no light set for filter %q", filter)
		return ""
	}

	t.Run("drop one set", func(t *testing.T) {
		inv := build()
		frames, sets := inv.ExcludeSets([]string{idByFilter(inv, "R")})
		assert.Equal(t, 3, frames)
		assert.Equal(t, 1, sets)
		assert.Len(t, inv.Frames, 5) // 4 L lights + 1 dark
		for _, s := range inv.Sets {
			assert.NotEqual(t, "R", s.Key.Filter)
		}
	})

	t.Run("unknown id is a no-op", func(t *testing.T) {
		inv := build()
		frames, sets := inv.ExcludeSets([]string{"LIGHT|nope|X|e1|g0o0b1|i0|t0|s:"})
		assert.Zero(t, frames)
		assert.Zero(t, sets)
		assert.Len(t, inv.Frames, 8)
	})

	t.Run("empty list is a no-op", func(t *testing.T) {
		inv := build()
		frames, sets := inv.ExcludeSets(nil)
		assert.Zero(t, frames)
		assert.Zero(t, sets)
		assert.Len(t, inv.Frames, 8)
	})
}

// TestFinalizeInventory_ExcludeBeforeMapping: exclusion tokens are canonical-scan IDs, so they must
// match even when the user also remaps the filter — exclusion runs before ApplyFilterMapping.
func TestFinalizeInventory_ExcludeBeforeMapping(t *testing.T) {
	inv := &Inventory{}
	inv.Frames = append(inv.Frames, lightFrames("V", 2)...)
	inv.Frames = append(inv.Frames, lightFrames("L", 3)...)

	canonical := buildSets(inv.Frames)
	var excludeID string
	for _, s := range canonical {
		if s.Key.Filter == "V" {
			excludeID = s.Key.ID()
		}
	}
	require.NotEmpty(t, excludeID)

	finalizeInventory(inv, ScanOptions{
		ExcludeSets:   []string{excludeID},
		FilterMapping: map[string]string{"V": "G"},
	})

	assert.Len(t, inv.Frames, 3)
	for _, s := range inv.Sets {
		assert.NotContains(t, []string{"V", "G"}, s.Key.Filter)
	}
	found := false
	for _, w := range inv.Warnings {
		if w == "2 frame(s) in 1 set(s) excluded by user (stray-light artifact)" {
			found = true
		}
	}
	assert.True(t, found, "finalize warning missing: %v", inv.Warnings)
}
