package scene3d

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/annotate"
	"github.com/verove-jordan/astronomy/internal/skycat"
)

func TestDiscInclination(t *testing.T) {
	tests := []struct {
		name string
		q    float64 // projected axis ratio b/a
		want float64 // degrees from face-on
		ok   bool
	}{
		{"perfectly round is face-on", 1.0, 0, true},
		{"a thin edge-on disc is the intrinsic ratio", discQ0, 90, true},
		{"typical inclined spiral", 0.5, 62.11, true},
		{"nearly edge-on", 0.25, 81.19, true},
		{"mildly inclined", 0.8, 37.76, true},
		{"flatter than any disc can be", 0.1, 0, false},
		{"nonsense input", -1, 0, false},
		{"impossible ratio", 1.5, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := discInclination(tt.q)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.InDelta(t, tt.want, got, 0.01)
			}
		})
	}
}

// TestDiscInclination_IsMonotonic pins the property the whole disc model rests on: a flatter ellipse
// always means a more inclined disc, with no fold-back anywhere in the usable range.
func TestDiscInclination_IsMonotonic(t *testing.T) {
	prev := -1.0
	for q := 1.0; q >= discQ0; q -= 0.01 {
		got, ok := discInclination(q)
		require.True(t, ok, "q = %.2f", q)
		assert.Greater(t, got, prev-1e-9, "inclination must rise as the ellipse flattens (q = %.2f)", q)
		prev = got
	}
}

func TestArcminToPc(t *testing.T) {
	// M42: 85′ across at 412 pc is about 10 pc — the textbook size of the Orion Nebula.
	assert.InDelta(t, 10.2, arcminToPc(85, 412), 0.3)
	// M31: 190′ at 778 kpc is about 43 kpc, the classic "twice the Milky Way" figure.
	assert.InDelta(t, 43000, arcminToPc(190, 778000), 1000)
}

func TestIsDiscGalaxy(t *testing.T) {
	for _, m := range []string{"Sb", "SA(s)b", "SBc", "S0", "Sd", "IB(s)m"} {
		assert.True(t, isDiscGalaxy(m), "%q is a disc", m)
	}
	for _, m := range []string{"E", "E+4", "E0", "", "cE"} {
		assert.False(t, isDiscGalaxy(m), "%q is not a disc", m)
	}
}

// galaxyLabel builds a labelled galaxy with a projected ellipse.
func galaxyLabel(name, morphology string, majArcmin, minArcmin float64) annotate.Label {
	return annotate.Label{
		Name: name, Kind: "dso", Type: "galaxy", Morphology: morphology,
		Diameter: majArcmin, MinorAxis: minArcmin,
		Extent: &annotate.Extent{RXpx: 400, RYpx: 200, AngleRad: math.Pi / 4},
	}
}

func TestShapeFor_Galaxies(t *testing.T) {
	t.Run("a spiral gets a measured inclination", func(t *testing.T) {
		// M31: 190′ × 60′, SA(s)b, at 778 kpc.
		s := shapeFor(galaxyLabel("M31", "SA(s)b", 190, 60), 778000)
		require.NotNil(t, s)
		assert.Equal(t, ShapeDisc, s.Kind)
		assert.Equal(t, ShapeMeasured, s.Source)
		// 190′ × 60′ gives q = 0.32, so the thin-disc relation puts it at 75.5° — within a couple of
		// degrees of the ~77° usually quoted for M31, which is about as well as this method ever does.
		assert.InDelta(t, 75.5, s.InclinationDeg, 1, "M31 is a highly inclined spiral")
		assert.True(t, s.FlipAmbiguous, "an ellipse cannot say which edge is nearer")
		assert.InDelta(t, 45, s.PositionAngleDeg, 0.01, "orientation comes from the projected footprint")
		assert.Contains(t, s.Note, "axis ratio")
	})

	t.Run("an elliptical is flagged as underdetermined", func(t *testing.T) {
		s := shapeFor(galaxyLabel("M87", "E+0-1", 8.3, 6.6), 16403000)
		require.NotNil(t, s)
		assert.Equal(t, ShapeAssumed, s.Source, "a spheroid's projection does not fix its 3D shape")
		assert.Contains(t, s.Note, "lower bound")
	})

	t.Run("a galaxy flatter than any disc gets no shape", func(t *testing.T) {
		assert.Nil(t, shapeFor(galaxyLabel("NGC1", "Sc", 10, 0.5), 30000000))
	})

	t.Run("no size means no shape", func(t *testing.T) {
		assert.Nil(t, shapeFor(galaxyLabel("NGC2", "Sc", 0, 0), 30000000))
	})
}

func TestShapeFor_Shells(t *testing.T) {
	pn := func(name string, maj, min float64) annotate.Label {
		return annotate.Label{
			Name: name, Kind: "dso", Type: "planetary_nebula",
			Diameter: maj, MinorAxis: min,
			Extent: &annotate.Extent{RXpx: 100, RYpx: 90},
		}
	}
	t.Run("a round planetary nebula becomes a shell", func(t *testing.T) {
		s := shapeFor(pn("NGC6543", 0.35, 0.3), 1012)
		require.NotNil(t, s)
		assert.Equal(t, ShapeShell, s.Kind)
		assert.Equal(t, ShapeAssumed, s.Source)
		assert.Greater(t, s.RadiusPc, 0.0)
		assert.Contains(t, s.Note, "the shell form is assumed")
	})
	t.Run("a strongly bipolar one is refused rather than forced", func(t *testing.T) {
		assert.Nil(t, shapeFor(pn("NGC6302", 12.9, 2.0), 1042),
			"a bipolar nebula is not a shell, and the flat plane is the more honest answer")
	})
}

func TestShapeFor_CuratedBeatsGeneric(t *testing.T) {
	m42 := annotate.Label{
		Name: "M42", Secondary: "Great Orion Nebula", Kind: "dso", Type: "emission_nebula",
		Diameter: 85, Extent: &annotate.Extent{RXpx: 2534, RYpx: 2534},
	}
	s := shapeFor(m42, 412)
	require.NotNil(t, s)
	assert.Equal(t, ShapeVolume, s.Kind)
	assert.Equal(t, ShapeModelled, s.Source, "no measurement of a nebula's depth exists")
	require.NotNil(t, s.Profile)
	assert.Greater(t, s.Profile.Bowl, 0.5, "M42 is a blister: a cavity opening toward the observer")
	assert.NotEmpty(t, s.Cite, "a curated shape must say whose structure it follows")
	assert.Contains(t, s.Note, "blister")

	t.Run("an uncurated nebula falls back to the stated generic assumption", func(t *testing.T) {
		other := annotate.Label{
			Name: "Sh2-155", Kind: "dso", Type: "emission_nebula",
			Diameter: 50, Extent: &annotate.Extent{RXpx: 900, RYpx: 700},
		}
		g := shapeFor(other, 750)
		require.NotNil(t, g)
		assert.Equal(t, ShapeModelled, g.Source)
		assert.Empty(t, g.Cite)
		assert.Zero(t, g.Profile.Bowl)
		assert.Contains(t, g.Note, "as deep as it is wide")
	})
}

func TestShapeFor_ClustersKeepTheirStars(t *testing.T) {
	// A cluster is already three-dimensional — its member stars ARE the object, each placed at its own
	// measured parallax. Wrapping a modelled shell around them would hide the one thing that is real.
	l := annotate.Label{
		Name: "M45", Kind: "dso", Type: "open_cluster",
		Diameter: 110, Extent: &annotate.Extent{RXpx: 700, RYpx: 700},
	}
	assert.Nil(t, shapeFor(l, 136))
}

// TestCuratedShapes_TableIsWellFormed guards the embedded CSV: a typo there fails silently at run
// time (one object quietly loses its shape), so it is checked here instead.
func TestCuratedShapes_TableIsWellFormed(t *testing.T) {
	table := curatedShapes()
	require.NotEmpty(t, table)
	for key, e := range table {
		assert.Contains(t, []string{ShapeVolume, ShapeShell, ShapeDisc}, e.kind, "%s: kind", key)
		assert.NotEmpty(t, e.cite, "%s: a curated shape must carry its citation", key)
		assert.NotEmpty(t, e.note, "%s: a curated shape must say what it models", key)
		assert.Greater(t, e.depthRel, 0.0, "%s: depth", key)
		assert.Greater(t, e.exponent, 0.0, "%s: exponent", key)
		assert.LessOrEqual(t, e.bowl, 1.0, "%s: bowl", key)
		assert.LessOrEqual(t, e.hollow, 1.0, "%s: hollow", key)
	}
	// The objects most likely to be photographed must resolve, by any of their designations.
	for _, name := range []string{"M42", "NGC1976", "M57", "NGC7293", "M8", "IC434"} {
		_, ok := table[skycat.Normalize(name)]
		assert.True(t, ok, "%s should have a curated shape", name)
	}
}
