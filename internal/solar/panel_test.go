package solar

import (
	"math"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// synthPanel builds the geometry a camera rolled by rollDeg would have recorded for a Moon at the
// given true position angle, optionally through a mirroring train.
func synthPanel(source string, skyPA, rollDeg float64, mirrored bool) PanelFrame {
	const sep = 210.0
	f := PanelFrame{Source: source, SkyPADeg: skyPA, Flatten: 1,
		Sun: Limb{CX: 750, CY: 750, R: 300}, Moon: Limb{R: 308}}
	// Invert what rollFor does: place the occulter where a camera at this roll would have seen it.
	theta := skyAngle(skyPA) - rollDeg*math.Pi/180
	if mirrored {
		theta = math.Pi - theta
	}
	f.Moon.CX = f.Sun.CX + sep*math.Cos(theta)
	f.Moon.CY = f.Sun.CY + sep*math.Sin(theta)
	return f
}

func TestSolveOrientation_RecoversRollAndParity(t *testing.T) {
	tests := []struct {
		name     string
		mirrored bool
		roll     float64
	}{
		{"direct train, camera upright", false, 0},
		{"direct train, camera rolled", false, 37},
		{"mirroring train, camera rolled", true, -112},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// One clip spanning the Moon's swing through maximum, as the 12 Aug hero clip does.
			var frames []PanelFrame
			for _, pa := range []float64{280, 250, 200, 160, 136} {
				frames = append(frames, synthPanel("hero.MOV", pa, tt.roll, tt.mirrored))
			}

			got, notes := SolveOrientation(frames)

			require.Len(t, got, len(frames))
			assert.Empty(t, notes)
			for i, o := range got {
				assert.Equal(t, tt.mirrored, o.Mirrored, "panel %d", i)
				assert.InDelta(t, tt.roll, o.RollDeg, 0.01, "panel %d", i)
				assert.Less(t, o.Residual, 0.01, "a steady camera leaves no residual")
			}
		})
	}
}

func TestSolveOrientation_ParityNeedsASweepNotJustTwoPanels(t *testing.T) {
	// Two panels at nearly the same position angle: both handedness hypotheses look equally steady,
	// so the answer must not be trusted blindly. What matters is that it still returns a usable roll
	// for the handedness it picked.
	frames := []PanelFrame{
		synthPanel("a.MOV", 280, 25, false),
		synthPanel("a.MOV", 279, 25, false),
	}
	got, _ := SolveOrientation(frames)

	require.Len(t, got, 2)
	assert.InDelta(t, got[0].RollDeg, got[1].RollDeg, 1.0, "the two rolls agree whichever parity won")
}

func TestSolveOrientation_SaysSoWhenNoClipCanDecideParity(t *testing.T) {
	frames := []PanelFrame{
		synthPanel("a.MOV", 280, 10, false),
		synthPanel("b.MOV", 136, 10, false),
	}
	_, notes := SolveOrientation(frames)

	require.NotEmpty(t, notes)
	assert.Contains(t, notes[0], "handedness could not be measured")
}

func TestSolveOrientation_UnfittedOcculterBorrowsAnOrientation(t *testing.T) {
	// A shallow bite near contact is exactly where the two-circle fit gives up, and it lands on the
	// panels a sequence most wants: the first bite and the last. Leaving such a panel unrotated puts
	// a crescent facing an arbitrary direction into an otherwise straightened row, so it takes the
	// roll of its nearest neighbour instead — the roll belongs to the camera, not to the phase.
	t.Run("prefers a panel from the same clip", func(t *testing.T) {
		frames := []PanelFrame{
			synthPanel("hero.MOV", 280, 20, false),
			{Source: "hero.MOV", Sun: Limb{CX: 750, CY: 750, R: 300}, SkyPADeg: 200, Flatten: 1},
			synthPanel("other.MOV", 136, -55, false),
		}
		got, notes := SolveOrientation(frames)

		require.Len(t, got, 3)
		assert.InDelta(t, 20, got[1].RollDeg, 0.01, "the same clip's roll, not the other clip's")
		assert.True(t, got[1].Inherited)
		// Not notes[0]: with the two measurable panels in different clips no clip can settle the
		// handedness, so the parity note comes first.
		assert.True(t, anyNoteContains(notes, "the same clip"), "notes: %v", notes)
	})

	t.Run("falls back to another clip and says so", func(t *testing.T) {
		frames := []PanelFrame{
			synthPanel("hero.MOV", 280, 20, false),
			synthPanel("hero.MOV", 200, 20, false),
			{Source: "edge.MOV", Sun: Limb{CX: 750, CY: 750, R: 300}, SkyPADeg: 120, Flatten: 1},
		}
		got, notes := SolveOrientation(frames)

		require.Len(t, got, 3)
		assert.InDelta(t, 20, got[2].RollDeg, 0.01)
		assert.True(t, got[2].Inherited)
		assert.True(t, anyNoteContains(notes, "another clip"), "notes: %v", notes)
	})

	t.Run("with nothing to borrow from it keeps the camera's own orientation", func(t *testing.T) {
		frames := []PanelFrame{{Source: "only.MOV", Sun: Limb{CX: 750, CY: 750, R: 300}, SkyPADeg: 120, Flatten: 1}}
		got, notes := SolveOrientation(frames)

		require.Len(t, got, 1)
		assert.Zero(t, got[0].RollDeg)
		assert.False(t, got[0].Inherited)
		assert.True(t, anyNoteContains(notes, "no other panel to borrow"), "notes: %v", notes)
	})
}

// panelScene renders the frame's own geometry as a synthetic Halpha disc, so the warp is measured on
// pixels rather than on the numbers that produced them.
func panelScene(f PanelFrame) *fits.Image {
	s := defaultSun()
	s.w, s.h = 1500, 1500
	s.cx, s.cy, s.r = f.Sun.CX, f.Sun.CY, f.Sun.R
	s.moonCX, s.moonCY, s.moonR = f.Moon.CX, f.Moon.CY, f.Moon.R
	s.features, s.proms = 14, 0
	s.noise, s.ringAmp, s.gradAmp = 0, 0, 0
	return drawSun(s)
}

func TestWarpPanel_PutsTheOcculterWhereTheSkySaysItWas(t *testing.T) {
	// A panel recorded through a rolled, mirroring train must come out with the Moon at its true
	// position angle, in the shared convention: North up, East left.
	const (
		skyPA = 300.0
		roll  = 64.0
	)
	f := synthPanel("hero.MOV", skyPA, roll, true)
	im := panelScene(f)

	const outR = 200.0
	const side = 640
	out := WarpPanel(im, f, Orientation{RollDeg: roll, Mirrored: true}, outR, side)

	got, ok := FitPair(out)
	require.True(t, ok)
	require.True(t, got.Eclipsed(), "the occulter must survive the resample")

	half := float64(side-1) / 2
	assert.InDelta(t, half, got.Sun.CX, 2.0, "the Sun lands in the middle")
	assert.InDelta(t, half, got.Sun.CY, 2.0)
	assert.InDelta(t, outR, got.Sun.R, 3.0, "the Sun lands at the shared radius")

	gotPA := imageAngleToPA(math.Atan2(got.Moon.CY-got.Sun.CY, got.Moon.CX-got.Sun.CX))
	t.Logf("occulter recovered at PA %.1f (true %.1f)", gotPA, skyPA)
	assert.InDelta(t, skyPA, gotPA, 2.0, "the occulter sits at its true position angle")
}

func TestWarpPanel_StretchesTheVerticalBackOut(t *testing.T) {
	// Refraction squashes a low Sun along the local vertical. The warp must put that back, on that
	// axis and by that amount — an isotropic correction, or one on the wrong axis, leaves an oval.
	tests := []struct {
		name           string
		parallacticDeg float64
	}{
		{"vertical straight up", 0},
		{"vertical tipped by the parallactic angle", 43.5},
		{"vertical near the horizontal", 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const flatten = 0.90
			f := PanelFrame{Source: "low.MOV", Sun: Limb{CX: 750, CY: 750, R: 300},
				SkyPADeg: 120, ParallacticDeg: tt.parallacticDeg, Flatten: flatten}
			im := cleanDisc(f.Sun)

			out := WarpPanel(im, f, Orientation{}, f.Sun.R, 1100)

			ratio := discStretch(out, tt.parallacticDeg)
			t.Logf("vertical / horizontal extent = %.4f", ratio)
			assert.InDelta(t, 1/flatten, ratio, 0.02, "the vertical comes back a tenth longer")
		})
	}
}

func TestWarpPanel_LeavesAHighSunRound(t *testing.T) {
	f := PanelFrame{Source: "high.MOV", Sun: Limb{CX: 750, CY: 750, R: 300},
		SkyPADeg: 120, ParallacticDeg: 43.5, Flatten: 1}

	out := WarpPanel(cleanDisc(f.Sun), f, Orientation{}, f.Sun.R, 1100)

	assert.InDelta(t, 1.0, discStretch(out, 43.5), 0.01)
}

// cleanDisc is a limb-darkened disc with no occulter and no detail — an unambiguous shape whose
// elongation can be measured exactly.
func cleanDisc(sun Limb) *fits.Image {
	s := defaultSun()
	s.w, s.h = 1500, 1500
	s.cx, s.cy, s.r = sun.CX, sun.CY, sun.R
	s.features, s.proms = 0, 0
	s.noise, s.ringAmp, s.gradAmp, s.psfSigma = 0, 0, 0, 0
	return drawSun(s)
}

// discStretch is the ratio of the disc's extent along the local vertical to its extent across it,
// from the second moments of the light above the sky.
//
// Moments rather than an edge walk: limb darkening puts the half-peak level well inside the limb, so
// a threshold crossing measures the profile as much as the shape. The RATIO of second moments is
// invariant to whatever radial profile the disc has, which is exactly what an elongation test needs.
func discStretch(im *fits.Image, parallacticDeg float64) float64 {
	a := skyAngle(parallacticDeg)
	up := [2]float64{math.Cos(a), math.Sin(a)}
	side := [2]float64{-up[1], up[0]}

	sky := float32(imgops.Percentile(imgops.Subsample(im.Pix[0], 50000), 10))
	half := float64(im.W-1) / 2
	var sumW, varUp, varSide float64
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			w := float64(im.Pix[0][y*im.W+x] - sky)
			if w <= 0 {
				continue
			}
			dx, dy := float64(x)-half, float64(y)-half
			pu := dx*up[0] + dy*up[1]
			ps := dx*side[0] + dy*side[1]
			sumW += w
			varUp += w * pu * pu
			varSide += w * ps * ps
		}
	}
	if sumW == 0 || varSide == 0 {
		return 0
	}
	return math.Sqrt(varUp / varSide)
}

// imageAngleToPA takes an angle in the output raster back to a position angle. skyAngle is its own
// inverse — reflecting twice returns the original — so this is the same formula in degrees.
func imageAngleToPA(a float64) float64 {
	pa := math.Atan2(-math.Cos(a), -math.Sin(a)) * 180 / math.Pi
	for pa < 0 {
		pa += 360
	}
	return pa
}

func anyNoteContains(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

func TestReconcileGeometry_RepairsACrescentThatCouldNotMeasureItself(t *testing.T) {
	// Two panels of one clip: a shallow phase whose solar limb is nearly complete, and a deep one
	// whose Sun is a sliver and whose fitted circle is badly wrong in both radius and centre. The
	// deep panel must come back to the truth, borrowing the scale from its clip and the centre from
	// its own well-fitted Moon plus the separation the sky knows.
	const (
		sunArcsec = 947.1
		trueScale = 3.14 // arcsec per pixel
		trueSunR  = sunArcsec / trueScale
		sepArcsec = 200.0
		skyPA     = 200.0
		roll      = 41.0
	)
	sepPx := sepArcsec / trueScale
	theta := skyAngle(skyPA) - roll*math.Pi/180

	shallow := PanelFrame{
		Source: "hero.MOV", SkyPADeg: 300, SunRadiusArcsec: sunArcsec, SepArcsec: 1500, Flatten: 1,
		Sun:  Limb{CX: 700, CY: 700, R: trueSunR, ArcDeg: 340},
		Moon: Limb{CX: 700 + 300, CY: 700, R: trueSunR * 1.03, ArcDeg: 60},
	}
	// The deep panel's true Sun sits one separation back along theta from its Moon.
	trueCX, trueCY := 900-sepPx*math.Cos(theta), 880-sepPx*math.Sin(theta)
	deep := PanelFrame{
		Source: "hero.MOV", SkyPADeg: skyPA, SunRadiusArcsec: sunArcsec, SepArcsec: sepArcsec, Flatten: 1,
		Sun:  Limb{CX: trueCX + 40, CY: trueCY - 55, R: trueSunR * 1.35, ArcDeg: 25}, // badly fitted
		Moon: Limb{CX: 900, CY: 880, R: trueSunR * 1.03, ArcDeg: 300},
	}
	frames := []PanelFrame{shallow, deep}
	orients := []Orientation{{RollDeg: roll}, {RollDeg: roll}}

	notes := ReconcileGeometry(frames, orients)

	t.Logf("notes: %v", notes)
	assert.InDelta(t, trueSunR, frames[1].Sun.R, 1.0, "the radius comes from the clip's own plate scale")
	assert.InDelta(t, trueCX, frames[1].Sun.CX, 1.0, "the centre is placed from the Moon and the sky")
	assert.InDelta(t, trueCY, frames[1].Sun.CY, 1.0)
	// The panel that measured itself well is left where it was.
	assert.InDelta(t, 700, frames[0].Sun.CX, 1e-6)
	assert.InDelta(t, trueSunR, frames[0].Sun.R, 1e-6)
	require.NotEmpty(t, notes, "a 35% disagreement with the clip's scale is worth saying")
}

func TestReconcileGeometry_LeavesAPanelWithNoSkyAlone(t *testing.T) {
	frames := []PanelFrame{{Source: "a.MOV", Sun: Limb{CX: 10, CY: 20, R: 300, ArcDeg: 350}, Flatten: 1}}
	orients := []Orientation{{}}

	ReconcileGeometry(frames, orients)

	assert.InDelta(t, 300, frames[0].Sun.R, 1e-9, "without a sky radius there is nothing to reconcile against")
}
