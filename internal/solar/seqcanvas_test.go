package solar

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestPlanSequenceCanvas_PlacesPanelsEvenlyAlongTheLine(t *testing.T) {
	c, err := PlanSequenceCanvas(9, 300, 0.18, SequenceLayout{AngleDeg: 22, Spacing: 1.35, MaxEdge: 16000})
	require.NoError(t, err)

	require.Len(t, c.Centres, 9)
	assert.False(t, c.Shrunk)
	assert.Equal(t, CanonicalSide(300, 0.18, 1), c.Side)

	// Equal steps, and the middle panel sits at the middle of the sheet.
	step := math.Hypot(c.Centres[1][0]-c.Centres[0][0], c.Centres[1][1]-c.Centres[0][1])
	for i := 1; i < len(c.Centres); i++ {
		d := math.Hypot(c.Centres[i][0]-c.Centres[i-1][0], c.Centres[i][1]-c.Centres[i-1][1])
		assert.InDelta(t, step, d, 1e-6, "step %d", i)
	}
	assert.InDelta(t, 1.35*600, step, 1e-6, "the step is the spacing in solar diameters")
	assert.InDelta(t, float64(c.Width-1)/2, c.Centres[4][0], 1e-6)
	assert.InDelta(t, float64(c.Height-1)/2, c.Centres[4][1], 1e-6)

	// A line that climbs to the right runs to smaller row numbers.
	assert.Greater(t, c.Centres[8][0], c.Centres[0][0])
	assert.Less(t, c.Centres[8][1], c.Centres[0][1])
}

func TestPlanSequenceCanvas_ShrinksThePanelsRatherThanTheSheet(t *testing.T) {
	// Eleven panels at a 3000 px radius would be a 100k px sheet.
	c, err := PlanSequenceCanvas(11, 3000, 0.18, SequenceLayout{AngleDeg: 22, Spacing: 1.35, MaxEdge: 12000})
	require.NoError(t, err)

	assert.True(t, c.Shrunk)
	assert.LessOrEqual(t, c.Width, 12000)
	assert.LessOrEqual(t, c.Height, 12000)
	assert.Less(t, c.Radius, 3000.0, "the panels were reduced, not the finished sheet")
	assert.Greater(t, c.Radius, 0.0)
}

func TestPlanSequenceCanvas_RefusesNothingToLayOut(t *testing.T) {
	_, err := PlanSequenceCanvas(0, 300, 0.18, DefaultSequenceLayout())
	require.Error(t, err)

	_, err = PlanSequenceCanvas(5, 0, 0.18, DefaultSequenceLayout())
	require.Error(t, err)
}

func TestRenderSequence_KeepsEveryPanelAndSeamsNothing(t *testing.T) {
	const n = 5
	c, err := PlanSequenceCanvas(n, 60, 0.18, SequenceLayout{AngleDeg: 20, Spacing: 1.1, MaxEdge: 16000})
	require.NoError(t, err)

	// Each panel is a flat square of its own brightness, so where two overlap the brighter must win
	// outright — an average or a feather would leave an intermediate value.
	panels := make([]*fits.Image, n)
	for i := range panels {
		panels[i] = flatPanel(c.Side, float32(i+1)/10)
	}

	out, notes := RenderSequence(panels, c)

	assert.Empty(t, notes)
	require.Equal(t, c.Width, out.W)
	for i, centre := range c.Centres {
		got := out.Pix[0][int(centre[1])*out.W+int(centre[0])]
		assert.InDelta(t, float64(i+1)/10, float64(got), 1e-6, "panel %d survives at its own centre", i+1)
	}
	// Overlaps carry the brighter panel's value, never a blend of the two.
	for _, v := range out.Pix[0] {
		if v == 0 {
			continue
		}
		assert.True(t, isOneOfTheLevels(v, n), "unexpected blended value %v", v)
	}
}

func TestRenderSequence_SkipsAPanelOfTheWrongSizeRatherThanShiftingTheRest(t *testing.T) {
	c, err := PlanSequenceCanvas(3, 60, 0.18, DefaultSequenceLayout())
	require.NoError(t, err)

	panels := []*fits.Image{flatPanel(c.Side, 0.5), flatPanel(c.Side/2, 0.5), flatPanel(c.Side, 0.9)}
	out, notes := RenderSequence(panels, c)

	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "panel 2")
	// The third panel is still at the third position.
	got := out.Pix[0][int(c.Centres[2][1])*out.W+int(c.Centres[2][0])]
	assert.InDelta(t, 0.9, float64(got), 1e-6)
}

func flatPanel(side int, level float32) *fits.Image {
	im := fits.NewImage(side, side, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = level
	}
	return im
}

func isOneOfTheLevels(v float32, n int) bool {
	for i := 1; i <= n; i++ {
		if math.Abs(float64(v)-float64(i)/10) < 1e-6 {
			return true
		}
	}
	return false
}
