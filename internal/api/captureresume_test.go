package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/capture"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/store"
)

// Rebuilding what a session was started with, so finishing it later means finishing the SAME night.
//
// Everything asserted here is a setting that, if it came back wrong, would produce frames that
// cannot be stacked with the ones already on disk beside them — and nothing about the run would look
// wrong at the time.

func sessionRow(t *testing.T, req capture.Request) store.CaptureSession {
	t.Helper()
	full, err := json.Marshal(req)
	require.NoError(t, err)
	seq, err := json.Marshal(req.Sequence)
	require.NoError(t, err)
	return store.CaptureSession{
		ID: 42, Object: req.Object, Root: req.Root, Panel: req.Panel,
		MosaicPlanID: req.MosaicPlanID, TileIndex: -1,
		Sequence: seq, Request: full,
		SiteLat: req.LatDeg, SiteLon: req.LonDeg, SiteElevationM: req.ElevationM,
	}
}

func TestRequestFromSession_RestoresEverySettingTheNightWasShotWith(t *testing.T) {
	s := &Server{cfg: &config.Config{FocalLenMM: 740, PixelSizeUm: 3.8}}
	original := capture.Request{
		Sequence: capture.Sequence{Name: "narrowband", Interleave: true, RepeatBlock: 5, Steps: []capture.Step{
			{Filter: "Ha", Count: 30, ExposureUs: 300_000_000, Gain: 200, Offset: 50, Bin: 1, DitherN: 3},
		}},
		Root: "/data/M31", Object: "M31", Panel: "p02",
		Telescope: "RedCat 51", FocalMM: 250, PixelUm: 3.76,
		RADeg: 10.684, DecDeg: 41.269,
		LatDeg: 48.34, LonDeg: 2.76, ElevationM: 120,
		DitherRadiusPx: 12, ImageScaleArcsecPx: 3.102,
	}

	got, warning, err := s.requestFromSession(sessionRow(t, original))
	require.NoError(t, err)
	assert.Empty(t, warning)

	// The optics above all: a resumed run at the configured 740 mm instead of this rig's 250 mm
	// writes a wrong FOCALLEN into every header and hands the plate solver a 3x wrong scale.
	assert.Equal(t, "RedCat 51", got.Telescope)
	assert.Equal(t, 250.0, got.FocalMM)
	assert.Equal(t, 3.76, got.PixelUm)
	assert.Equal(t, 10.684, got.RADeg)
	assert.Equal(t, 41.269, got.DecDeg)
	assert.Equal(t, 12.0, got.DitherRadiusPx)
	assert.Equal(t, 3.102, got.ImageScaleArcsecPx)
	assert.Equal(t, original.Sequence, got.Sequence)
	assert.Equal(t, "M31", got.Object)
	assert.Equal(t, "/data/M31", got.Root)
	assert.Equal(t, "p02", got.Panel)
	assert.Equal(t, 48.34, got.LatDeg)
}

// A session predating the request column can still be finished, but the observer has to be told that
// its optics came from this machine's configuration rather than from the night itself.
func TestRequestFromSession_SaysWhenItIsGuessing(t *testing.T) {
	s := &Server{cfg: &config.Config{FocalLenMM: 740, PixelSizeUm: 3.8}}
	seq, err := json.Marshal(capture.Sequence{Steps: []capture.Step{
		{Filter: "L", Count: 20, ExposureUs: 60_000_000, Bin: 1},
	}})
	require.NoError(t, err)
	row := store.CaptureSession{
		ID: 7, Object: "M31", Root: "/data/M31", TileIndex: -1, Sequence: seq,
	}

	got, warning, err := s.requestFromSession(row)
	require.NoError(t, err)
	assert.Contains(t, warning, "current configuration")
	require.Len(t, got.Sequence.Steps, 1)
	assert.Equal(t, 20, got.Sequence.Steps[0].Count, "the plan itself still comes from the session")
	assert.Zero(t, got.FocalMM, "left blank here; launchCapture fills it from config and says so")
}

// A session with nothing recorded cannot be finished, and saying so beats starting an empty run.
func TestRequestFromSession_RefusesASessionWithNoPlan(t *testing.T) {
	s := &Server{cfg: &config.Config{}}

	_, _, err := s.requestFromSession(store.CaptureSession{ID: 9, TileIndex: -1})
	assert.Error(t, err)
}

// The columns own where the frames go. A request blob that disagreed with them would write into a
// folder the logbook does not point at, which is how a night goes missing.
func TestRequestFromSession_ColumnsWinForTheDestination(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	row := sessionRow(t, capture.Request{
		Sequence: capture.Sequence{Steps: []capture.Step{{Filter: "L", Count: 1, ExposureUs: 1, Bin: 1}}},
		Root:     "/stale/path", Object: "stale", Panel: "p99",
	})
	row.Root, row.Object, row.Panel = "/data/M31", "M31", "p02"

	got, _, err := s.requestFromSession(row)
	require.NoError(t, err)
	assert.Equal(t, "/data/M31", got.Root)
	assert.Equal(t, "M31", got.Object)
	assert.Equal(t, "p02", got.Panel)
}

// A mosaic tile keeps its index, and a session that is not part of one keeps none: TileIndex is a
// POINTER precisely so that tile 0 and "no tile" stay different things.
func TestRequestFromSession_TileIndex(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	base := sessionRow(t, capture.Request{
		Sequence: capture.Sequence{Steps: []capture.Step{{Filter: "L", Count: 1, ExposureUs: 1, Bin: 1}}},
	})

	t.Run("a tile keeps its index", func(t *testing.T) {
		row := base
		row.TileIndex = 0
		got, _, err := s.requestFromSession(row)
		require.NoError(t, err)
		require.NotNil(t, got.TileIndex)
		assert.Equal(t, 0, *got.TileIndex)
	})

	t.Run("a session outside a mosaic has none", func(t *testing.T) {
		row := base
		row.TileIndex = -1
		got, _, err := s.requestFromSession(row)
		require.NoError(t, err)
		assert.Nil(t, got.TileIndex)
	})
}

// The tally handed to Remaining is what the database aggregated, unchanged.
func TestFramesTallied(t *testing.T) {
	got := framesTallied([]store.CaptureFrameStat{
		{Filter: "L", FrameType: "light", Frames: 12},
		{Filter: "Ha", FrameType: "light", Frames: 5},
		{Filter: "", FrameType: "dark", Frames: 20},
	})

	assert.Equal(t, []capture.FrameTally{
		{Filter: "L", Type: "light", Count: 12},
		{Filter: "Ha", Type: "light", Count: 5},
		{Filter: "", Type: "dark", Count: 20},
	}, got)
}
