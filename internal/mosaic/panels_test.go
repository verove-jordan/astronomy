package mosaic

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

func lightAt(path string, raDeg, decDeg float64) inspect.Frame {
	return inspect.Frame{
		Path: path, Type: inspect.Light,
		ObjCtRA: fmt.Sprintf("%.6f", raDeg), ObjCtDec: fmt.Sprintf("%.6f", decDeg),
	}
}

func lightNoCoords(path string) inspect.Frame {
	return inspect.Frame{Path: path, Type: inspect.Light}
}

func TestPanelFolderRe(t *testing.T) {
	yes := []string{"p1", "p01", "P02", "panel_3", "panel-3", "panel 4", "panel5", "tile-4", "tile_6", "TILE7", "pa", "pH"}
	no := []string{"p123", "p", "planet", "tile-z", "panel", "01", "lights", "px9"}
	for _, s := range yes {
		assert.True(t, panelFolderRe.MatchString(s), "%q should match", s)
	}
	for _, s := range no {
		assert.False(t, panelFolderRe.MatchString(s), "%q should NOT match", s)
	}
}

func TestSegmentPanels_FolderLayout(t *testing.T) {
	frames := []inspect.Frame{
		lightNoCoords("/cap/p01/a.fit"),
		lightNoCoords("/cap/p01/b.fit"),
		lightNoCoords("/cap/P02/c.fit"),
		lightNoCoords("/cap/panel_3/sub/d.fit"),
		lightNoCoords("/cap/tile-4/e.fit"),
		{Path: "/cap/p01/dark.fit", Type: inspect.Dark}, // non-lights are ignored
	}
	panels, warns := SegmentPanels(frames, "/cap", SourceAuto, 0.15, nil)
	require.Len(t, panels, 4)
	assert.Empty(t, warns)
	assert.Equal(t, []string{"p01", "p02", "p03", "p04"}, panelLabels(panels))
	assert.True(t, panels[0].Paths["/cap/p01/a.fit"])
	assert.True(t, panels[0].Paths["/cap/p01/b.fit"])
	assert.False(t, panels[0].Paths["/cap/p01/dark.fit"])
	assert.True(t, panels[1].Paths["/cap/P02/c.fit"])
	assert.True(t, panels[2].Paths["/cap/panel_3/sub/d.fit"])
	assert.False(t, panels[0].HasCenter, "no headers, no center")
}

func TestSegmentPanels_PartialFoldersFallThroughToCoords(t *testing.T) {
	frames := []inspect.Frame{
		lightAt("/cap/p01/a.fit", 150.00, 20.00),
		lightAt("/cap/loose.fit", 150.00, 20.001), // outside any panel folder
		lightAt("/cap/p02/b.fit", 150.00, 20.12),
	}
	panels, warns := SegmentPanels(frames, "/cap", SourceFolders, 0.15, nil)
	require.Len(t, panels, 2, "coords clustering must take over for ALL lights")
	assert.Contains(t, strings.Join(warns, " | "), "outside any panel folder")
	assert.Len(t, panels[0].Paths, 2, "the loose light joins its pointing's cluster")
	assert.Len(t, panels[1].Paths, 1)
}

func TestSegmentPanels_Coords(t *testing.T) {
	const fov = 0.15
	tests := []struct {
		name       string
		frames     []inspect.Frame
		wantPanels int
		wantWarn   string
	}{
		{
			"two pointings beyond the join threshold split",
			[]inspect.Frame{
				lightAt("/c/a.fit", 150, 20),
				lightAt("/c/b.fit", 150, 20+0.8*fov),
			}, 2, "",
		},
		{
			"dither within 0.35 fov joins",
			[]inspect.Frame{
				lightAt("/c/a.fit", 150, 20),
				lightAt("/c/b.fit", 150, 20+0.3*fov),
			}, 1, "",
		},
		{
			"sexagesimal and decimal headers cluster together",
			[]inspect.Frame{
				{Path: "/c/a.fit", Type: inspect.Light, ObjCtRA: "13 29 52.7", ObjCtDec: "+47 11 43"},
				{Path: "/c/b.fit", Type: inspect.Light, ObjCtRA: "202.4696", ObjCtDec: "47.1953"},
			}, 1, "",
		},
		{
			"strays merge into the first panel",
			[]inspect.Frame{
				lightAt("/c/a.fit", 150, 20),
				lightAt("/c/b.fit", 150, 20.12),
				lightNoCoords("/c/lost.fit"),
			}, 2, "missing/unparsable",
		},
		{
			"no coords at all collapses to one panel",
			[]inspect.Frame{lightNoCoords("/c/a.fit"), lightNoCoords("/c/b.fit")}, 1, "no usable OBJCTRA",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panels, warns := SegmentPanels(tt.frames, "/c", SourceCoords, fov, nil)
			require.Len(t, panels, tt.wantPanels)
			joined := strings.Join(warns, " | ")
			if tt.wantWarn == "" {
				assert.Empty(t, warns)
			} else {
				assert.Contains(t, joined, tt.wantWarn)
			}
			total := 0
			for _, p := range panels {
				total += len(p.Paths)
			}
			assert.Equal(t, len(tt.frames), total, "every light lands in exactly one panel")
		})
	}
}

func TestSegmentPanels_Degenerate(t *testing.T) {
	panels, warns := SegmentPanels(nil, "/c", SourceAuto, 0.15, nil)
	assert.Nil(t, panels)
	assert.Contains(t, strings.Join(warns, " "), "no light frames")

	panels, warns = SegmentPanels([]inspect.Frame{lightAt("/c/a.fit", 1, 1)}, "/c", SourceCoords, 0, nil)
	require.Len(t, panels, 1)
	assert.Contains(t, strings.Join(warns, " "), "field-of-view")

	panels, warns = SegmentPanels([]inspect.Frame{lightAt("/c/a.fit", 1, 1)}, "/c", PanelSource("bogus"), 0.15, nil)
	require.Len(t, panels, 1)
	assert.Contains(t, strings.Join(warns, " "), "unknown panel source")
}

func TestSegmentPanels_PlanMatching(t *testing.T) {
	const fov = 0.15
	plan := &Plan{
		Name: "M31 2x1", Target: "M31", CenterRA: 150, CenterDec: 20.06,
		Tiles: []Tile{
			{Row: 0, Col: 0, Order: 1, Folder: "p01", RA: 150, Dec: 20},
			{Row: 0, Col: 1, Order: 2, Folder: "p02", RA: 150, Dec: 20.12},
			{Row: 0, Col: 2, Order: 3, Folder: "p03", RA: 150, Dec: 20.24}, // never captured
		},
	}
	frames := []inspect.Frame{
		lightAt("/c/b1.fit", 150, 20.121), // captured out of plan order
		lightAt("/c/a1.fit", 150, 20.001),
		lightAt("/c/a2.fit", 150, 20.002),
	}
	panels, warns := SegmentPanels(frames, "/c", SourceCoords, fov, plan)
	require.Len(t, panels, 2)
	assert.Equal(t, []string{"p01", "p02"}, panelLabels(panels), "plan order + tile labels")
	require.NotNil(t, panels[0].PlanTile)
	assert.Equal(t, 1, panels[0].PlanTile.Order)
	assert.Len(t, panels[0].Paths, 2)
	assert.Contains(t, strings.Join(warns, " | "), "p03")
}

func panelLabels(panels []Panel) []string {
	out := make([]string, len(panels))
	for i, p := range panels {
		out[i] = p.Label
	}
	return out
}
