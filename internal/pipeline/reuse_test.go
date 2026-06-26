package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/store"
)

type fakeProvider struct {
	lights []store.FrameRow
}

func (f *fakeProvider) PriorLightFrames(_ context.Context, _ store.LightQuery) ([]store.FrameRow, error) {
	return f.lights, nil
}
func (f *fakeProvider) RawCalibFrames(_ context.Context, _ store.CalibQuery) ([]store.FrameRow, error) {
	return nil, nil
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

func TestBuildReusePlan_FoldsPriorFrames(t *testing.T) {
	prov := &fakeProvider{lights: []store.FrameRow{
		priorRow(2, "/s2/L_001.fits", "L"),
		priorRow(2, "/s2/L_002.fits", "L"),
		priorRow(3, "/s3/R_001.fits", "R"),
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
	prov := &fakeProvider{lights: []store.FrameRow{
		priorRow(2, "/s2/L_001.fits", "L"),
		priorRow(3, "/s3/L_001.fits", "L"),
	}}
	cfg := ReuseConfig{Provider: prov, ConeDeg: 0.5, Sessions: map[int64]bool{2: true}} // only session 2

	plan, err := buildReusePlan(context.Background(), cfg, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)

	assert.Equal(t, 1, plan.Summary.PriorSessions)
	assert.Equal(t, 1, plan.Summary.PriorFrames)
	assert.Len(t, plan.byFilter["L"], 2) // current + session-2 only (session 3 excluded)
}

func TestBuildReusePlan_NoProvider(t *testing.T) {
	plan, err := buildReusePlan(context.Background(), ReuseConfig{}, currentInv(), 1, targetQuery{Object: "M51"})
	require.NoError(t, err)
	assert.Len(t, plan.byFilter["L"], 1) // current session only
	assert.Equal(t, 0, plan.Summary.PriorFrames)
}
