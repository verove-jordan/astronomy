package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/store"
)

type fakeProvider struct {
	lights []store.FrameRow
	calib  []store.FrameRow // returned by RawCalibFrames, filtered by the query's SessionID
}

func (f *fakeProvider) PriorLightFrames(_ context.Context, _ store.LightQuery) ([]store.FrameRow, error) {
	return f.lights, nil
}
func (f *fakeProvider) RawCalibFrames(_ context.Context, q store.CalibQuery) ([]store.FrameRow, error) {
	var out []store.FrameRow
	for _, r := range f.calib {
		if q.SessionID == 0 || r.SessionID == q.SessionID {
			out = append(out, r)
		}
	}
	return out, nil
}

// currentInv builds an inventory with one L light set of two frames (the session being processed).
func currentInv() *inspect.Inventory {
	key := inspect.SetKey{Type: inspect.Light, Filter: "L", ExposureMs: 60000, Gain: 100, Offset: 50, Bin: 1, TempBucket: -10}
	frames := []*inspect.Frame{
		{Path: "/cur/L_001.fits", Type: inspect.Light, Filter: "L", ExposureMs: 60000, Gain: 100, Offset: 50, BinX: 1, BinY: 1, TempMilliC: -10000, HasTemp: true, Object: "M51"},
		{Path: "/cur/L_002.fits", Type: inspect.Light, Filter: "L", ExposureMs: 60000, Gain: 100, Offset: 50, BinX: 1, BinY: 1, TempMilliC: -10000, HasTemp: true, Object: "M51"},
	}
	return &inspect.Inventory{
		Frames: frames,
		Sets:   []inspect.Set{{Key: key, Frames: frames, Count: 2}},
	}
}

func priorRow(session int64, path, filter string) store.FrameRow {
	return store.FrameRow{
		SessionID: session, Path: path, FrameType: "LIGHT", Filter: filter,
		ExposureMs: 60000, Gain: 100, Offset: 50, Bin: 1, TempMilliC: -10000, HasTemp: true,
	}
}

// priorFile creates a real (empty) frame file for a prior row: buildReusePlan verifies prior paths
// still exist on disk (a freed/ghost path would sink its whole group's Siril link), so prior-frame
// fixtures must be real files. Current-session paths stay fake — the inventory is trusted as scanned.
func priorFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("fits"), 0o644))
	return p
}

func TestBuildReusePlan_FoldsPriorFrames(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{lights: []store.FrameRow{
		priorRow(2, priorFile(t, dir, "s2/L_001.fits"), "L"),
		priorRow(2, priorFile(t, dir, "s2/L_002.fits"), "L"),
		priorRow(3, priorFile(t, dir, "s3/R_001.fits"), "R"),
		priorRow(2, "/cur/L_001.fits", "L"), // duplicate of a current frame → must be dropped
	}}
	cfg := ReuseConfig{Provider: prov, ConeDeg: 0.5}

	plan, err := buildReusePlan(context.Background(), cfg, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)

	// L channel: current group + session-2 group.
	assert.Len(t, plan.byFilter["L"], 2)
	// R channel: only the session-3 prior group (no current R).
	assert.Len(t, plan.byFilter["R"], 1)

	assert.Equal(t, 2, plan.Summary.PriorSessions)
	assert.Equal(t, 3, plan.Summary.PriorFrames) // duplicate dropped, leaving 2×L + 1×R
	assert.Equal(t, int64(3*60000), plan.Summary.AddedIntegrationMs)
}

func TestBuildReusePlan_SessionSelection(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{lights: []store.FrameRow{
		priorRow(2, priorFile(t, dir, "s2/L_001.fits"), "L"),
		priorRow(3, priorFile(t, dir, "s3/L_001.fits"), "L"),
	}}
	cfg := ReuseConfig{Provider: prov, ConeDeg: 0.5, Sessions: map[int64]bool{2: true}} // only session 2

	plan, err := buildReusePlan(context.Background(), cfg, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)

	assert.Equal(t, 1, plan.Summary.PriorSessions)
	assert.Equal(t, 1, plan.Summary.PriorFrames)
	assert.Len(t, plan.byFilter["L"], 2) // current + session-2 only (session 3 excluded)
}

func TestBuildReusePlan_SkipsMissingPriorFrames(t *testing.T) {
	// A catalogued prior frame whose file is gone (e.g. freed after an S3 mirror) must be skipped AND
	// counted — one dangling path would otherwise sink its whole group's Siril link.
	dir := t.TempDir()
	prov := &fakeProvider{lights: []store.FrameRow{
		priorRow(2, priorFile(t, dir, "s2/L_001.fits"), "L"),
		priorRow(2, filepath.Join(dir, "s2/GONE.fits"), "L"),
	}}
	cfg := ReuseConfig{Provider: prov, ConeDeg: 0.5}

	plan, err := buildReusePlan(context.Background(), cfg, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)

	assert.Equal(t, 1, plan.Summary.PriorFrames, "only the frame that exists on disk is folded in")
	assert.Equal(t, 1, plan.MissingPrior, "the ghost is counted for the run warning")
}

func TestBuildReusePlan_NoProvider(t *testing.T) {
	plan, err := buildReusePlan(context.Background(), ReuseConfig{}, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)
	assert.Len(t, plan.byFilter["L"], 1) // current session only
	assert.Equal(t, 0, plan.Summary.PriorFrames)
}
