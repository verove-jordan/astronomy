package pipeline

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pointing"
	"github.com/verove-jordan/astronomy/internal/skypano"
	"github.com/verove-jordan/astronomy/internal/starfield"
)

func TestPanoProjections(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  []string
		first skypano.Projection
	}{
		{"stereographic", "stereographic", []string{"stereographic"}, skypano.Stereographic},
		{"galactic", "galactic", []string{"galactic_strip"}, skypano.Equirectangular},
		{"both", "both", []string{"stereographic", "galactic_strip"}, skypano.Stereographic},
		// A run that has already stacked every panel must not be failed by a typo in a knob.
		{"an unknown value still renders something", "spherical?", []string{"stereographic"}, skypano.Stereographic},
		{"empty", "", []string{"stereographic"}, skypano.Stereographic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := panoProjections(tt.in)

			var names []string
			for _, p := range got {
				names = append(names, p.name)
			}
			assert.Equal(t, tt.want, names)
			require.NotEmpty(t, got)
			assert.Equal(t, tt.first, got[0].projection)
		})
	}
}

func TestPanoProjections_GalacticStripIsInTheGalacticFrame(t *testing.T) {
	// The point of that canvas is that the band comes out level, which only happens in its own frame.
	got := panoProjections("galactic")

	require.Len(t, got, 1)
	assert.Equal(t, skypano.Galactic, got[0].frame)
	assert.Equal(t, skypano.Equatorial, panoProjections("stereographic")[0].frame)
}

// TestNightpanoPreset_LeavesTheBackgroundToTheCanvas pins the decision that a panel must NOT be
// flattened against its own background: at 57 by 72 degrees the Milky Way band IS the panel's
// large-scale gradient, so removing it removes the subject.
func TestNightpanoPreset_LeavesTheBackgroundToTheCanvas(t *testing.T) {
	p := mode.For(mode.Nightpano)

	assert.False(t, p.BackgroundAI, "per-panel AI background extraction must be off")
	assert.Zero(t, p.BackgroundDegree, "per-panel polynomial background must be off")
	assert.True(t, p.PanoBackground, "the canvas removes the dome instead")
	assert.Greater(t, p.PanoBandMaskLatDeg, 0.0, "and it must know where the band is")
}

// TestNightpanoPreset_MatchesMilkywayWhereThePanelsAreStacked: a panorama is the milkyway recipe run
// N times, so the knobs that drive a panel's stack have to be the same ones.
func TestNightpanoPreset_MatchesMilkywayWhereThePanelsAreStacked(t *testing.T) {
	pano, milky := mode.For(mode.Nightpano), mode.For(mode.Milkyway)

	assert.Equal(t, milky.Color, pano.Color)
	assert.Equal(t, milky.Look, pano.Look)
	assert.Equal(t, milky.Orientation, pano.Orientation)
	assert.Equal(t, milky.BackgroundLevel, pano.BackgroundLevel)
	assert.Equal(t, milky.Grade, pano.Grade)
}

func TestApplyNightpanoParamPatch(t *testing.T) {
	tests := []struct {
		name string
		// unchanged marks a patch that must be REFUSED: nothing moves and the run is not told it did.
		unchanged bool
		patch     map[string]any
		check     func(t *testing.T, p mode.Preset)
	}{
		{
			name:  "the canvas knobs",
			patch: map[string]any{"projection": "galactic", "scale_deg_per_pix": 0.05, "band_mask_lat_deg": 12},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, "galactic", p.PanoProjection)
				assert.InDelta(t, 0.05, p.PanoScaleDegPerPix, 1e-9)
				assert.InDelta(t, 12, p.PanoBandMaskLatDeg, 1e-9)
			},
		},
		{
			name:  "the grade knobs it shares with milkyway",
			patch: map[string]any{"look": "iphone", "brightness": 0.08},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, "iphone", p.Look)
				assert.InDelta(t, 0.08, p.BackgroundLevel, 1e-9)
			},
		},
		{
			name:      "an unknown projection is refused rather than rendered as nothing",
			unchanged: true,
			patch:     map[string]any{"projection": "orthographic"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, mode.For(mode.Nightpano).PanoProjection, p.PanoProjection)
			},
		},
		{
			name:  "the scale is clamped",
			patch: map[string]any{"scale_deg_per_pix": 99.0},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.2, p.PanoScaleDegPerPix, 1e-9) },
		},
		{
			name:  "the background can be turned off",
			patch: map[string]any{"pano_background": false},
			check: func(t *testing.T, p mode.Preset) { assert.False(t, p.PanoBackground) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mode.For(mode.Nightpano)
			raw, err := json.Marshal(tt.patch)
			require.NoError(t, err)

			next, tr, changed := applyNightpanoParamPatch(p, raw)

			assert.Equal(t, tierC, tr, "a panorama has no persisted canvas to re-enter cheaply")
			assert.Equal(t, !tt.unchanged, changed)
			tt.check(t, next)
		})
	}
}

func TestKnownParamKeys_NightpanoCoversBothItsPatchModels(t *testing.T) {
	keys := knownParamKeys(mode.Nightpano)

	for _, k := range []string{"look", "brightness", "saturation_scale", "highlight_ceiling"} {
		assert.True(t, keys[k], "milkyway grade key %q", k)
	}
	for _, k := range []string{"projection", "scale_deg_per_pix", "group_step_deg", "band_mask_lat_deg", "pano_background"} {
		assert.True(t, keys[k], "canvas key %q", k)
	}
	// The panels stack natively through the nightscape recipe, so Siril's stacking panel does not
	// apply and must not be advertised.
	assert.False(t, keys["stack_reject_high"], "the Siril stacking knobs are not nightpano's")
}

func TestParamsFor_NightpanoReportsTheCanvas(t *testing.T) {
	p := mode.For(mode.Nightpano)

	got := ParamsFor(p)

	assert.Equal(t, p.PanoProjection, got["projection"])
	assert.Equal(t, p.PanoScaleDegPerPix, got["scale_deg_per_pix"])
	assert.Equal(t, p.PanoBackground, got["pano_background"])
	assert.Equal(t, p.Look, got["look"])
}

func TestGroupPanels_SkipsFramesWithoutPointing(t *testing.T) {
	// Files that do not exist read as no metadata, which is the same path a JPEG with no gravity
	// vector takes: they are counted and reported, never silently stacked into whichever panel came
	// last.
	panels, warns := groupPanels([]string{"/nonexistent/a.dng", "/nonexistent/b.dng"}, nil)

	assert.Empty(t, panels)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], "no usable pointing metadata")
}

func TestPanoProjections_ResolvesTheArch(t *testing.T) {
	names := func(s string) []string {
		var out []string
		for _, p := range panoProjections(s) {
			out = append(out, p.name)
		}
		return out
	}
	assert.Equal(t, []string{"altaz_arch"}, names("altaz"))
	assert.Equal(t, []string{"stereographic", "galactic_strip", "altaz_arch"}, names("all"))
	assert.Equal(t, []string{"stereographic", "galactic_strip"}, names("both"))
	// An unknown value must not fail a run that has already done all of its stacking.
	assert.Equal(t, []string{"stereographic"}, names("nonsense"))
}

// The arch is drawn for ONE instant, and that instant is the middle of the session: the sky turns
// about 15 degrees an hour, so picking an end would swing every other panel by the whole span.
func TestPanoEpoch_TakesTheMiddleOfTheSession(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 8, 11, h, m, 0, 0, time.UTC) }
	panels := []*panoPanel{
		{center: pointing.Frame{LatDeg: 47.2767, LonDeg: -2.4933, At: at(1, 26), HasSite: true, HasTime: true}},
		{center: pointing.Frame{LatDeg: 47.2767, LonDeg: -2.4933, At: at(3, 29), HasSite: true, HasTime: true}},
	}
	lat, lst, epoch, ok := panoEpoch(panels)
	require.True(t, ok)
	assert.InDelta(t, 47.2767, lat, 1e-6)
	assert.Equal(t, at(2, 27).Add(30*time.Second), epoch, "the midpoint of the span")
	assert.InDelta(t, astro.LST(epoch, -2.4933), lst, 1e-9)
}

// Without a position or a time there is no horizon to draw the sky over, and saying so is better
// than inventing one.
func TestPanoEpoch_RefusesWithoutSiteOrTime(t *testing.T) {
	_, _, _, ok := panoEpoch([]*panoPanel{{center: pointing.Frame{HasSite: true}}})
	assert.False(t, ok, "a frame with no time cannot place the horizon")

	_, _, _, ok = panoEpoch([]*panoPanel{{center: pointing.Frame{HasTime: true}}})
	assert.False(t, ok, "a frame with no position cannot place the horizon")
}

func TestApplyNightpanoParamPatch_ForegroundAndMeteors(t *testing.T) {
	p := mode.For(mode.Nightpano)
	require.False(t, p.PanoForeground, "the landscape is off until asked for")
	require.False(t, p.KeepMeteors)

	next, tier, changed := applyNightpanoParamPatch(p,
		json.RawMessage(`{"projection":"altaz","pano_foreground":true,"keep_meteors":true}`))
	assert.True(t, changed)
	assert.Equal(t, "C", tier.String(), "both re-stack or re-assemble; neither is a re-grade")
	assert.Equal(t, "altaz", next.PanoProjection)
	assert.True(t, next.PanoForeground)
	assert.True(t, next.KeepMeteors)

	got := ParamsFor(next)
	assert.Equal(t, true, got["pano_foreground"])
	assert.Equal(t, true, got["keep_meteors"])
}

// lowestPanel picks the only panel that can hold any ground, and refuses one it cannot place.
func TestLowestPanel_TakesTheOneAimedNearestTheHorizon(t *testing.T) {
	mk := func(label string, alt float64, dir string, site, tm bool) *panoPanel {
		return &panoPanel{label: label, outDir: dir, img: fits.NewImage(4, 4, 3),
			center: pointing.Frame{AltDeg: alt, HasSite: site, HasTime: tm}}
	}
	got := lowestPanel([]*panoPanel{
		mk("p08", 75.6, "/x/p08", true, true),
		mk("p02", 16.2, "/x/p02", true, true),
		mk("p03", 39.5, "/x/p03", true, true),
	})
	require.NotNil(t, got)
	assert.Equal(t, "p02", got.label)

	// A panel with no position or no time cannot be turned onto the arch's instant at all.
	assert.Nil(t, lowestPanel([]*panoPanel{mk("p02", 16.2, "/x/p02", false, true)}))
	assert.Nil(t, lowestPanel([]*panoPanel{mk("p02", 16.2, "/x/p02", true, false)}))
	assert.Nil(t, lowestPanel([]*panoPanel{mk("p02", 16.2, "", true, true)}), "a panel that never stacked")
}

// An arch is a picture of one sky over one horizon. Panels from two sites average to a horizon
// neither session had, and panels from two nights to an instant when nothing was being shot — both
// produce a confident, plausible, WRONG picture rather than an obvious failure. The real case: the
// Loire-Atlantique coast plus a site 390 km inland three nights later.
func TestPanoEpoch_RefusesToMixSitesOrNights(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 8, d, h, m, 0, 0, time.UTC) }
	panel := func(lat, lon float64, tm time.Time) *panoPanel {
		return &panoPanel{center: pointing.Frame{
			LatDeg: lat, LonDeg: lon, At: tm, HasSite: true, HasTime: true}}
	}
	const coastLat, coastLon = 47.2767, -2.4933
	const inlandLat, inlandLon = 48.3433, 2.7712

	t.Run("two sites", func(t *testing.T) {
		_, _, _, ok := panoEpoch([]*panoPanel{
			panel(coastLat, coastLon, at(11, 1, 26)),
			panel(inlandLat, inlandLon, at(11, 1, 40)),
		})
		assert.False(t, ok, "390 km apart is not one horizon")
	})

	t.Run("two nights", func(t *testing.T) {
		_, _, _, ok := panoEpoch([]*panoPanel{
			panel(coastLat, coastLon, at(11, 1, 26)),
			panel(coastLat, coastLon, at(14, 0, 30)),
		})
		assert.False(t, ok, "three nights apart is not one instant")
	})

	t.Run("one site, one night, still fine", func(t *testing.T) {
		_, _, _, ok := panoEpoch([]*panoPanel{
			panel(coastLat, coastLon, at(11, 1, 26)),
			panel(coastLat+0.02, coastLon+0.02, at(11, 3, 29)), // a couple of km along the beach
		})
		assert.True(t, ok, "moving along a beach during one night must still draw an arch")
	})
}

func TestGreatCircleKm(t *testing.T) {
	// The two real sites this session had to tell apart.
	got := greatCircleKm(47.2767, -2.4933, 48.3433, 2.7712)
	assert.InDelta(t, 400, got, 40, "the coast to Seine-et-Marne is about 400 km")
	assert.Zero(t, greatCircleKm(47.2767, -2.4933, 47.2767, -2.4933))
}

// A panel with a horizon in it supplies bright, sharp objects that are not stars — measured on the low
// panel of a real session, half dark land and sea with a town's chain of street lamps through it, the
// detector returned 1535 objects and the solve failed outright. Masking the ground is what lets that
// panel be solved at all.
func TestSkyOnlyDetections(t *testing.T) {
	const w, h = 200, 200
	dir := t.TempDir()
	alpha := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y < 120 { // sky above, ground below
				alpha.Pix[0][y*w+x] = 1
			}
		}
	}
	require.NoError(t, alpha.WriteFITS(filepath.Join(dir, "sky_alpha.fits")))

	var det []starfield.Star
	for i := 0; i < 100; i++ {
		det = append(det, starfield.Star{X: float64(i), Y: 40})  // sky
		det = append(det, starfield.Star{X: float64(i), Y: 170}) // street lamps
	}
	kept, dropped := skyOnlyDetections(det, dir, w, h)
	assert.Len(t, kept, 100, "only the sky detections survive")
	assert.Equal(t, 100, dropped)
	for _, d := range kept {
		assert.Less(t, d.Y, 120.0)
	}

	// Too few would be left to solve with: keep everything rather than guarantee a failure.
	few := []starfield.Star{{X: 10, Y: 40}, {X: 20, Y: 170}}
	kept, dropped = skyOnlyDetections(few, dir, w, h)
	assert.Len(t, kept, 2, "the mask is refused when it would leave too little to solve")
	assert.Zero(t, dropped)

	// No mask, or one of the wrong size, must never lose detections.
	kept, dropped = skyOnlyDetections(det, t.TempDir(), w, h)
	assert.Len(t, kept, 200)
	assert.Zero(t, dropped)
	kept, _ = skyOnlyDetections(det, dir, w+1, h)
	assert.Len(t, kept, 200, "a mask that is not the panel's own shape is not trusted")
}

// A run combining two sessions must still draw an arch — from the LARGER session — rather than
// refusing outright and leaving the run with no arch and no foreground at all. Every panel still
// feeds the sky canvases; only the horizon is session-specific.
func TestPanoArchCluster_DrawsTheLargestSession(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 8, d, h, m, 0, 0, time.UTC) }
	panel := func(label string, lat, lon float64, tm time.Time) *panoPanel {
		return &panoPanel{label: label, center: pointing.Frame{
			LatDeg: lat, LonDeg: lon, At: tm, HasSite: true, HasTime: true}}
	}
	const coastLat, coastLon = 47.2767, -2.4933  // Loire-Atlantique, 10 August
	const inlandLat, inlandLon = 48.3433, 2.7712 // Seine-et-Marne, 13/14 August, 390 km away

	panels := []*panoPanel{
		panel("a", coastLat, coastLon, at(11, 1, 26)),
		panel("b", coastLat, coastLon, at(11, 2, 10)),
		panel("c", coastLat+0.01, coastLon, at(11, 3, 29)),
		panel("x", inlandLat, inlandLon, at(14, 0, 0)),
		panel("y", inlandLat, inlandLon, at(14, 0, 40)),
	}
	arch, lat, _, epoch, ok := panoArchCluster(panels)
	require.True(t, ok)
	require.Len(t, arch, 3, "the three-panel coastal session wins")
	for _, p := range arch {
		assert.Contains(t, []string{"a", "b", "c"}, p.label)
	}
	assert.InDelta(t, coastLat, lat, 0.02, "and the arch stands at the coast, not between the two")
	assert.Equal(t, at(11, 2, 27).Add(30*time.Second), epoch, "the middle of that session")
}

// A slow drift must not chain across a region: each panel is compared with the session's START, not
// with the one before it, or twenty five-kilometre steps would quietly become a hundred kilometres.
func TestPanoArchCluster_DriftDoesNotChain(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 8, 11, 1, m, 0, 0, time.UTC) }
	var panels []*panoPanel
	for i := 0; i < 6; i++ {
		panels = append(panels, &panoPanel{center: pointing.Frame{
			LatDeg: 47.0 + float64(i)*0.15, LonDeg: -2.0, // ~17 km per step
			At: at(i * 10), HasSite: true, HasTime: true}})
	}
	arch, _, _, _, ok := panoArchCluster(panels)
	require.True(t, ok)
	assert.Less(t, len(arch), len(panels), "the far end is a different place and must not join the arch")
}

// A quad solver can always find SOME four stars whose shape matches a catalogue quad. On a star-poor
// panel searched deeply that answer is a coincidence, and a panel placed wrongly corrupts the canvas
// around it — strictly worse than a panel left out. These are the real numbers from the low panel of
// the 2026-08-10 session.
func TestPlausibleSolve(t *testing.T) {
	good := skypano.Solution{Matches: 616, RMSPx: 2.35}
	weak := skypano.Solution{Matches: 90, RMSPx: 2.85}

	t.Run("the false solve is refused", func(t *testing.T) {
		// 90 stars, 90 degrees off the recorded bearing.
		assert.False(t, plausibleSolve(weak, 296, 206))
	})

	t.Run("a bearing that agrees is fine", func(t *testing.T) {
		assert.True(t, plausibleSolve(good, 215, 215))
	})

	// The compass on this hardware is wrong in a PARTICULAR way: near zero or near 180. Three real
	// panels solved to 215/215/224 against a recorded 35/35/44 and were correct.
	t.Run("the compass being 180 out is expected, not a failure", func(t *testing.T) {
		assert.True(t, plausibleSolve(good, 215, 35))
		assert.True(t, plausibleSolve(good, 224, 44))
	})

	t.Run("but 90 degrees off is not a compass error", func(t *testing.T) {
		assert.False(t, plausibleSolve(good, 296, 206))
	})

	t.Run("too few matches is refused whatever the bearing says", func(t *testing.T) {
		assert.False(t, plausibleSolve(weak, 215, 215))
	})

	t.Run("with no bearing recorded the match count is all there is", func(t *testing.T) {
		assert.True(t, plausibleSolve(good, 123, 0))
		assert.False(t, plausibleSolve(weak, 123, 0))
	})
}

func TestAngleGapDeg(t *testing.T) {
	assert.InDelta(t, 0.0, angleGapDeg(10, 10), 1e-9)
	assert.InDelta(t, 20.0, angleGapDeg(350, 10), 1e-9, "wrapping across zero")
	assert.InDelta(t, 180.0, angleGapDeg(0, 180), 1e-9)
	assert.InDelta(t, 90.0, angleGapDeg(296, 206), 1e-9, "the real false solve")
}
