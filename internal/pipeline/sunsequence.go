package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/eclipsegeom"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// sunsequence.go renders the eclipse progression poster: the whole event in one picture, phases
// stepping from a shallow bite through maximum and back out.
//
// The hard part is not the layout, it is that the panels come from different clips of a hand-held
// afocal rig and share nothing — not scale, not orientation, not handedness. What ties them together
// is that the Moon's true position angle at each instant is computable, so comparing it with the
// direction measured in each picture recovers the camera's roll without plate solving anything. See
// solar/panel.go for that solve and eclipsegeom for the phases themselves.

// ordSunSequence places the poster after the finished image in the stage timeline: it is the last
// thing the run makes, and it is made out of everything before it.
const ordSunSequence = 950

// sequenceRecord is what a run leaves behind about its sequence, so the sheet can be laid out again
// without going near the frames.
type sequenceRecord struct {
	Site     eclipsegeom.Site      `json:"site"`
	Maximum  time.Time             `json:"maximum"`
	Mirrored bool                  `json:"mirrored"`
	Layout   solar.SequenceLayout  `json:"layout"`
	Canvas   solar.SequenceCanvas  `json:"canvas"`
	Panels   []sequencePanelRecord `json:"panels"`
	Notes    []string              `json:"notes,omitempty"`
}

// sequencePanelRecord is one panel's provenance: what was asked for, what the sky said, what the
// pixels said, and which of the two candidates won.
type sequencePanelRecord struct {
	Index     int       `json:"index"`
	Side      string    `json:"side"`
	Master    string    `json:"master"`
	Source    string    `json:"source"`
	At        time.Time `json:"at"`
	Frames    int       `json:"frames"`
	Choice    string    `json:"choice"`
	TargetMag float64   `json:"target_magnitude"`
	MissMag   float64   `json:"pair_miss_magnitude"`
	// Predicted comes from the ephemeris, Measured from the two fitted circles. They are recorded
	// side by side because their disagreement is the run's own check on its geometry.
	PredictedObscuration float64 `json:"predicted_obscuration"`
	MeasuredObscuration  float64 `json:"measured_obscuration"`
	SunAltDeg            float64 `json:"sun_alt_deg"`
	MoonPADeg            float64 `json:"moon_pa_deg"`
	// ParallacticDeg and RefractFlatten are what a re-layout needs to squash the disc back out
	// on the right axis without recomputing the ephemeris.
	ParallacticDeg float64           `json:"parallactic_deg"`
	RefractFlatten float64           `json:"refract_flatten"`
	Orientation    solar.Orientation `json:"orientation"`
	StackPSF       solar.PSF         `json:"stack_psf"`
	FramePSF       solar.PSF         `json:"frame_psf"`
}

// renderPhaseSequence is the run's own entry point: plan the phases, build them from the frames the
// run just ingested, and write the sheets.
func renderPhaseSequence(ctx context.Context, opts Options, group solar.Group, frames []solar.Frame,
	p solar.Preset, outDir, object string, res *Result, step, total int) []string {

	say := func(line string) {
		opts.report(Progress{Step: "rendering the phase sequence", Index: step, Total: total, Line: line})
	}
	site, ok := sequenceSite(group, p)
	if !ok {
		return []string{"sun: phase sequence: the clips carry no location tag and no site_lat/site_lon was given, so the phases cannot be computed"}
	}
	outs, warnings := composeSequence(ctx, frames, site, p, outDir, object, nil, say)
	if res.Final != nil {
		res.Final.Outputs = append(res.Final.Outputs, outs...)
	}
	registerSequencePreview(opts, outDir, outs)
	return warnings
}

// composeSequence is the sequence proper, from a set of frames to written sheets. Both the run and a
// rebuild come through here, so there is one answer to what a sequence is.
//
// hydrate, when set, fills in the geometry and score of a window's frames on demand. A run has
// already measured them during ingest; a rebuild reading a scratch directory has only paths and
// clocks, and measuring all of them up front would cost ten minutes to use a few hundred.
func composeSequence(ctx context.Context, frames []solar.Frame, site eclipsegeom.Site, p solar.Preset,
	outDir, object string, hydrate hydrator, say func(string)) ([]string, []string) {

	plan, notes, err := eclipsegeom.PlanLadder(coverageSpans(frames, p.WindowSeconds), site, p.SequencePanels)
	if err != nil {
		return nil, []string{"sun: phase sequence: " + err.Error()}
	}
	warnings := prefix("sun: phase sequence: ", notes)
	for _, n := range notes {
		say(n)
	}

	panels, pWarn := buildPanels(ctx, plan, frames, p, site, hydrate, say)
	warnings = append(warnings, pWarn...)
	if len(panels) < 3 {
		return nil, append(warnings, "sun: phase sequence: fewer than three panels could be built, so no sheet was rendered")
	}
	orientations, oWarn := solveSequenceOrientation(panels)
	warnings = append(warnings, prefix("sun: phase sequence: ", oWarn)...)

	canvas, err := solar.PlanSequenceCanvas(len(panels), medianRadius(panels), p.CropMargin, p.SequenceLayoutOpts())
	if err != nil {
		return nil, append(warnings, "sun: phase sequence: "+err.Error())
	}
	if canvas.Shrunk {
		say(fmt.Sprintf("the sheet would have run past %d px, so the discs were rendered at ⌀%.0f px instead",
			p.SequenceLayoutOpts().MaxEdge, 2*canvas.Radius))
	}
	say(fmt.Sprintf("%d panels on a %d×%d sheet, discs ⌀%.0f px", len(panels), canvas.Width, canvas.Height, 2*canvas.Radius))

	seqDir := filepath.Join(outDir, sequenceDirName)
	if err := fsutil.EnsureDir(seqDir); err != nil {
		return nil, append(warnings, "sun: phase sequence: "+err.Error())
	}
	// The panels and their record are written BEFORE the sheets, not after. Choosing and placing the
	// phases is the expensive half — the frames behind them cost hours to ingest — and rendering is
	// minutes; persisting in that order means anything that goes wrong during the render costs the
	// render only, and a re-layout can pick the run up from here.
	warnings = append(warnings, persistPanels(panels, seqDir)...)
	writeSequenceJSON(seqDir, sequenceRecord{
		Site: site, Maximum: maximumOf(plan), Mirrored: orientations,
		Layout: p.SequenceLayoutOpts(), Canvas: canvas,
		Panels: panelRecords(panels, seqDir), Notes: notes,
	}, &warnings)

	outs, rWarn := renderSequenceSheets(panels, canvas, p, outDir, object, say)
	warnings = append(warnings, rWarn...)

	promOuts, pWarn := renderProminences(frames, site, p, outDir, object, hydrate, say)
	return append(outs, promOuts...), append(warnings, pWarn...)
}

// renderProminences writes the deepest phase on its own, at full resolution, rendered for the
// chromosphere.
//
// It is a separate picture rather than a crop of the sheet because it is a different subject. On the
// sheet every panel is scaled to one shared solar radius so the progression reads; here the frame is
// left at the size it was recorded, because the thing being looked at is a prominence a few
// arcseconds across standing off a limb, and there is no reason to resample it.
func renderProminences(frames []solar.Frame, site eclipsegeom.Site, p solar.Preset,
	outDir, object string, hydrate hydrator, say func(string)) ([]string, []string) {

	best, notes := frameWithTheMostProminence(frames, site, hydrate)
	if best.Path == "" {
		return nil, prefix("sun: phase sequence: prominences: ", append(notes, "no deeply eclipsed frame showed measurable chromosphere off the limb"))
	}
	im, err := fits.ReadImage(best.Path)
	if err != nil {
		return nil, []string{"sun: phase sequence: prominences: " + err.Error()}
	}
	mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
	g, ok := solar.FitGeometry(mono, p.TwoBody)
	if !ok {
		return nil, []string{"sun: phase sequence: prominences: the chosen frame has no fittable limb"}
	}
	var outs, warnings []string
	for _, palette := range sequencePalettes(p) {
		fin := prominenceFinish(p.Finish)
		fin.Palette = palette
		resolved, _, _ := solar.ResolveFinish(mono, g.Sun, fin)
		path := filepath.Join(outDir, fmt.Sprintf("%s_prominences_%s.png", object, palette))
		if err := solar.WritePNG(solar.FinishPair(mono, g, resolved), path); err != nil {
			warnings = append(warnings, "sun: phase sequence: prominences: "+err.Error())
			continue
		}
		outs = append(outs, path)
	}
	if len(outs) > 0 {
		say(fmt.Sprintf("wrote the chromosphere shot from %s at %s (%.1f%% obscured)",
			filepath.Base(best.Source), time.UnixMilli(best.TimeMs).UTC().Format("15:04:05"),
			eclipsegeom.At(time.UnixMilli(best.TimeMs).UTC(), site).Obscuration*100))
	}
	return outs, append(warnings, prefix("sun: phase sequence: prominences: ", notes)...)
}

// hydrator fills in a window's per-frame geometry and score, and says what it could not measure.
type hydrator func([]solar.Frame) ([]solar.Frame, []string)

// buildPanels walks the plan, building each phase and keeping the ones that came out.
//
// A phase that fails is dropped and named rather than aborting the sheet: the shallowest rungs sit
// within a minute of contact, where the occulter is a dent a few pixels deep and the two-circle fit
// is entitled to give up, and losing the outermost pair is not a reason to lose the other seven.
func buildPanels(ctx context.Context, plan []eclipsegeom.Panel, frames []solar.Frame, p solar.Preset,
	site eclipsegeom.Site, hydrate hydrator, say func(string)) ([]*seqPanel, []string) {

	var out []*seqPanel
	var warnings []string
	for i, want := range plan {
		say(fmt.Sprintf("panel %d/%d — %s at %.1f%% obscuration", i+1, len(plan),
			want.Side, want.Obscuration*100))
		window, widened := framesAtPhaseWidening(frames, want, site)
		if widened != "" {
			warnings = append(warnings, fmt.Sprintf("sun: phase sequence: panel %d: %s", i+1, widened))
		}
		if len(window) < 1 {
			window = framesAround(frames, want.At, p.WindowSeconds, p.MinFrames)
		}
		if hydrate != nil {
			var hWarn []string
			window, hWarn = hydrate(window)
			warnings = append(warnings, prefix(fmt.Sprintf("sun: phase sequence: panel %d: ", i+1), hWarn)...)
		}
		if len(window) == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"sun: phase sequence: panel %d dropped: no usable frame near %s",
				i+1, want.At.UTC().Format("15:04:05")))
			continue
		}
		panel, warn, err := buildPanelFromWindow(ctx, want, window, p, site)
		warnings = append(warnings, prefix(fmt.Sprintf("sun: phase sequence: panel %d: ", i+1), warn)...)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("sun: phase sequence: panel %d dropped: %v", i+1, err))
			continue
		}
		say(fmt.Sprintf("panel %d/%d — kept the %s (%d frame(s)) from %s, edge σ %.2f px",
			i+1, len(plan), panel.Choice, panel.Frames, panel.Source, chosenSigma(panel)))
		if gap := obscurationGap(panel.Pair.Obscuration, panel.Circ.Obscuration); gap > 5 && panel.Pair.Eclipsed() {
			warnings = append(warnings, fmt.Sprintf(
				"sun: phase sequence: panel %d measures %.1f%% obscuration where the sky says %.1f%% — the two-circle fit is the suspect, not the ephemeris",
				i+1, panel.Pair.Obscuration*100, panel.Circ.Obscuration*100))
		}
		out = append(out, panel)
	}
	return out, warnings
}

// phaseTol is how far a candidate's phase may sit from the panel's, in obscuration.
//
// The centre is held tighter than the rest, and deliberately. A panel on the shoulder that is two
// points off shows a crescent nobody could tell apart; the CENTRE is the one panel whose whole claim
// is "this is as deep as it got", so it is allowed only a point of slack — enough to choose between
// a few hundred frames for the cleanest, not enough to stop being the maximum.
func phaseTol(want eclipsegeom.Panel) float64 {
	if want.Side == eclipsegeom.Peak {
		return peakPhaseTol
	}
	return panelPhaseTol
}

// framesAtPhaseWidening collects a rung's candidates, loosening the phase tolerance while that rung
// is starved of them.
//
// One fixed tolerance assumes the recording is even, and an eclipse recording never is. Cloud empties
// minutes of it, and ingest's per-clip frame cap spends its whole budget wherever the clip happened to
// be clearest — on the 12 Aug 2026 session that put 1468 of one clip's 2000 materialised frames into
// its first 1.2 minutes and left the next fourteen with 31. The same tolerance therefore caught 1474
// candidates for one rung and 12 for another. What a rung can afford to widen by is a property of the
// rung, so it is decided per rung and by what the window actually holds.
//
// The PEAK rung never widens. Its entire claim is "this is as deep as it got", and buying it more
// candidates by letting it drift is the trade that put an 82% crescent on the first sheet labelled
// 96%.
func framesAtPhaseWidening(frames []solar.Frame, want eclipsegeom.Panel,
	site eclipsegeom.Site) ([]solar.Frame, string) {

	tol, capSeconds := phaseTol(want), phaseCapSeconds(want)
	window := framesAtPhase(frames, want, site, tol, capSeconds)
	if want.Side == eclipsegeom.Peak || len(window) >= panelWantCandidates {
		return window, ""
	}
	narrow, own := len(window), sourcesOf(window)
	for len(window) < panelWantCandidates && tol < panelMaxPhaseTol {
		tol = math.Min(tol+panelPhaseTolStep, panelMaxPhaseTol)
		window = onlyFrom(framesAtPhase(frames, want, site, tol, capSeconds), own)
	}
	if len(window) == narrow {
		return window, fmt.Sprintf(
			"only %d frames sit at this phase, and widening the window to %.0f points of obscuration found no more",
			narrow, tol*100)
	}
	return window, fmt.Sprintf(
		"only %d frames sat within %.0f points of this phase, so the window widened to %.0f and found %d",
		narrow, phaseTol(want)*100, tol*100, len(window))
}

// sourcesOf is the set of clips a window already draws on.
func sourcesOf(window []solar.Frame) map[string]bool {
	out := make(map[string]bool, 2)
	for _, f := range window {
		out[f.Source] = true
	}
	return out
}

// onlyFrom keeps a widened window inside the clips the rung already had, and that restriction is
// what makes widening safe rather than merely bigger.
//
// A clip is one exposure, one magnification and one pass through the phone's encoder, and those do
// not match between clips of a hand-held session. Widening panel 6 of the 12 Aug 2026 sheet without
// this reached from its own clip into one recorded at a smaller image scale — 270 px of solar radius
// against 293 — and far more heavily compressed: its disc is quantised into flat patches with no
// granulation left anywhere. Quantisation SNAPS the limb gradient into a step, so that frame measured
// 0.87 px against the honest frame's 1.29 and won on every metric while being visibly the worse
// picture. A sharpness measure cannot see the difference; the clip boundary can.
func onlyFrom(window []solar.Frame, own map[string]bool) []solar.Frame {
	out := make([]solar.Frame, 0, len(window))
	for _, f := range window {
		if own[f.Source] {
			out = append(out, f)
		}
	}
	return out
}

// phaseCapSeconds bounds the clock as well as the phase, so a phase that barely moves cannot pull in
// half the eclipse.
func phaseCapSeconds(want eclipsegeom.Panel) float64 {
	if want.Side == eclipsegeom.Peak {
		return peakCapSeconds
	}
	return panelCapSeconds
}

const (
	panelPhaseTol   = 0.030
	peakPhaseTol    = 0.010
	panelCapSeconds = 360
	peakCapSeconds  = 150
	// panelWantCandidates is how many frames a rung wants before it stops widening. It is not a
	// target for the picture — a panel ends up being one frame — it is a target for the CHOICE: the
	// seeing decorrelates frame to frame, so the sharpest of a hundred is measurably better than the
	// sharpest of a dozen, and the two shoulder rungs of the 12 Aug 2026 sheet were choosing from 12
	// and 19.
	panelWantCandidates = 120
	panelPhaseTolStep   = 0.010
	// panelMaxPhaseTol is where widening stops however hungry the rung still is. Six points of
	// obscuration is still a crescent nobody could tell from the one that was asked for; much beyond
	// that and the rung stops being the phase its label claims.
	panelMaxPhaseTol = 0.060
)

// solveSequenceOrientation solves the shared handedness and each panel's roll, then repairs the
// solar circles the deep crescents could not measure — in that order, because the repair needs the
// roll and the roll is measured best on the panels that need no repair.
func solveSequenceOrientation(panels []*seqPanel) (bool, []string) {
	frames := make([]solar.PanelFrame, len(panels))
	for i, p := range panels {
		frames[i] = p.Frame
	}
	orients, notes := solar.SolveOrientation(frames)
	notes = append(notes, solar.ReconcileGeometry(frames, orients)...)
	mirrored := false
	for i := range panels {
		panels[i].Frame = frames[i]
		panels[i].Orient = orients[i]
		// The repaired circle goes back into the geometry the FINISH is handed, not only into the
		// one the placement uses. Six measurements inside the finish read "the disc" — the flat, the
		// deconvolution, the limb-darkening profile, the tone anchor, the halo and the prominence
		// reference — and the anchor is taken over the visible-Sun mask. Hand that a solar circle
		// that is in the wrong place and the mask is mostly empty canvas, so the anchor lands on
		// nothing and the crescent renders clipped white. That is exactly what one panel of the
		// 12 Aug sheet did until this line existed.
		panels[i].Pair.Sun = frames[i].Sun
		panels[i].Pair.Obscuration = solar.OverlapFraction(panels[i].Pair.Sun, panels[i].Pair.Moon)
		mirrored = mirrored || orients[i].Mirrored
	}
	return mirrored, notes
}

// renderSequenceSheets renders one sheet per palette.
//
// Every panel of every sheet goes through ONE set of finish options, with only the deconvolution
// width resolved per panel from that panel's own limb. Sharing the tone curve is what makes the
// sequence read as one picture: the crescent's surface brightness does not fall as the Moon covers
// it — only the total light does — so a panel whose own histogram set its own stretch would render
// the deep phases brighter than the shallow ones, which is both wrong and immediately visible.
func renderSequenceSheets(panels []*seqPanel, canvas solar.SequenceCanvas, p solar.Preset,
	outDir, object string, say func(string)) ([]string, []string) {

	var outs, warnings []string
	for _, palette := range sequencePalettes(p) {
		say("rendering the " + palette + " sheet")
		warped := make([]*fits.Image, len(panels))
		for i, panel := range panels {
			warped[i] = warpFinished(panel, p, palette, canvas)
		}
		sheet, notes := solar.RenderSequence(warped, canvas)
		warnings = append(warnings, prefix("sun: phase sequence: ", notes)...)
		path := filepath.Join(outDir, fmt.Sprintf("%s_sequence_%s.png", object, palette))
		if err := solar.WritePNG(sheet, path); err != nil {
			warnings = append(warnings, "sun: phase sequence: "+err.Error())
			continue
		}
		outs = append(outs, path)
	}
	return outs, warnings
}

// warpFinished finishes one panel in one palette and places it in the shared sky frame.
func warpFinished(panel *seqPanel, p solar.Preset, palette string, canvas solar.SequenceCanvas) *fits.Image {
	fin := panelFinish(panel, p.Finish)
	fin.Palette = palette
	// Only the deconvolution is re-resolved per panel: different clips, different focus.
	resolved, _, _ := solar.ResolveFinish(panel.Master, panel.Pair.Sun, fin)
	img := solar.FinishPair(panel.Master, panel.Pair, resolved)
	return solar.WarpPanel(img, panel.Frame, panel.Orient, canvas.Radius, canvas.Side)
}

// sequenceFinish adapts the run's finish to what a sheet of single frames needs.
//
// Two changes, both forced by the sheet rather than chosen. The sky is rendered at EXACTLY black:
// a panel is a disc on a black sheet, and the warm pedestal that flatters a single portrait draws
// every panel's own rectangle across the sequence. And the sharpening's noise thresholds are raised,
// because a panel is one frame off a video codec rather than an average of hundreds — at the run's
// own thresholds the fine scales amplify the codec's block noise into a mottled crust over the
// disc, which is the first thing anyone sees on a poster.
func sequenceFinish(f solar.FinishOptions) solar.FinishOptions {
	f.BackgroundLevel = 0
	// Full instrument-field correction. An Halpha etalon has a sweet spot — the passband shifts
	// across the field, so away from it the disc simply stops being in band and fades out. On the
	// shallowest panel of this session that leaves a Sun with soft flat sides and rounded corners
	// looking, at a glance, like a crop artefact; it is the instrument, it is in the source frames,
	// and Deflat is the thing that answers it. At the run's 0.6 it is only half answered.
	f.FlatStrength = sequenceFlatStrength
	// No synthetic halo. addDiscGlow paints an aureole around the limb because a stacked full disc
	// looks bare without one — but this capture HAS a real aureole, and drawing another over it puts
	// a brown arc around every crescent that is in none of the source frames. Compared side by side
	// with the phone's own view of the same instant, that arc is the single most obvious thing the
	// pipeline was adding.
	f.GlowStrength = 0
	// Barely flatten the limb darkening. At 0.85 it is measuring a radial profile over a crescent
	// that spans a few degrees of azimuth, and the natural falloff is part of what makes the raw
	// frames look like the Sun.
	f.LimbFlatten = sequenceLimbFlatten
	// A single frame's deconvolution budget, not a stack's. The default 50 iterations is sized for a
	// master carrying something like a seventeenth of one frame's noise (see DefaultFinish); run at
	// that depth on one frame it converts grain into ringing, which is the rough dark edge inside the
	// crescent and the mottle over the disc.
	f.DeconvIters = sequenceDeconvIters
	f.Sharpen.Thresholds = []float64{sequenceDenoise * 4, sequenceDenoise * 2, sequenceDenoise, 0, 0}
	if len(f.Sharpen.Gains) >= 5 {
		g := append([]float64(nil), f.Sharpen.Gains...)
		g[0], g[1] = 0, g[1]*sequenceFineGain
		f.Sharpen.Gains = g
	}
	return f
}

// panelFinish is the rendering one panel gets: the shared one, unless it is the deepest phase.
func panelFinish(panel *seqPanel, f solar.FinishOptions) solar.FinishOptions {
	if panel.Plan.Side == eclipsegeom.Peak {
		return prominenceFinish(f)
	}
	return sequenceFinish(f)
}

// prominenceFinish renders the chromosphere rather than the photosphere.
//
// Near maximum the Sun is a thread of chromosphere with prominences standing off it, and that — not
// a bright crescent — is what the eye and the phone both saw: a dark red arc on black with flames
// floating at the cusps. Getting it means giving up on the disc. The tone curve is pulled down so
// the crescent renders deep instead of bright, and the off-limb is lifted until the prominences sit
// at the same level as the arc they came from. Every panel of a sequence shares one finish so the
// sheet reads as one picture, and this is the deliberate exception: at 96% obscuration the subject
// itself has changed.
func prominenceFinish(f solar.FinishOptions) solar.FinishOptions {
	f = sequenceFinish(f)
	f.Stretch = prominenceStretch
	f.ProminenceBoost = prominenceBoost
	f.LimbFlatten = 0
	return f
}

const (
	// sequenceDenoise scales the starlet thresholds, in units of the measured noise sigma. The run's
	// own value is 1; a single video frame needs more.
	sequenceDenoise = 2.0
	// sequenceDeconvIters is one frame's deconvolution budget.
	sequenceDeconvIters = 12
	// sequenceFlatStrength fully removes the etalon's sweet spot and the eyepiece vignette.
	sequenceFlatStrength = 1.0
	// sequenceLimbFlatten leaves most of the natural limb darkening in place.
	sequenceLimbFlatten = 0.30
	// prominenceStretch and prominenceBoost render the chromosphere: a low midtone lift so the
	// crescent stays deep, and enough off-limb gain for the prominences to read against it.
	prominenceStretch = 0.30
	prominenceBoost   = 2.6
)

// sequenceFineGain is what is left of the second starlet scale on a sequence panel. The first is
// switched off entirely.
//
// The reason is the plate scale, not taste. This capture runs at 3.14 arcsec/px against a
// diffraction FWHM of about 2.3 arcsec, so the finest starlet scales — one and two pixels — sit
// ENTIRELY below what the optics can resolve. There is no solar structure there to bring out; every
// bit of contrast those gains add is the codec's. Raising the thresholds does not reach it either,
// because the thresholds are set from noise measured on the SKY, and on-disc video noise has been
// measured at hundreds of times that estimate. So the fine scales are cut rather than gated, and the
// scales that do carry filaments and plage — four pixels and coarser, a dozen arcseconds and up —
// are left exactly as the run tuned them.
const sequenceFineGain = 0.35

// sequencePalettes is which renderings a sequence produces. All three are rendered because the same
// eclipse reads differently in each — the recording's own red is what it looked like through the
// eyepiece, gold is the poster convention, and mono shows the prominences with nothing in the way —
// and re-rendering a persisted panel costs a second where re-stacking it costs minutes.
func sequencePalettes(p solar.Preset) []string {
	if p.Finish.Palette == solar.PaletteNative && !p.Finish.NativeChroma.OK() {
		// Nothing was measured, so the native sheet would silently be the gold one.
		return []string{solar.PaletteGold, solar.PaletteMono}
	}
	return []string{solar.PaletteNative, solar.PaletteGold, solar.PaletteMono}
}

func chosenSigma(panel *seqPanel) float64 {
	if panel.Choice == "frame" {
		return panel.FramePSF.SigmaPx
	}
	return panel.StackPSF.SigmaPx
}

func maximumOf(plan []eclipsegeom.Panel) time.Time {
	for _, p := range plan {
		if p.Side == eclipsegeom.Peak {
			return p.At
		}
	}
	return time.Time{}
}

func prefix(p string, lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = p + l
	}
	return out
}

// persistPanels writes each chosen master, so the sheet can be laid out again — a different angle, a
// different spacing, a different palette — without re-reading a single video frame.
func persistPanels(panels []*seqPanel, seqDir string) []string {
	var warnings []string
	for i, panel := range panels {
		path := filepath.Join(seqDir, sequencePanelName(i))
		if err := panel.Master.WriteFITS(path); err != nil {
			warnings = append(warnings, fmt.Sprintf("sun: phase sequence: persist panel %d: %v", i+1, err))
		}
	}
	return warnings
}

func sequencePanelName(i int) string { return fmt.Sprintf("panel_%02d.fits", i+1) }

func panelRecords(panels []*seqPanel, seqDir string) []sequencePanelRecord {
	out := make([]sequencePanelRecord, len(panels))
	for i, p := range panels {
		out[i] = sequencePanelRecord{
			Index: i + 1, Side: p.Plan.Side.String(), Master: filepath.Join(seqDir, sequencePanelName(i)),
			Source: p.Source, At: p.At, Frames: p.Frames, Choice: p.Choice,
			TargetMag: p.Plan.TargetMag, MissMag: p.Plan.MissMag,
			PredictedObscuration: p.Circ.Obscuration, MeasuredObscuration: p.Pair.Obscuration,
			SunAltDeg: p.Circ.SunAltDeg, MoonPADeg: p.Circ.MoonPADeg,
			ParallacticDeg: p.Circ.ParallacticDeg, RefractFlatten: p.Circ.RefractFlatten,
			Orientation: p.Orient,
			StackPSF:    p.StackPSF, FramePSF: p.FramePSF,
		}
	}
	return out
}

func writeSequenceJSON(seqDir string, rec sequenceRecord, warnings *[]string) {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		*warnings = append(*warnings, "sun: phase sequence: record: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(seqDir, "sequence.json"), b, 0o644); err != nil {
		*warnings = append(*warnings, "sun: phase sequence: record: "+err.Error())
	}
}

// registerSequencePreview puts the first sheet on the run's timeline.
func registerSequencePreview(opts Options, outDir string, outs []string) {
	if len(outs) == 0 {
		return
	}
	dir := filepath.Join(outDir, "previews")
	if err := fsutil.EnsureDir(dir); err != nil {
		return
	}
	dst := filepath.Join(dir, fmt.Sprintf("%03d_sequence.png", ordSunSequence))
	if err := fsutil.CopyFile(outs[0], dst); err != nil {
		return
	}
	opts.report(Progress{StagePreview: &postprocess.StagePreview{
		Index: ordSunSequence, Stage: "sequence", PngPath: dst}})
}
