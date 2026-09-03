package pipeline

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/eclipsegeom"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// piriacSite is where the 12 Aug 2026 clips were shot, from their own location tag.
var piriacSite = eclipsegeom.Site{LatDeg: 47.2783, LonDeg: -2.4948, ElevM: 20}

func sessionSpans() []eclipsegeom.Span {
	clip := func(h, mi, sec int, dur time.Duration) eclipsegeom.Span {
		from := time.Date(2026, 8, 12, h, mi, sec, 0, time.UTC)
		return eclipsegeom.Span{From: from, To: from.Add(dur)}
	}
	return []eclipsegeom.Span{
		clip(17, 30, 6, 15980*time.Millisecond),
		clip(17, 47, 51, 97985*time.Millisecond),
		clip(18, 11, 49, 1049450*time.Millisecond),
		clip(18, 39, 54, 300190*time.Millisecond),
		clip(18, 46, 40, 446575*time.Millisecond),
		clip(19, 12, 33, 85668*time.Millisecond),
	}
}

// TestSequenceGlue_BringsEveryPhaseIntoOneSkyFrame is the end-to-end check on the part that cannot
// be verified from either half alone: that the position angle the ephemeris predicts and the one
// measured in the picture combine into a roll that actually straightens the sequence.
//
// Each panel is drawn as the camera would have recorded it — true geometry for that instant, then
// rolled and mirrored by the rig — and the test asserts that after the solve every occulter lands
// back at its own true position angle.
func TestSequenceGlue_BringsEveryPhaseIntoOneSkyFrame(t *testing.T) {
	const (
		cameraRoll = -73.0
		sunRadius  = 150.0
	)
	plan, _, err := eclipsegeom.PlanLadder(sessionSpans(), piriacSite, 11)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(plan), 7)

	panels := make([]*seqPanel, 0, len(plan))
	for _, want := range plan {
		circ := eclipsegeom.At(want.At, piriacSite)
		im := drawEclipseFrame(circ, sunRadius, cameraRoll, true)
		g, ok := solar.FitPair(im)
		require.Truef(t, ok, "the fixture for %s must be fittable", want.At.Format("15:04:05"))
		panels = append(panels, &seqPanel{
			Plan: want, Circ: circ, At: want.At, Source: "hero.MOV", Master: im, Pair: g,
			Frame: solar.PanelFrame{
				Source: "hero.MOV", Sun: g.Sun, Moon: g.Moon, SkyPADeg: circ.MoonPADeg,
				ParallacticDeg: circ.ParallacticDeg, Flatten: circ.RefractFlatten,
			},
		})
	}

	mirrored, notes := solveSequenceOrientation(panels)

	assert.True(t, mirrored, "the mirroring train is recovered from the position-angle sweep alone")
	for _, n := range notes {
		// The shallowest rungs sit within a minute of contact, where the bite is a few pixels deep
		// and the two-circle fit is entitled to miss it. Those panels must borrow an orientation
		// rather than stay unrotated.
		t.Logf("note: %s", n)
		assert.Contains(t, n, "takes its orientation from the nearest panel")
	}

	canvas, err := solar.PlanSequenceCanvas(len(panels), medianRadius(panels), 0.18, solar.DefaultSequenceLayout())
	require.NoError(t, err)

	for i, panel := range panels {
		if !panel.Pair.Eclipsed() {
			continue
		}
		out := solar.WarpPanel(panel.Master, panel.Frame, panel.Orient, canvas.Radius, canvas.Side)
		got, ok := solar.FitPair(out)
		require.Truef(t, ok, "panel %d must survive the warp", i+1)
		if !got.Eclipsed() {
			continue // a bite a few pixels deep is entitled to be unfittable; the ladder says so too
		}
		gotPA := paOf(got.Moon.CX-got.Sun.CX, got.Moon.CY-got.Sun.CY)
		t.Logf("panel %2d %-8s roll %7.2f  PA measured %6.1f  true %6.1f",
			i+1, panel.Plan.Side, panel.Orient.RollDeg, gotPA, panel.Circ.MoonPADeg)
		assert.InDeltaf(t, panel.Circ.MoonPADeg, gotPA, 3.0,
			"panel %d lands at its true position angle", i+1)
	}
}

func TestSequenceGlue_RollIsSteadyForOneCamera(t *testing.T) {
	const cameraRoll = 41.0
	plan, _, err := eclipsegeom.PlanLadder(sessionSpans(), piriacSite, 9)
	require.NoError(t, err)

	panels := make([]*seqPanel, 0, len(plan))
	for _, want := range plan {
		circ := eclipsegeom.At(want.At, piriacSite)
		im := drawEclipseFrame(circ, 150, cameraRoll, false)
		g, ok := solar.FitPair(im)
		if !ok || !g.Eclipsed() {
			continue
		}
		panels = append(panels, &seqPanel{
			Plan: want, Circ: circ, Source: "hero.MOV", Master: im, Pair: g,
			Frame: solar.PanelFrame{Source: "hero.MOV", Sun: g.Sun, Moon: g.Moon,
				SkyPADeg: circ.MoonPADeg, ParallacticDeg: circ.ParallacticDeg, Flatten: circ.RefractFlatten},
		})
	}
	require.GreaterOrEqual(t, len(panels), 5)

	_, _ = solveSequenceOrientation(panels)

	for i, p := range panels {
		assert.InDeltaf(t, cameraRoll, p.Orient.RollDeg, 3.0, "panel %d recovers the camera's roll", i+1)
		assert.Lessf(t, p.Orient.Residual, 3.0, "panel %d sits close to the clip's own mean roll", i+1)
	}
}

// drawEclipseFrame renders one instant as a camera at the given roll and handedness would have
// recorded it: the true separation and radii, scaled to a solar radius of sunRadius px.
func drawEclipseFrame(c eclipsegeom.Circumstance, sunRadius, rollDeg float64, mirrored bool) *fits.Image {
	const side = 900
	arcsecPerPx := c.SunRadiusArcsec / sunRadius
	sepPx := c.SepArcsec / arcsecPerPx
	moonR := c.MoonRadiusArcsec / arcsecPerPx

	// Undo what the solve will do: place the occulter where this camera would have seen it.
	theta := skyAngleRad(c.MoonPADeg) - rollDeg*math.Pi/180
	if mirrored {
		theta = math.Pi - theta
	}
	cx, cy := float64(side)/2, float64(side)/2
	mx, my := cx+sepPx*math.Cos(theta), cy+sepPx*math.Sin(theta)

	im := fits.NewImage(side, side, 1)
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			v := 0.02
			if d := math.Hypot(dx, dy); d <= sunRadius {
				mu := math.Sqrt(math.Max(0, 1-(d/sunRadius)*(d/sunRadius)))
				v = 1 - 0.45*(1-mu)
			}
			if math.Hypot(float64(x)-mx, float64(y)-my) <= moonR {
				v = 0.02
			}
			im.Pix[0][y*side+x] = float32(v)
		}
	}
	copy(im.Pix[0], imgops.GaussianBlur(im.Pix[0], side, side, 1.2))
	return im
}

// skyAngleRad mirrors solar's own convention: North up, East left.
func skyAngleRad(paDeg float64) float64 {
	pa := paDeg * math.Pi / 180
	return math.Atan2(-math.Cos(pa), -math.Sin(pa))
}

// paOf is the inverse: a raster direction back to a position angle.
func paOf(dx, dy float64) float64 {
	a := math.Atan2(dy, dx)
	pa := math.Atan2(-math.Cos(a), -math.Sin(a)) * 180 / math.Pi
	for pa < 0 {
		pa += 360
	}
	return pa
}

func TestCoverageSpans_BreaksWhereTheFramesStop(t *testing.T) {
	base := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC).UnixMilli()
	var frames []solar.Frame
	add := func(source string, fromSec, toSec int) {
		for s := fromSec; s <= toSec; s++ {
			frames = append(frames, solar.Frame{Source: source, TimeMs: base + int64(s)*1000})
		}
	}
	add("a.MOV", 0, 20)
	add("a.MOV", 400, 420) // a gap far wider than four windows: cloud, or a pause
	add("b.MOV", 800, 830)

	spans := coverageSpans(frames, 30)

	require.Len(t, spans, 3)
	assert.Equal(t, 20*time.Second, spans[0].To.Sub(spans[0].From))
	assert.Equal(t, base+400_000, spans[1].From.UnixMilli())
	assert.Equal(t, 30*time.Second, spans[2].To.Sub(spans[2].From))
}

func TestCoverageSpans_IgnoresFramesWithNoClock(t *testing.T) {
	frames := []solar.Frame{{Source: "a.MOV"}, {Source: "a.MOV"}}
	assert.Empty(t, coverageSpans(frames, 30))
}

func TestFramesAround_WidensRatherThanReturningARunt(t *testing.T) {
	base := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	var frames []solar.Frame
	for s := 0; s < 60; s++ {
		frames = append(frames, solar.Frame{Source: "a.MOV", TimeMs: base.Add(time.Duration(s) * time.Second).UnixMilli()})
	}

	t.Run("a centred window takes what the window holds", func(t *testing.T) {
		got := framesAround(frames, base.Add(30*time.Second), 30, 12)
		assert.Len(t, got, 31)
	})
	t.Run("a phase at the very start still gets a panel", func(t *testing.T) {
		// Only 16 frames lie within half a window of the first second, but the clip's own start is a
		// legitimate phase: the 12 Aug session's shallowest bite is a sixteen-second clip.
		got := framesAround(frames, base, 30, 12)
		assert.GreaterOrEqual(t, len(got), 12)
	})
	t.Run("a demand larger than the clip returns the whole clip", func(t *testing.T) {
		got := framesAround(frames, base.Add(30*time.Second), 1, 500)
		assert.Len(t, got, 60)
	})
}

func TestSequenceSite_PrefersTheOverrideThenTheClipTag(t *testing.T) {
	tagged := solar.Group{Members: []solar.Member{
		{FrameProbe: solar.FrameProbe{Video: &solar.VideoInfo{}}},
		{FrameProbe: solar.FrameProbe{Video: &solar.VideoInfo{
			HasSite: true, LatDeg: 47.2783, LonDeg: -2.4948, ElevM: 20}}},
	}}

	t.Run("the clip's own tag is used when nothing overrides it", func(t *testing.T) {
		site, ok := sequenceSite(tagged, solar.Preset{})
		require.True(t, ok)
		assert.InDelta(t, 47.2783, site.LatDeg, 1e-9)
		assert.InDelta(t, 20, site.ElevM, 1e-9)
	})
	t.Run("an explicit site wins", func(t *testing.T) {
		site, ok := sequenceSite(tagged, solar.Preset{SiteLatDeg: 40, SiteLonDeg: -3})
		require.True(t, ok)
		assert.InDelta(t, 40, site.LatDeg, 1e-9)
	})
	t.Run("no tag and no override is a refusal, not a guess at 0N 0E", func(t *testing.T) {
		untagged := solar.Group{Members: []solar.Member{{FrameProbe: solar.FrameProbe{Video: &solar.VideoInfo{}}}}}
		_, ok := sequenceSite(untagged, solar.Preset{})
		assert.False(t, ok)
	})
}
