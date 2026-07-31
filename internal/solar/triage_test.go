package solar

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probesAt builds still probes with the given measured radii, everything else nominal.
func probesAt(radii ...float64) []FrameProbe {
	out := make([]FrameProbe, 0, len(radii))
	for i, r := range radii {
		out = append(out, FrameProbe{
			Path: fmt.Sprintf("IMG_%04d.DNG", 700+i),
			Kind: KindStill, DiscOK: true,
			Disc:         Limb{R: r, ArcDeg: 360},
			OnDiscMedian: 0.1 + 0.001*float64(i%3), Detail: 0.02 + 0.0004*float64(i%3), LimbRatio: 1.2,
			TakenAtMs: int64(i) * 1000,
		})
	}
	return out
}

func radiiOf(g Group) []float64 {
	out := make([]float64, 0, len(g.Members))
	for _, m := range g.Members {
		out = append(out, m.Disc.R)
	}
	return out
}

// TestGroupByScale covers the clustering rule, which is the whole point of triage: a folder of
// mixed attempts has to fall apart along the lines where the setup actually changed.
func TestGroupByScale(t *testing.T) {
	tests := []struct {
		name  string
		radii []float64
		want  int
		note  string
	}{
		{"one configuration", []float64{1197, 1198, 1201, 1203}, 1, ""},
		{"two clearly separate", []float64{400, 402, 404, 900, 903}, 2, ""},
		{
			name:  "a continuous run stays together",
			radii: []float64{1081, 1090, 1096, 1098},
			want:  1,
			note: "anchoring each candidate to the group's first member would split this at an " +
				"arbitrary point; the gap rule keeps it whole",
		},
		{
			name:  "different resolutions, same scale, one group",
			radii: []float64{972, 975, 980, 984},
			want:  1,
			note:  "this is the 48MP-at-24mm vs 12MP-at-55mm case: metadata differs, scale does not",
		},
		{"a real gap splits", []float64{972, 975, 1040, 1043}, 2, ""},
		// Every step here is under the gap tolerance, yet the run spans 15% end to end. Where exactly
		// an unbroken ramp gets cut is arbitrary — the invariant that matters is the bounded spread,
		// which TestGroupByScale_SpreadBounded asserts. All this pins is that it does get cut.
		{"chaining is capped", []float64{1000, 1029, 1059, 1090, 1122, 1155}, 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := groupByScale(probesAt(tt.radii...), Options{})
			assert.Len(t, got, tt.want, tt.note)
			var total int
			for _, g := range got {
				total += len(g.Members)
				assert.NotEmpty(t, g.ID)
			}
			assert.Equal(t, len(tt.radii), total, "every probe must land in exactly one group")
		})
	}
}

// TestGroupByScale_SpreadBounded pins the invariant every later stage relies on: whatever the input,
// no group spans more scale than a single cheap resample can bridge.
func TestGroupByScale_SpreadBounded(t *testing.T) {
	radii := make([]float64, 0, 60)
	for r := 400.0; r < 1600; r *= 1.012 { // a deliberately unbroken ramp
		radii = append(radii, r)
	}
	for _, g := range groupByScale(probesAt(radii...), Options{}) {
		rs := radiiOf(g)
		require.NotEmpty(t, rs)
		assert.LessOrEqual(t, rs[len(rs)-1]/rs[0]-1, maxGroupSpread+1e-9,
			"group %s spans %.1f%%", g.ID, 100*(rs[len(rs)-1]/rs[0]-1))
	}
}

// TestGroupByScale_SeparatesKinds keeps stills and clips apart even at identical scale: the user
// asked for the best source to win on its own merits, not to be averaged with another.
func TestGroupByScale_SeparatesKinds(t *testing.T) {
	probes := probesAt(1000, 1002)
	probes = append(probes, FrameProbe{
		Path: "IMG_0723.MOV", Kind: KindVideo, DiscOK: true,
		Disc: Limb{R: 1001, ArcDeg: 360}, Video: &VideoInfo{Frames: 3000},
	})
	got := groupByScale(probes, Options{})
	require.Len(t, got, 2)
	kinds := map[Kind]bool{got[0].Kind: true, got[1].Kind: true}
	assert.True(t, kinds[KindStill] && kinds[KindVideo])
}

// TestGateGroup covers which frames survive, and — just as important — which are merely annotated.
func TestGateGroup(t *testing.T) {
	base := func() []FrameProbe {
		p := probesAt(1000, 1001, 1002, 1003, 1004, 1005, 1006, 1007)
		return p
	}

	t.Run("a clean group keeps everything", func(t *testing.T) {
		g := newGroup(KindStill, base())
		gateGroup(&g, Options{})
		assert.Equal(t, 8, g.Frames)
		assert.True(t, g.Stackable)
	})

	t.Run("a blown frame is rejected", func(t *testing.T) {
		p := base()
		p[3].ClippedFrac = 0.05
		g := newGroup(KindStill, p)
		gateGroup(&g, Options{})
		assert.Equal(t, 7, g.Frames)
		assert.Equal(t, ReasonClipped, firstRejection(g).Code)
	})

	t.Run("a far-under-exposed frame is rejected", func(t *testing.T) {
		p := base()
		p[2].OnDiscMedian = 0.005 // ~20x below its siblings
		g := newGroup(KindStill, p)
		gateGroup(&g, Options{})
		assert.Equal(t, 7, g.Frames)
		assert.Equal(t, ReasonTooDark, firstRejection(g).Code)
	})

	t.Run("a differently exposed frame is kept, with a note", func(t *testing.T) {
		// The session bracketed exposure deliberately. Photometric normalisation exists to handle
		// this; rejecting it would throw away most of a real folder.
		p := base()
		p[4].OnDiscMedian = 0.25
		g := newGroup(KindStill, p)
		gateGroup(&g, Options{})
		assert.Equal(t, 8, g.Frames, "no frame is dropped for exposure alone")
		m := memberByPath(g, p[4].Path)
		require.NotEmpty(t, m.Reasons)
		assert.Equal(t, ReasonExposure, m.Reasons[0].Code)
		assert.False(t, m.Reasons[0].Rejects)
	})

	t.Run("a badly soft frame is rejected but a slightly soft one is not", func(t *testing.T) {
		p := base()
		p[1].Detail = 0.002 // a tenth of its siblings
		p[5].Detail = 0.016 // merely softer
		g := newGroup(KindStill, p)
		gateGroup(&g, Options{})
		assert.Equal(t, 7, g.Frames)
		assert.True(t, memberByPath(g, p[1].Path).Rejected)
		assert.False(t, memberByPath(g, p[5].Path).Rejected)
	})

	t.Run("a partial disc with enough arc survives", func(t *testing.T) {
		p := base()
		p[0].Disc.Partial, p[0].Disc.ArcDeg = true, 95
		g := newGroup(KindStill, p)
		gateGroup(&g, Options{})
		assert.Equal(t, 8, g.Frames, "a limb close-up is a capture, not a fault")
		assert.Equal(t, ReasonEdgeClipped, memberByPath(g, p[0].Path).Reasons[0].Code)
	})

	t.Run("a partial disc with too little arc is rejected", func(t *testing.T) {
		p := base()
		p[0].Disc.Partial, p[0].Disc.ArcDeg = true, 25
		g := newGroup(KindStill, p)
		gateGroup(&g, Options{})
		assert.Equal(t, 7, g.Frames)
		assert.True(t, memberByPath(g, p[0].Path).Rejected)
	})

	t.Run("a small group is reported, not silently dropped", func(t *testing.T) {
		g := newGroup(KindStill, probesAt(1000, 1001))
		gateGroup(&g, Options{})
		assert.False(t, g.Stackable)
		assert.NotEmpty(t, g.Notes)
		assert.Len(t, g.Members, 2, "its files stay visible in the report")
	})
}

// TestAnnotateNeighbours checks the scale gap that tells a user whether merging two groups is cheap.
func TestAnnotateNeighbours(t *testing.T) {
	groups := []Group{
		{Kind: KindStill, DiscRadius: 1000},
		{Kind: KindStill, DiscRadius: 1020},
		{Kind: KindStill, DiscRadius: 2000},
	}
	annotateNeighbours(groups)
	assert.InDelta(t, 2.0, groups[0].NearestPct, 0.1)
	assert.InDelta(t, 1.96, groups[1].NearestPct, 0.1)
	assert.Greater(t, groups[2].NearestPct, 40.0)
}

func firstRejection(g Group) Reason {
	for _, m := range g.Members {
		for _, r := range m.Reasons {
			if r.Rejects {
				return r
			}
		}
	}
	return Reason{}
}

func memberByPath(g Group, path string) Member {
	for _, m := range g.Members {
		if m.Path == path {
			return m
		}
	}
	return Member{}
}
