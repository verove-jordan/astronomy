package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/store"
)

// fakeMasterLib is an in-memory calib.MasterStore for the plan preview (no DB, no writes expected).
type fakeMasterLib struct{ masters []calib.Master }

func (f *fakeMasterLib) ListMasters(context.Context) ([]calib.Master, error) { return f.masters, nil }
func (f *fakeMasterLib) SaveMaster(context.Context, calib.Master) error      { return nil }

// planScanner returns a fixed inventory (PreviewRunPlan's scan seam).
type planScanner struct{ inv *inspect.Inventory }

func (s planScanner) ScanMany(context.Context, []string, inspect.ScanOptions) (*inspect.Inventory, error) {
	return s.inv, nil
}

// TestPreviewRunPlan_JoinsSessionsAndCalibration: the plan groups exactly like the run (current +
// prior sessions), resolves current masters from library∪capture candidates, and resolves a prior
// group's flat through the SAME sessionFlatPaths rows the run will use ("session-rebuild" + count).
func TestPreviewRunPlan_JoinsSessionsAndCalibration(t *testing.T) {
	dir := t.TempDir()
	inv := currentInv() // one current L set (gain 100, 60 s, -10 °C)
	// A capture-built bias candidate: the current inventory carries its own bias frames.
	biasKey := inspect.SetKey{Type: inspect.Bias, Gain: 100, Offset: 50, Bin: 1}
	inv.Sets = append(inv.Sets, inspect.Set{Key: biasKey, Count: 20,
		Frames: []*inspect.Frame{{Type: inspect.Bias, Gain: 100, Offset: 50, BinX: 1}}})

	prior := nightMs(t, "2023-03-15")
	lightRow := priorRow(2, priorFile(t, dir, "s2/L_001.fits"), "L")
	lightRow.DateObsMs = prior
	prov := &fakeProvider{
		lights: []store.FrameRow{lightRow},
		calib: []store.FrameRow{
			{SessionID: 2, Path: priorFile(t, dir, "s2/flat1.fits"), FrameType: "FLAT", Filter: "L",
				Gain: 100, Offset: 50, Bin: 1, DateObsMs: prior},
			{SessionID: 2, Path: priorFile(t, dir, "s2/flat2.fits"), FrameType: "FLAT", Filter: "L",
				Gain: 100, Offset: 50, Bin: 1, DateObsMs: prior},
		},
	}
	lib := &fakeMasterLib{masters: []calib.Master{{
		Type: calib.MasterFlat, Filter: "L", ExposureMs: 5, Gain: 100, Offset: 50, Bin: 1,
		FrameCount: 30, Path: "/library/master_FLAT_L.fits",
	}}}

	plan, err := PreviewRunPlan(context.Background(), prov, planScanner{inv}, lib,
		[]string{dir}, "", 0.5, false, nil)
	require.NoError(t, err)

	// Sessions: the current capture + the prior session's night, current first.
	require.Len(t, plan.Sessions, 2)
	assert.True(t, plan.Sessions[0].Current)
	assert.Equal(t, int64(2), plan.Sessions[1].SessionID)
	assert.Equal(t, "2023-03-15", plan.Sessions[1].Session)

	require.Len(t, plan.Channels, 1)
	groups := plan.Channels[0].Groups
	require.Len(t, groups, 2, "current group + the prior session's night group")

	var cur, pri *PlanGroup
	for i := range groups {
		if groups[i].Current {
			cur = &groups[i]
		} else {
			pri = &groups[i]
		}
	}
	require.NotNil(t, cur)
	require.NotNil(t, pri)

	// Current group: the library flat matches; the bias is a capture-built candidate (no path).
	require.NotNil(t, cur.Flat)
	assert.Equal(t, planSourceLibrary, cur.Flat.Source)
	assert.Equal(t, calib.SuggestID(inspect.SetKey{Type: inspect.Light, Filter: "L", ExposureMs: 60000,
		Gain: 100, Offset: 50, Bin: 1, TempBucket: -10}, calib.RoleFlat), cur.Flat.SuggestID,
		"the exclusion key matches the calibration preview's identity")
	require.NotNil(t, cur.Bias)
	assert.Equal(t, planSourceCapture, cur.Bias.Source)

	// Prior group: the flat is a per-night session rebuild over exactly the provider's on-disk rows.
	require.NotNil(t, pri.Flat)
	assert.Equal(t, planSourceSession, pri.Flat.Source)
	assert.Equal(t, 2, pri.Flat.RawFlats, "counts the SAME rows sessionFlatPaths gives the run")
	assert.Equal(t, "2023-03-15", pri.Session)
}

// TestPreviewRunPlan_NoProviderCurrentOnly: reuse off → a current-capture-only plan, no error.
func TestPreviewRunPlan_NoProviderCurrentOnly(t *testing.T) {
	plan, err := PreviewRunPlan(context.Background(), nil, planScanner{currentInv()}, &fakeMasterLib{},
		nil, "", 0.5, false, nil)
	require.NoError(t, err)
	require.Len(t, plan.Channels, 1)
	require.Len(t, plan.Channels[0].Groups, 1)
	assert.True(t, plan.Channels[0].Groups[0].Current)
	assert.Zero(t, plan.Reuse.PriorFrames)
}
