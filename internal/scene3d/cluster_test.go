package scene3d

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

func TestHalfSampleMode_FindsAPeakOnABroadBackground(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	var xs []float64
	for i := 0; i < 60; i++ { // the cluster: tight, around log10(136) = 2.134
		xs = append(xs, 2.134+rng.NormFloat64()*0.02)
	}
	for i := 0; i < 200; i++ { // the field: three decades of everything else
		xs = append(xs, 1.5+rng.Float64()*3)
	}
	rng.Shuffle(len(xs), func(i, j int) { xs[i], xs[j] = xs[j], xs[i] })

	assert.InDelta(t, 2.134, halfSampleMode(xs), 0.03,
		"the mode must follow the cluster, not the middle of the field")
}

func TestHalfSampleMode_DegenerateInputs(t *testing.T) {
	assert.Equal(t, 3.0, halfSampleMode([]float64{3}))
	assert.Equal(t, 3.5, halfSampleMode([]float64{3, 4}))
}

func TestInsideExtent(t *testing.T) {
	l := annotate.Label{X: 1000, Y: 800, Extent: &annotate.Extent{RXpx: 200, RYpx: 100, AngleRad: 0}}
	tests := []struct {
		name string
		x, y float64
		want bool
	}{
		{"the centre", 1000, 800, true},
		{"along the major axis, inside", 1180, 800, true},
		{"along the major axis, outside", 1220, 800, false},
		{"along the minor axis, inside", 1000, 890, true},
		{"along the minor axis, outside", 1000, 920, false},
		{"a corner of the bounding box is not in the ellipse", 1190, 890, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, insideExtent(l, tt.x, tt.y))
		})
	}
}

func TestInsideExtent_RespectsRotation(t *testing.T) {
	// The same ellipse turned a quarter turn: what was inside along x is now inside along y.
	l := annotate.Label{X: 0, Y: 0, Extent: &annotate.Extent{RXpx: 200, RYpx: 50, AngleRad: math.Pi / 2}}
	assert.True(t, insideExtent(l, 0, 180), "the major axis now runs along y")
	assert.False(t, insideExtent(l, 180, 0), "and no longer along x")
}

// clusterField plants a cluster inside a footprint on top of a field spanning three decades — the
// situation measureCluster exists for. Only the members share a distance; everything else is
// foreground and background that happens to lie in the same direction.
func clusterField(members, field int, distPc float64, l annotate.Label, seed int64) []annotate.Point {
	rng := rand.New(rand.NewSource(seed))
	inside := func() (float64, float64) {
		for {
			x := l.X + (rng.Float64()*2-1)*l.Extent.RXpx
			y := l.Y + (rng.Float64()*2-1)*l.Extent.RYpx
			if insideExtent(l, x, y) {
				return x, y
			}
		}
	}
	var out []annotate.Point
	add := func(x, y, d float64) {
		out = append(out, annotate.Point{X: int(x), Y: int(y), Star: &annotate.StarInfo{DistPc: d}})
	}
	for i := 0; i < members; i++ {
		x, y := inside()
		add(x, y, distPc*(1+rng.NormFloat64()*0.03))
	}
	for i := 0; i < field; i++ {
		x, y := inside()
		add(x, y, math.Pow(10, 1.5+rng.Float64()*3))
	}
	// Stars elsewhere in the frame at the cluster's distance must NOT be counted as members.
	for i := 0; i < 40; i++ {
		add(l.X+l.Extent.RXpx*4, l.Y, distPc)
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func TestMeasureCluster_RecoversThePlantedDistance(t *testing.T) {
	l := annotate.Label{
		Name: "M45", Kind: "dso", Type: "open_cluster", X: 1800, Y: 1300,
		Extent: &annotate.Extent{RXpx: 700, RYpx: 600},
	}
	points := clusterField(80, 60, 136, l, 31)

	fit, ok := measureCluster(l, points)
	require.True(t, ok)
	assert.InDelta(t, 136, fit.distPc, 136*0.06, "measured cluster distance")
	assert.GreaterOrEqual(t, fit.members, 60, "most planted members must be recovered")
	assert.Less(t, fit.members, 110, "the field must not be swept in with them")
	assert.Equal(t, fit.members, len(fit.memberOf))

	// Every flagged member really is at the cluster's distance and inside its footprint.
	for _, i := range fit.memberOf {
		assert.InDelta(t, 136, points[i].Star.DistPc, 136*0.25)
		assert.True(t, insideExtent(l, float64(points[i].X), float64(points[i].Y)))
	}
}

func TestMeasureCluster_RefusesWhatIsNotACluster(t *testing.T) {
	l := annotate.Label{
		Name: "NGC1", Kind: "dso", Type: "open_cluster", X: 1000, Y: 1000,
		Extent: &annotate.Extent{RXpx: 400, RYpx: 400},
	}
	t.Run("a bare field with no overdensity", func(t *testing.T) {
		_, ok := measureCluster(l, clusterField(0, 200, 136, l, 41))
		assert.False(t, ok, "three decades of field stars are not a cluster")
	})
	t.Run("too few stars to measure anything", func(t *testing.T) {
		_, ok := measureCluster(l, clusterField(4, 3, 136, l, 43))
		assert.False(t, ok)
	})
	t.Run("no footprint to search inside", func(t *testing.T) {
		bare := annotate.Label{Name: "NGC1", Kind: "dso", Type: "open_cluster"}
		_, ok := measureCluster(bare, clusterField(80, 20, 136, l, 45))
		assert.False(t, ok)
	})
}
