package pipeline

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/store"
)

// nightMs returns an epoch-ms instant that NightKey buckets into the given local night (22:00 local
// of that date — evening side, unambiguous regardless of the machine TZ used by NightKey).
func nightMs(t *testing.T, night string) int64 {
	t.Helper()
	tt, err := time.ParseInLocation("2006-01-02", night, time.Local)
	require.NoError(t, err)
	return tt.Add(22 * time.Hour).UnixMilli()
}

// TestBuildReusePlan_TwoPriorSessionsSameConfigStaySeparate is the latent-bug regression: two
// DIFFERENT prior sessions with an identical capture config used to merge into one group stamped
// with the FIRST session's id — so the second session's frames got the first session's flat.
func TestBuildReusePlan_TwoPriorSessionsSameConfigStaySeparate(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{lights: []store.FrameRow{
		priorRow(2, priorFile(t, dir, "s2/L_001.fits"), "L"),
		priorRow(3, priorFile(t, dir, "s3/L_001.fits"), "L"), // same config, DIFFERENT session
	}}
	cfg := ReuseConfig{Provider: prov, ConeDeg: 0.5}

	plan, err := buildReusePlan(context.Background(), cfg, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)

	require.Len(t, plan.byFilter["L"], 3, "current + one group PER prior session (never merged)")
	ids := []int64{}
	for _, g := range plan.byFilter["L"] {
		if !g.Current {
			ids = append(ids, g.SessionID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	assert.Equal(t, []int64{2, 3}, ids, "each prior session keeps its own group → its own flat")
}

// TestBuildReusePlan_SplitsPriorSessionByNight: one prior session whose frames span two capture
// nights forms one group per night (per-night flats), each labeled with its night key.
func TestBuildReusePlan_SplitsPriorSessionByNight(t *testing.T) {
	dir := t.TempDir()
	rowAt := func(path string, ms int64) store.FrameRow {
		r := priorRow(2, path, "L")
		r.DateObsMs = ms
		return r
	}
	prov := &fakeProvider{lights: []store.FrameRow{
		rowAt(priorFile(t, dir, "s2/a.fits"), nightMs(t, "2023-02-27")),
		rowAt(priorFile(t, dir, "s2/b.fits"), nightMs(t, "2023-02-27")),
		rowAt(priorFile(t, dir, "s2/c.fits"), nightMs(t, "2023-03-15")),
	}}
	cfg := ReuseConfig{Provider: prov, ConeDeg: 0.5}

	plan, err := buildReusePlan(context.Background(), cfg, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)

	var prior []lightGroup
	for _, g := range plan.byFilter["L"] {
		if !g.Current {
			prior = append(prior, g)
		}
	}
	require.Len(t, prior, 2, "one group per capture night within the session")
	sort.Slice(prior, func(i, j int) bool { return prior[i].Session < prior[j].Session })
	assert.Equal(t, "2023-02-27", prior[0].Session)
	assert.Len(t, prior[0].Frames, 2)
	assert.Equal(t, "2023-03-15", prior[1].Session)
	assert.Len(t, prior[1].Frames, 1)

	require.Len(t, plan.Summary.Sessions, 1)
	assert.Equal(t, []string{"2023-02-27", "2023-03-15"}, plan.Summary.Sessions[0].Nights,
		"the reuse summary lists the session's distinct nights")
}

// TestBuildReusePlan_UndatedPriorRowsKeepSingleGroup: prior rows with no DATE-OBS (older catalog
// entries) keep today's one-group-per-config behavior — the night key stays empty.
func TestBuildReusePlan_UndatedPriorRowsKeepSingleGroup(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{lights: []store.FrameRow{
		priorRow(2, priorFile(t, dir, "s2/a.fits"), "L"),
		priorRow(2, priorFile(t, dir, "s2/b.fits"), "L"),
	}}
	cfg := ReuseConfig{Provider: prov, ConeDeg: 0.5}

	plan, err := buildReusePlan(context.Background(), cfg, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)

	var prior []lightGroup
	for _, g := range plan.byFilter["L"] {
		if !g.Current {
			prior = append(prior, g)
		}
	}
	require.Len(t, prior, 1)
	assert.Empty(t, prior[0].Session)
	assert.Len(t, prior[0].Frames, 2)
	assert.Empty(t, plan.Summary.Sessions[0].Nights, "no nights advertised for undated rows")
}

// anchorTestGroup builds a lightGroup with n placeholder frames for anchor-ranking tests.
func anchorTestGroup(session string, current bool, filter string, frames int) lightGroup {
	fs := make([]*inspect.Frame, frames)
	for i := range fs {
		fs[i] = &inspect.Frame{Type: inspect.Light, Filter: filter}
	}
	return lightGroup{Current: current, Session: session, Filter: filter, Frames: fs}
}

// TestReusePlan_ComputeAnchor pins the anchor-night ranking: grouped runs anchor on the night with
// current data first, then the most channels, then the most frames, then the latest date — and a
// single-group-per-channel run never anchors (the fast path stays untouched).
func TestReusePlan_ComputeAnchor(t *testing.T) {
	cases := []struct {
		name        string
		byFilter    map[string][]lightGroup
		anchored    bool
		anchorNight string
		nights      []string
	}{
		{
			name: "single group per channel never anchors",
			byFilter: map[string][]lightGroup{
				"L": {anchorTestGroup("2023-02-27", true, "L", 10)},
				"R": {anchorTestGroup("2023-02-27", true, "R", 10)},
			},
			anchored: false, nights: []string{"2023-02-27"},
		},
		{
			name: "night covering more channels wins (task #312 shape)",
			byFilter: map[string][]lightGroup{
				"L": {anchorTestGroup("2020-04-26", true, "L", 30), anchorTestGroup("2023-02-27", true, "L", 100)},
				"R": {anchorTestGroup("2020-04-26", true, "R", 10), anchorTestGroup("2023-02-27", true, "R", 100)},
				"G": {anchorTestGroup("2023-02-27", true, "G", 100)},
				"B": {anchorTestGroup("2023-02-27", true, "B", 85)},
			},
			anchored: true, anchorNight: "2023-02-27", nights: []string{"2020-04-26", "2023-02-27"},
		},
		{
			name: "channel tie → more light frames wins",
			byFilter: map[string][]lightGroup{
				"L": {anchorTestGroup("2023-01-01", true, "L", 20), anchorTestGroup("2023-01-02", true, "L", 60)},
			},
			anchored: true, anchorNight: "2023-01-02", nights: []string{"2023-01-01", "2023-01-02"},
		},
		{
			name: "current capture outranks a bigger prior-only night",
			byFilter: map[string][]lightGroup{
				"L": {anchorTestGroup("2023-01-01", true, "L", 10), anchorTestGroup("2022-06-01", false, "L", 200)},
				"R": {anchorTestGroup("2022-06-01", false, "R", 200)},
			},
			anchored: true, anchorNight: "2023-01-01", nights: []string{"2022-06-01", "2023-01-01"},
		},
		{
			name: "full tie → latest night wins",
			byFilter: map[string][]lightGroup{
				"L": {anchorTestGroup("2023-01-01", true, "L", 10), anchorTestGroup("2023-03-01", true, "L", 10)},
			},
			anchored: true, anchorNight: "2023-03-01", nights: []string{"2023-01-01", "2023-03-01"},
		},
		{
			name: "undated grouped data anchors with an empty night key",
			byFilter: map[string][]lightGroup{
				"L": {anchorTestGroup("", true, "L", 10), anchorTestGroup("", false, "L", 20)},
			},
			anchored: true, anchorNight: "", nights: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := &ReusePlan{byFilter: tc.byFilter}
			plan.computeAnchor()
			assert.Equal(t, tc.anchored, plan.Anchored)
			assert.Equal(t, tc.anchorNight, plan.AnchorNight)
			assert.Equal(t, tc.nights, plan.Nights)
		})
	}
}

// TestAnchorGroupIndex pins the per-channel anchor-group choice: anchor-night membership beats
// everything, then current data, then size, then the later night.
func TestAnchorGroupIndex(t *testing.T) {
	cases := []struct {
		name        string
		groups      []lightGroup
		anchorNight string
		want        int
	}{
		{
			name: "anchor-night group beats a bigger other-night group",
			groups: []lightGroup{
				anchorTestGroup("2020-04-26", true, "L", 300),
				anchorTestGroup("2023-02-27", true, "L", 30),
			},
			anchorNight: "2023-02-27", want: 1,
		},
		{
			name: "channel absent from the anchor night → biggest current group",
			groups: []lightGroup{
				anchorTestGroup("2020-04-26", false, "R", 90),
				anchorTestGroup("2020-05-01", true, "R", 40),
			},
			anchorNight: "2023-02-27", want: 1,
		},
		{
			name: "same night and currency → more frames",
			groups: []lightGroup{
				anchorTestGroup("2023-02-27", true, "L", 10),
				anchorTestGroup("2023-02-27", true, "L", 90),
			},
			anchorNight: "2023-02-27", want: 1,
		},
		{
			name: "single group is its own anchor",
			groups: []lightGroup{
				anchorTestGroup("2020-04-26", true, "G", 100),
			},
			anchorNight: "2023-02-27", want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, anchorGroupIndex(tc.groups, tc.anchorNight))
		})
	}
}

// TestUseFastPath pins the channel dispatch: an anchored run (any channel with ≥2 groups) routes
// EVERY channel through the grouped path — the task #312 G/B channels took the fast path next to
// anchored L/R and produced mixed-dimension masters that killed the combine.
func TestUseFastPath(t *testing.T) {
	mk := func(bf map[string][]lightGroup) *ReusePlan {
		p := &ReusePlan{byFilter: bf}
		p.computeAnchor()
		return p
	}
	t.Run("plain single-night channel keeps the fast path", func(t *testing.T) {
		plan := mk(map[string][]lightGroup{"L": {anchorTestGroup("2023-02-27", true, "L", 10)}})
		assert.True(t, useFastPath(plan, plan.byFilter["L"]))
	})
	t.Run("a lone prior-session group stays grouped (needs its own session flat)", func(t *testing.T) {
		plan := mk(map[string][]lightGroup{"L": {anchorTestGroup("", false, "L", 10)}})
		assert.False(t, useFastPath(plan, plan.byFilter["L"]))
	})
	t.Run("an anchored run routes even single-group channels through the grouped path", func(t *testing.T) {
		plan := mk(map[string][]lightGroup{
			"L": {anchorTestGroup("2020-04-26", true, "L", 30), anchorTestGroup("2023-02-27", true, "L", 100)},
			"G": {anchorTestGroup("2023-02-27", true, "G", 100)},
		})
		assert.False(t, useFastPath(plan, plan.byFilter["G"]), "G must land on the same anchor canvas as L")
		assert.False(t, useFastPath(plan, plan.byFilter["L"]))
	})
}

// TestSessionFlatPaths pins the pure night-selection helper the run and the pre-run plan share.
func TestSessionFlatPaths(t *testing.T) {
	dir := t.TempDir()
	flatRow := func(path, filter string, ms int64) store.FrameRow {
		return store.FrameRow{SessionID: 2, Path: path, FrameType: "FLAT", Filter: filter, DateObsMs: ms}
	}
	n1 := nightMs(t, "2023-02-27")
	n2 := nightMs(t, "2023-03-15")
	a := priorFile(t, dir, "flats/a.fits")
	b := priorFile(t, dir, "flats/b.fits")
	c := priorFile(t, dir, "flats/c.fits")

	t.Run("exact night wins", func(t *testing.T) {
		rows := []store.FrameRow{flatRow(a, "L", n1), flatRow(b, "L", n2)}
		paths, missing, note := sessionFlatPaths(rows, "L", "2023-02-27")
		assert.Equal(t, []string{a}, paths)
		assert.Zero(t, missing)
		assert.Empty(t, note)
	})
	t.Run("no night filter → every on-disk flat (today's behavior)", func(t *testing.T) {
		rows := []store.FrameRow{flatRow(a, "L", n1), flatRow(b, "L", n2)}
		paths, _, note := sessionFlatPaths(rows, "L", "")
		assert.Len(t, paths, 2)
		assert.Empty(t, note)
	})
	t.Run("other-night fallback carries the dust warning", func(t *testing.T) {
		rows := []store.FrameRow{flatRow(b, "L", n2)}
		paths, _, note := sessionFlatPaths(rows, "L", "2023-02-27")
		assert.Equal(t, []string{b}, paths)
		assert.Contains(t, note, "dust may have moved")
	})
	t.Run("wrong filter and missing files drop out", func(t *testing.T) {
		rows := []store.FrameRow{
			flatRow(c, "R", n1), // wrong filter
			flatRow(filepath.Join(dir, "flats/GONE.fits"), "L", n1), // freed to S3
		}
		paths, missing, note := sessionFlatPaths(rows, "L", "2023-02-27")
		assert.Empty(t, paths)
		assert.Equal(t, 1, missing)
		assert.Empty(t, note)
	})
}
