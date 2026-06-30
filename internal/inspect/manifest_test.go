package inspect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseManifest_CanonicalExample(t *testing.T) {
	// The user's documented form: a filter sequence then a per-filter gain map.
	m := parseManifest("LLL RR GG BB Ha Ha\ngain L200 RGB250 Ha300\n")

	wantFilter := []string{"L", "L", "L", "R", "R", "G", "G", "B", "B", "Ha", "Ha"}
	wantGain := []int64{200, 200, 200, 250, 250, 250, 250, 250, 250, 300, 300}
	require.Len(t, m.Slots, len(wantFilter))
	for i, s := range m.Slots {
		assert.Equal(t, wantFilter[i], s.Filter, "slot %d filter", i)
		assert.True(t, s.HasGain, "slot %d hasgain", i)
		assert.Equal(t, wantGain[i], s.Gain, "slot %d gain", i)
	}
}

func TestParseManifest_RealFixtures(t *testing.T) {
	const m81 = `L L L L L L 0 gain
R 0 gain
G 0 gain
B 0 gain
Ha 180 gain

Ha 50 gain 300s
Ha 10 gain 300s
Ha Ha 0 gain 300s

120s
-25°
full moon j-1
`
	const m33 = `LLL R G B Ha
LLL R G B Ha
LLL
LLL
LL
30 s
300 gain
-15°`
	const flats = `LLLRGBHaHa
0 gain
-15°
L -> 2s
R -> 7s
G -> 6s
B -> 6s
Ha -> 200s
histogram : debut 38400 - fin 51200 - avg - 40k/45k`
	const orion = `filtre 1 2 3 4 5 1
10 photo par filtre
30s
-30deg
150 gain`

	t.Run("per-line and per-filter gains (m81)", func(t *testing.T) {
		m := parseManifest(m81)
		require.Len(t, m.Slots, 14) // 6L + R + G + B + 5Ha
		assert.Equal(t, countFilter(m.Slots, "L"), 6)
		assert.Equal(t, countFilter(m.Slots, "Ha"), 5)
		// Ha slots carry the gains 180, 50, 10, 0, 0 in order.
		assert.Equal(t, []int64{180, 50, 10, 0, 0}, gainsFor(m.Slots, "Ha"))
		// L has no inline exposure → inherits the global 120s; the 300s Ha keep their own.
		assert.Equal(t, int64(120_000), expFor(m.Slots, "L")[0])
		assert.Equal(t, int64(300_000), gainExp(m.Slots, "Ha", 50))
		assert.True(t, m.HasTemp)
		assert.Equal(t, int64(-25_000), m.TempMilliC)
		assert.NotEmpty(t, m.Warnings) // "full moon j-1" ignored
	})

	t.Run("repeated sequences, global gain (m33)", func(t *testing.T) {
		m := parseManifest(m33)
		require.Len(t, m.Slots, 22) // (3+1+1+1+1)*2 + 3 + 3 + 2
		for _, s := range m.Slots {
			assert.True(t, s.HasGain)
			assert.Equal(t, int64(300), s.Gain)
			assert.Equal(t, int64(30_000), s.ExposureMs)
		}
		assert.Equal(t, int64(-15_000), m.TempMilliC)
	})

	t.Run("glued run, per-filter exposures, gain 0 (flats)", func(t *testing.T) {
		m := parseManifest(flats)
		require.Len(t, m.Slots, 8) // LLL R G B Ha Ha
		for _, s := range m.Slots {
			assert.True(t, s.HasGain)
			assert.Equal(t, int64(0), s.Gain) // "0 gain" is a real value, not "unset"
		}
		assert.Equal(t, int64(2_000), expFor(m.Slots, "L")[0])
		assert.Equal(t, int64(7_000), expFor(m.Slots, "R")[0])
		assert.Equal(t, int64(200_000), expFor(m.Slots, "Ha")[0])
	})

	t.Run("numeric filter-wheel is unmappable (orion)", func(t *testing.T) {
		m := parseManifest(orion)
		assert.Empty(t, m.Slots) // "filtre 1 2 3 4 5 1" has no LRGB letters
		assert.NotEmpty(t, m.Warnings)
		assert.True(t, m.HasTemp)
	})
}

func TestExpandFilterWord(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		ok   bool
	}{
		{"LLLRGBHaHa", []string{"L", "L", "L", "R", "G", "B", "Ha", "Ha"}, true},
		{"Ha", []string{"Ha"}, true},
		{"OIII", []string{"OIII"}, true},
		{"gain", nil, false},
		{"moon", nil, false},
		{"L200", nil, false}, // digits are not filters
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := expandFilterWord(tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestApplyManifests_MapsSlotsToChronologicalSubdirs(t *testing.T) {
	root := t.TempDir()
	mfDir := filepath.Join(root, "M101old")
	require.NoError(t, os.MkdirAll(mfDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mfDir, "info.txt"),
		[]byte("LL RR\n300 gain\n-15°\n"), 0o644))

	// Four capture sub-runs; build them out of chronological order to prove the sort.
	runs := []string{
		"2021-08-14_00_40_00Z", // R (last)
		"2021-08-14_00_10_00Z", // L (first)
		"2021-08-14_00_30_00Z", // R
		"2021-08-14_00_20_00Z", // L
	}
	inv := &Inventory{Root: root}
	for _, r := range runs {
		dir := filepath.Join(mfDir, r)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		inv.Frames = append(inv.Frames,
			&Frame{Path: filepath.Join(dir, "a.fit"), Type: Unknown},
			&Frame{Path: filepath.Join(dir, "b.fit"), Type: Unknown})
	}

	applyManifests(context.Background(), root, inv)

	wantFilter := map[string]string{
		"2021-08-14_00_10_00Z": "L", "2021-08-14_00_20_00Z": "L",
		"2021-08-14_00_30_00Z": "R", "2021-08-14_00_40_00Z": "R",
	}
	for _, fr := range inv.Frames {
		run := filepath.Base(filepath.Dir(fr.Path))
		assert.Equal(t, wantFilter[run], fr.Filter, "frame in %s", run)
		assert.Equal(t, Light, fr.Type)
		assert.Equal(t, int64(300), fr.Gain)
		assert.True(t, fr.HasTemp)
		assert.Equal(t, SourceManifest, fr.ClassSource)
	}
}

// A hand-written info.txt that lists more filter tokens than there are capture folders (e.g. a session
// cut short) must still map the aligned prefix in capture order and warn about the rest — NOT discard the
// whole manifest (the old all-or-nothing behavior that dumped every light into signal detection and was
// the root of the "everything became Ha" misclassification).
func TestApplyManifests_CountMismatchMapsMinAndWarns(t *testing.T) {
	root := t.TempDir()
	mfDir := filepath.Join(root, "set")
	require.NoError(t, os.MkdirAll(mfDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mfDir, "info.txt"), []byte("L R Ha\n"), 0o644)) // 3 slots

	inv := &Inventory{Root: root}
	for _, r := range []string{"2021-08-14_00_10_00Z", "2021-08-14_00_20_00Z"} { // only 2 folders
		dir := filepath.Join(mfDir, r)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		inv.Frames = append(inv.Frames, &Frame{Path: filepath.Join(dir, "a.fit"), Type: Unknown})
	}

	applyManifests(context.Background(), root, inv)

	wantFilter := map[string]string{"2021-08-14_00_10_00Z": "L", "2021-08-14_00_20_00Z": "R"}
	for _, fr := range inv.Frames {
		run := filepath.Base(filepath.Dir(fr.Path))
		assert.Equal(t, wantFilter[run], fr.Filter, "first folders map in order; 3rd slot (Ha) has no folder")
		assert.Equal(t, Light, fr.Type)
	}
	require.NotEmpty(t, inv.Warnings, "the unmapped remainder must be warned")
}

// --- test helpers ---

func countFilter(slots []manifestSlot, f string) int {
	n := 0
	for _, s := range slots {
		if s.Filter == f {
			n++
		}
	}
	return n
}

func gainsFor(slots []manifestSlot, f string) []int64 {
	var out []int64
	for _, s := range slots {
		if s.Filter == f {
			out = append(out, s.Gain)
		}
	}
	return out
}

func expFor(slots []manifestSlot, f string) []int64 {
	var out []int64
	for _, s := range slots {
		if s.Filter == f {
			out = append(out, s.ExposureMs)
		}
	}
	return out
}

// gainExp returns the exposure of the first slot of filter f with the given gain.
func gainExp(slots []manifestSlot, f string, gain int64) int64 {
	for _, s := range slots {
		if s.Filter == f && s.Gain == gain {
			return s.ExposureMs
		}
	}
	return -1
}
