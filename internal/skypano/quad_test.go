package skypano

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQuadCode_InvariantToSimilarity is the property the whole solver rests on: the same four stars
// must hash the same however the picture is translated, rotated or scaled between two views.
func TestQuadCode_InvariantToSimilarity(t *testing.T) {
	pts := []Point{{100, 100}, {500, 420}, {260, 210}, {330, 300}}
	o := QuadOptions{MinDiameterPx: 10, MaxDiameterPx: 5000}

	base, ok := makeQuad(pts, [4]int{0, 1, 2, 3}, o)
	require.True(t, ok)

	for _, tt := range []struct {
		name           string
		scale, rotDeg  float64
		shiftX, shiftY float64
	}{
		{"identity", 1, 0, 0, 0},
		{"translated", 1, 0, -900, 1400},
		{"rotated 37 degrees", 1, 37, 0, 0},
		{"rotated 180 degrees", 1, 180, 0, 0},
		{"scaled up", 2.7, 0, 0, 0},
		{"scaled, rotated and moved", 0.4, 113, 300, -50},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.rotDeg * math.Pi / 180
			cos, sin := math.Cos(a), math.Sin(a)
			moved := make([]Point, len(pts))
			for i, p := range pts {
				moved[i] = Point{
					X: tt.scale*(p.X*cos-p.Y*sin) + tt.shiftX,
					Y: tt.scale*(p.X*sin+p.Y*cos) + tt.shiftY,
				}
			}
			got, ok := makeQuad(moved, [4]int{0, 1, 2, 3}, o)

			require.True(t, ok)
			for i := range base.Code {
				assert.InDelta(t, base.Code[i], got.Code[i], 1e-9, "code component %d", i)
			}
		})
	}
}

// TestQuadCode_ChangesUnderReflection pins the property that cost a long debugging session: a quad
// code is NOT mirror-invariant. A flipped image therefore matches nothing at all rather than
// matching imprecisely, which is why a row-order mistake reads as "no solution" instead of "poor
// solution" — see PriorCamera's rowsBottomUp.
func TestQuadCode_ChangesUnderReflection(t *testing.T) {
	pts := []Point{{100, 100}, {500, 420}, {260, 210}, {330, 300}}
	o := QuadOptions{MinDiameterPx: 10, MaxDiameterPx: 5000}
	base, ok := makeQuad(pts, [4]int{0, 1, 2, 3}, o)
	require.True(t, ok)

	mirrored := make([]Point, len(pts))
	for i, p := range pts {
		mirrored[i] = Point{X: p.X, Y: -p.Y}
	}
	got, ok := makeQuad(mirrored, [4]int{0, 1, 2, 3}, o)
	require.True(t, ok)

	same := true
	for i := range base.Code {
		if math.Abs(base.Code[i]-got.Code[i]) > 1e-6 {
			same = false
		}
	}
	assert.False(t, same, "a reflected asterism must hash differently, or a mirrored frame would solve")
}

// TestQuadCode_StableUnderRelabelling checks the symmetry breaking: the same four stars handed over
// in any order must produce one code.
func TestQuadCode_StableUnderRelabelling(t *testing.T) {
	pts := []Point{{100, 100}, {500, 420}, {260, 210}, {330, 300}}
	o := QuadOptions{MinDiameterPx: 10, MaxDiameterPx: 5000}
	want, ok := makeQuad(pts, [4]int{0, 1, 2, 3}, o)
	require.True(t, ok)

	for _, order := range [][4]int{{1, 0, 3, 2}, {2, 3, 0, 1}, {3, 1, 2, 0}, {2, 0, 1, 3}} {
		got, ok := makeQuad(pts, order, o)

		require.True(t, ok)
		for i := range want.Code {
			assert.InDelta(t, want.Code[i], got.Code[i], 1e-9, "order %v, component %d", order, i)
		}
	}
}

// TestSelectUniform_EvensOutDensity covers the fix for the neighbour-set problem: a field whose
// stars pile up in one region must not hand the solver a set that piles up too, or the two sides
// never agree locally about which stars are present.
func TestSelectUniform_EvensOutDensity(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var pts []Point
	// A dense clump in one corner plus a sparse scatter everywhere else, ordered brightest first.
	for i := 0; i < 2000; i++ {
		pts = append(pts, Point{rng.Float64() * 200, rng.Float64() * 200})
	}
	for i := 0; i < 200; i++ {
		pts = append(pts, Point{rng.Float64() * 800, rng.Float64() * 800})
	}

	idx := selectUniform(pts, 800, 800, 4, 4, 5)

	require.NotEmpty(t, idx)
	cells := map[[2]int]int{}
	for _, i := range idx {
		cells[[2]int{int(pts[i].X / 200), int(pts[i].Y / 200)}]++
	}
	for c, n := range cells {
		assert.LessOrEqual(t, n, 5, "cell %v took more than its share", c)
	}
	assert.Greater(t, len(cells), 8, "the sparse region must still be represented, not swamped by the clump")
}
