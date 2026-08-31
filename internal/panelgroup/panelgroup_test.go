package panelgroup

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/pointing"
)

// TestGroup_RealSession is the acceptance test: the pointings in testdata are the ones our own
// parser read out of the 201 frames of the 2026-08-11 Milky Way arch session, and the grouping has
// to recover the eight tripod positions the photographer actually used.
//
// Calibration frames are excluded here on purpose. Group's contract is "lights only" — the darks in
// that session were shot without moving the tripod, so they sit at the same pointing as the panel
// before them and only their pixels give them away.
func TestGroup_RealSession(t *testing.T) {
	frames := loadSession(t)
	lights := selectRange(frames, "IMG_0892", "IMG_0902")
	lights = append(lights, selectRange(frames, "IMG_0927", "IMG_1048")...)
	require.Len(t, lights, 132, "the session holds 132 lights; the rest are darks and bias")

	panels := Group(lights, DefaultOptions())

	want := []struct {
		label       string
		count       int
		first, last string
		az, alt     float64
	}{
		{"p01", 11, "IMG_0892.DNG", "IMG_0902.DNG", 215.3, 39.3},
		{"p02", 27, "IMG_0927.DNG", "IMG_0953.DNG", 206.4, 16.2},
		{"p03", 18, "IMG_0954.DNG", "IMG_0971.DNG", 206.6, 39.5},
		{"p04", 15, "IMG_0972.DNG", "IMG_0986.DNG", 34.9, 49.8},
		{"p05", 8, "IMG_0987.DNG", "IMG_0994.DNG", 34.8, 63.2},
		// IMG_0995 was caught mid-re-aim, two degrees from either neighbour. It cannot stack alone,
		// so it is absorbed into whichever panel is closer, which is p06 by a quarter of a degree.
		{"p06", 6, "IMG_0995.DNG", "IMG_1001.DNG", 43.7, 63.1},
		{"p07", 16, "IMG_1002.DNG", "IMG_1017.DNG", 44.5, 74.1},
		{"p08", 31, "IMG_1018.DNG", "IMG_1048.DNG", 44.1, 75.6},
	}

	require.Len(t, panels, len(want), "one panel per tripod position")
	for i, w := range want {
		t.Run(w.label, func(t *testing.T) {
			got := panels[i]
			assert.Equal(t, w.label, got.Label)
			assert.Len(t, got.Frames, w.count)
			assert.Equal(t, w.first, got.Frames[0].Path)
			assert.Equal(t, w.last, got.Frames[len(got.Frames)-1].Path)
			assert.InDelta(t, w.az, got.Center.AzDeg, 0.3, "centre azimuth")
			assert.InDelta(t, w.alt, got.Center.AltDeg, 0.3, "centre altitude")
			assert.Less(t, got.SpreadDeg, 3.0, "a panel is one aim, not a drift")
		})
	}
}

// TestGroup_RealSession_NoFrameLost guards the merge and absorb passes, which shuffle slices around
// and would be easy to get subtly wrong.
func TestGroup_RealSession_NoFrameLost(t *testing.T) {
	frames := loadSession(t)
	lights := selectRange(frames, "IMG_0892", "IMG_0902")
	lights = append(lights, selectRange(frames, "IMG_0927", "IMG_1048")...)

	panels := Group(lights, DefaultOptions())

	seen := map[string]int{}
	for _, p := range panels {
		for _, f := range p.Frames {
			seen[f.Path]++
		}
	}
	assert.Len(t, seen, len(lights), "every light lands in exactly one panel")
	for path, n := range seen {
		assert.Equal(t, 1, n, "%s appears more than once", path)
	}
}

// TestGroup_SplitsOnRollAlone is the case that motivated putting roll in the rule at all: in the
// real session the camera was capped for darks without being moved, leaving a boundary with 0.1
// degrees of pointing change and 6.4 of roll. If classification ever lets such a frame through as a
// light, geometry must still refuse to stack it with the panel.
func TestGroup_SplitsOnRollAlone(t *testing.T) {
	base := time.Date(2026, 8, 11, 1, 0, 0, 0, time.UTC)
	frames := []Frame{
		{Path: "a1", At: base, Pointing: pointing.Frame{AzDeg: 44.1, AltDeg: 75.6, RollDeg: 2.2}},
		{Path: "a2", At: base.Add(time.Minute), Pointing: pointing.Frame{AzDeg: 44.1, AltDeg: 75.6, RollDeg: 2.3}},
		{Path: "b1", At: base.Add(2 * time.Minute), Pointing: pointing.Frame{AzDeg: 44.2, AltDeg: 75.5, RollDeg: 8.6}},
		{Path: "b2", At: base.Add(3 * time.Minute), Pointing: pointing.Frame{AzDeg: 44.2, AltDeg: 75.5, RollDeg: 8.7}},
	}

	panels := Group(frames, DefaultOptions())

	require.Len(t, panels, 2)
	assert.Equal(t, []string{"a1", "a2"}, pathsOf(panels[0]))
	assert.Equal(t, []string{"b1", "b2"}, pathsOf(panels[1]))
}

func TestGroup_Rules(t *testing.T) {
	base := time.Date(2026, 8, 11, 1, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		frames []Frame
		want   [][]string
	}{
		{
			name: "a slow drift stays one panel",
			// A tripod settling on soft ground moves further in total than the split threshold, but
			// never that far between two frames. This is the case the removed merge pass was meant
			// to cover, and the per-frame rule already covers it.
			frames: []Frame{
				{Path: "f1", At: base, Pointing: pointing.Frame{AzDeg: 206.0, AltDeg: 16.2}},
				{Path: "f2", At: base.Add(time.Minute), Pointing: pointing.Frame{AzDeg: 206.5, AltDeg: 16.2}},
				{Path: "f3", At: base.Add(2 * time.Minute), Pointing: pointing.Frame{AzDeg: 207.0, AltDeg: 16.2}},
				{Path: "f4", At: base.Add(3 * time.Minute), Pointing: pointing.Frame{AzDeg: 207.5, AltDeg: 16.2}},
			},
			want: [][]string{{"f1", "f2", "f3", "f4"}},
		},
		{
			name: "a deliberate re-aim stays split",
			frames: []Frame{
				{Path: "f1", At: base, Pointing: pointing.Frame{AzDeg: 206, AltDeg: 16}},
				{Path: "f2", At: base.Add(time.Minute), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 16}},
				{Path: "f3", At: base.Add(2 * time.Minute), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 39}},
				{Path: "f4", At: base.Add(3 * time.Minute), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 39}},
			},
			want: [][]string{{"f1", "f2"}, {"f3", "f4"}},
		},
		{
			name: "a long pause splits even when the aim did not change",
			frames: []Frame{
				{Path: "f1", At: base, Pointing: pointing.Frame{AzDeg: 206, AltDeg: 16}},
				{Path: "f2", At: base.Add(time.Minute), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 16}},
				{Path: "f3", At: base.Add(time.Hour), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 16}},
				{Path: "f4", At: base.Add(time.Hour + time.Minute), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 16}},
			},
			want: [][]string{{"f1", "f2"}, {"f3", "f4"}},
		},
		{
			name: "frames arriving out of order are still grouped by capture time",
			frames: []Frame{
				{Path: "f4", At: base.Add(3 * time.Minute), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 39}},
				{Path: "f1", At: base, Pointing: pointing.Frame{AzDeg: 206, AltDeg: 16}},
				{Path: "f3", At: base.Add(2 * time.Minute), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 39}},
				{Path: "f2", At: base.Add(time.Minute), Pointing: pointing.Frame{AzDeg: 206, AltDeg: 16}},
			},
			want: [][]string{{"f1", "f2"}, {"f3", "f4"}},
		},
		{
			name:   "no frames, no panels",
			frames: nil,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panels := Group(tt.frames, DefaultOptions())

			require.Len(t, panels, len(tt.want))
			for i, want := range tt.want {
				assert.Equal(t, want, pathsOf(panels[i]))
			}
		})
	}
}

func pathsOf(p Panel) []string {
	out := make([]string, 0, len(p.Frames))
	for _, f := range p.Frames {
		out = append(out, f.Path)
	}
	return out
}

// loadSession reads the pointing fixture produced from the real capture folder.
func loadSession(t *testing.T) []Frame {
	t.Helper()
	f, err := os.Open("testdata/session_2026_08_11.tsv")
	require.NoError(t, err)
	defer f.Close()

	var frames []Frame
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		col := strings.Split(line, "\t")
		require.Len(t, col, 9, "fixture row: %s", line)

		at, err := time.Parse(time.RFC3339, col[1])
		require.NoError(t, err)
		frames = append(frames, Frame{
			Path: col[0],
			At:   at,
			Pointing: pointing.Frame{
				AzDeg:   mustFloat(t, col[2]),
				AltDeg:  mustFloat(t, col[3]),
				RollDeg: mustFloat(t, col[4]),
			},
		})
	}
	require.NoError(t, sc.Err())
	return frames
}

// selectRange returns the fixture frames whose names fall between first and last inclusive, which
// is how the session's blocks are described everywhere else.
func selectRange(frames []Frame, first, last string) []Frame {
	var out []Frame
	for _, f := range frames {
		name := strings.TrimSuffix(f.Path, ".DNG")
		if name >= first && name <= last {
			out = append(out, f)
		}
	}
	return out
}

func mustFloat(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	require.NoError(t, err)
	return v
}
