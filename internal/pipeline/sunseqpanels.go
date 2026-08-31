package pipeline

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/eclipsegeom"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// sunseqpanels.go turns a planned phase into the best picture the recording holds of it, which on a
// crescent is one frame rather than an average of many.
//
// A stack pays for registration twice — every frame resampled, then disagreeing estimates averaged —
// and on a crescent the solar arc is short, so the fitted centre is poorly constrained perpendicular
// to it and that scatter reaches every pixel. Measured on the 12 Aug 2026 clips the 834-frame master
// resolved the occulter's edge at sigma 2.30 px against 1.09 for a single frame. Rendered, it is not
// merely softer: the band the occulter's sweep took out is recovered by a second stack and joins
// along a hard dark arc through the crescent, the cusps double where the two registrations disagree,
// and sharpening turns the averaged noise into a mottled crust. A single frame has none of that.
//
// So the default is the window's sharpest frame, placed by one cubic resample and nothing else.
// SequenceStack builds the stack as a second candidate and keeps it only where the occulter's own
// edge says it is sharper — kept as a knob because the trade is a property of the capture, not a law.

// seqPanel is one phase of the sequence, from plan through to placement.
type seqPanel struct {
	Plan   eclipsegeom.Panel
	Circ   eclipsegeom.Circumstance
	At     time.Time
	Source string
	Frames int
	Master *fits.Image
	Pair   solar.Pair
	Frame  solar.PanelFrame
	Orient solar.Orientation
	// Choice is "stack" or "frame", and the two PSFs are what decided it.
	Choice   string
	StackPSF solar.PSF
	FramePSF solar.PSF
}

// buildPanel takes the best picture the recording holds of one planned instant.
//
// By default that is the window's single sharpest frame, put on the canonical raster by ONE cubic
// resample and nothing else — no averaging. Only when SequenceStack is set is the window also
// stacked, and then the occulter's own edge decides which of the two is kept.
func buildPanel(ctx context.Context, plan eclipsegeom.Panel, frames []solar.Frame, p solar.Preset,
	site eclipsegeom.Site) (*seqPanel, []string, error) {

	window := framesAround(frames, plan.At, p.WindowSeconds, p.MinFrames)
	if len(window) == 0 {
		return nil, nil, fmt.Errorf("no frame within reach of %s", plan.At.UTC().Format("15:04:05"))
	}
	return buildPanelFromWindow(ctx, plan, window, p, site)
}

// buildPanelFromWindow is buildPanel over an already-chosen window, so a rebuild that hydrates its
// frames itself does not have to select them twice.
func buildPanelFromWindow(ctx context.Context, plan eclipsegeom.Panel, window []solar.Frame,
	p solar.Preset, site eclipsegeom.Site) (*seqPanel, []string, error) {

	best, gateWarn := selectFrame(plan, window, site)
	single, err := stackSharpest(ctx, best, p)
	if err != nil {
		return nil, nil, fmt.Errorf("placing the sharpest of %d frames: %w", len(window), err)
	}
	// Dated by the frame it USED, not by the middle of the window it chose from. When a phase sits
	// near the end of a clip the window widens to the nearest frames and can span minutes, and a
	// panel described by an instant its pixels do not come from reports a phase it is not: on the
	// first render of this session that put the maximum panel ten points away from what it measured.
	panel := &seqPanel{
		Plan: plan, At: time.UnixMilli(best.TimeMs).UTC(), Source: filepath.Base(best.Source),
		Frames: 1, Master: single.Master, Pair: single.Pair(), Choice: "frame",
		FramePSF: solar.MeasureSharpness(single.Master, single.Pair()),
	}
	warnings := append(gateWarn, stackNotes(single.Notes)...)
	if p.SequenceStack {
		warnings = append(warnings, considerStack(ctx, panel, window, p)...)
		if panel.Choice == "stack" {
			panel.At = midTime(window) // an average of the window is dated by the window
		}
	}
	panel.Circ = eclipsegeom.At(panel.At, site)
	panel.Frame = solar.PanelFrame{
		Source: panel.Source, Sun: panel.Pair.Sun, Moon: panel.Pair.Moon,
		SkyPADeg: panel.Circ.MoonPADeg, ParallacticDeg: panel.Circ.ParallacticDeg,
		Flatten: panel.Circ.RefractFlatten,
		// What the sky says, so a crescent too thin to fit its own circle can borrow the scale and
		// the centre it could not measure. solar.ReconcileGeometry uses these once every panel and
		// every roll is known.
		SunRadiusArcsec: panel.Circ.SunRadiusArcsec, SepArcsec: panel.Circ.SepArcsec,
	}
	return panel, warnings, nil
}

// considerStack builds the window stack as a second candidate and keeps it only if it measurably
// resolves the occulter's edge better than the single frame did.
func considerStack(ctx context.Context, panel *seqPanel, window []solar.Frame, p solar.Preset) []string {
	stack, err := solar.Stack(ctx, window, p.StackOpts())
	if err != nil {
		return []string{fmt.Sprintf("%s: window stack: %v", panel.Source, err)}
	}
	panel.StackPSF = solar.MeasureSharpness(stack.Master, stack.Pair())
	if !sharperThan(panel.StackPSF, panel.FramePSF) {
		return stackNotes(stack.Notes)
	}
	panel.Master, panel.Pair, panel.Choice, panel.Frames = stack.Master, stack.Pair(), "stack", stack.Frames
	return stackNotes(stack.Notes)
}

// stackNotes drops the placement notes a one-frame stack emits about coverage. On a single frame
// "62% of the canvas was outside every frame" is the occulter, not a defect, and repeating it once
// per panel buries the notes that matter.
func stackNotes(notes []string) []string {
	var out []string
	for _, n := range notes {
		if strings.Contains(n, "was outside every frame") || strings.Contains(n, "is excluded from the stack") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// stackSharpest runs the single best frame of the window through the identical path the stack takes,
// so the two candidates land on the same raster and can be compared without a second variable. One
// frame warped once measured 1.66 px against 1.72 unwarped on the real clips — the resample is not
// what costs a stack its resolution.
//
// Drizzle is forced OFF here, whatever the preset asks for. Drizzling recovers resolution from
// the sub-pixel dither BETWEEN many frames; a single frame has none, so all it does is
// interpolate onto a finer grid — and the finish then sharpens that interpolation, which is
// exactly how a clean frame turns into a mottled one.
func stackSharpest(ctx context.Context, best solar.Frame, p solar.Preset) (*solar.StackResult, error) {
	opts := p.StackOpts()
	opts.Drizzle = 1
	return solar.Stack(ctx, []solar.Frame{best}, opts)
}

// selectFrame picks the frame a panel is made of: the cleanest one that is still the phase the panel
// claims to be.
//
// The candidates are bounded by PHASE, not by a stretch of clock. Bounding by time is what let the
// centre panel of the first sheet drift five minutes off maximum in search of a sharper frame and
// come back an 82% crescent labelled 96%. Bounding by phase says what actually matters — "this is
// the deepest moment, to within a point or two" — and it widens the search on its own exactly where
// the phase changes slowly, which near maximum is minutes rather than the thirty seconds a stacking
// window allows.
//
// Within those candidates the choice is the measured edge, after frames that cannot make a good
// picture at all have been thrown out.
func selectFrame(plan eclipsegeom.Panel, window []solar.Frame, site eclipsegeom.Site) (solar.Frame, []string) {
	usable, dropped := usableFrames(window)
	if len(usable) == 0 {
		usable = window
	}
	best, warn := sharpestAgreeingWithTheSky(usable, site, requireWholeDisc)
	if dropped > 0 {
		warn = append(warn, fmt.Sprintf("%d of %d candidate frames were unusable (disc cut by the frame edge, or too little limb to fit)", dropped, len(window)))
	}
	return best, warn
}

// usableFrames throws out the frames whose limb fit is too poor to trust: a circle voted on by a
// short arc, or one whose points do not lie on it, is a frame where the geometry that centres,
// scales and masks everything downstream is guesswork.
//
// Whether the DISC is cut is deliberately not tested here. Limb.Partial reports that the disc runs
// past the raster, and ingest crops tightly around the disc by design, so it fires on most frames of
// most clips and says nothing about whether the picture is any good — on this session it rejected
// 1216 of 1474 candidates for a panel whose result was fine. How much of the disc is actually
// missing is measured instead, in sharpestByItsOwnEdge, where the raster is in hand.
func usableFrames(window []solar.Frame) ([]solar.Frame, int) {
	out := make([]solar.Frame, 0, len(window))
	for _, f := range window {
		if f.Limb.R <= 0 {
			continue
		}
		if f.Limb.ArcDeg < minPanelArcDeg || f.Limb.ResidRMS > maxPanelResidPx {
			continue
		}
		out = append(out, f)
	}
	return out, len(window) - len(out)
}

const (
	// minPanelArcDeg is how much limb must have voted on the circle. A crescent at maximum still
	// offers well over a hundred degrees of solar limb, so this rejects a frame that was clipped or
	// half lost to cloud rather than one that is merely deep.
	minPanelArcDeg = 100
	// maxPanelResidPx is how far the fitted points may scatter about the circle.
	maxPanelResidPx = 2.0
)

// frameWithTheMostProminence finds the instant the flames were out, then the best frame at it.
//
// Two stages, because the two questions are different. WHICH MOMENT is a property of the Sun and
// changes over minutes, so it is answered by sampling the search window sparsely and measuring the
// light standing off the limb. WHICH FRAME of that moment is a property of the air and changes over
// a fraction of a second, so it is answered the ordinary way, by the edge. Ranking every frame by
// prominence would cost the same as the sparse sample and buy nothing: neighbouring frames show the
// same prominence.
func frameWithTheMostProminence(frames []solar.Frame, site eclipsegeom.Site,
	hydrate hydrator) (solar.Frame, []string) {

	// Only deeply eclipsed frames are eligible. Off-limb brightness rises as the crescent thickens —
	// more solar limb is exposed, so more chromosphere stands beside it — so an unconstrained search
	// walks straight out of the eclipse and returns a nearly full Sun with a bright rim.
	deep := make([]solar.Frame, 0, len(frames))
	for _, f := range frames {
		if eclipsegeom.At(time.UnixMilli(f.TimeMs).UTC(), site).Obscuration >= promMinObscuration {
			deep = append(deep, f)
		}
	}
	if len(deep) == 0 {
		return solar.Frame{}, nil
	}
	sample := everyNth(deep, promSampleStride(len(deep)))
	type scored struct {
		at    int64
		score float64
	}
	var ranked []scored
	for _, f := range sample {
		im, err := fits.ReadImage(f.Path)
		if err != nil {
			continue
		}
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		// Fitted here rather than taken from the frame: a rebuild reconstructs its frame list from
		// paths and clocks alone, so the circles are not filled in until a panel asks for them, and
		// a search that trusted them would measure every frame as having no Sun at all.
		g, ok := solar.FitGeometry(mono, true)
		if !ok {
			continue
		}
		score, ok := solar.ProminenceSignal(mono, g)
		if !ok {
			continue
		}
		ranked = append(ranked, scored{f.TimeMs, score})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	bestAt, bestScore := int64(0), -1.0
	if len(ranked) > 0 {
		bestAt, bestScore = ranked[0].at, ranked[0].score
	}
	var top []string
	for _, r := range ranked[:minInt(4, len(ranked))] {
		top = append(top, fmt.Sprintf("%s:%.3f", time.UnixMilli(r.at).UTC().Format("15:04:05"), r.score))
	}
	if bestScore < 0 {
		return solar.Frame{}, nil
	}
	at := time.UnixMilli(bestAt).UTC()
	near := framesAround(deep, at, promMomentSeconds, promMomentCandidates)
	// Hydrated here, and it is not optional. A rebuild carries only paths and clocks, so a candidate
	// arrives with no fitted circles at all — and every question asked of it downstream then answers
	// from zeroes. It measures its own obscuration as 0, disagrees with the sky by eighty points and
	// trips the "no frame matched the sky" fallback; its limb cannot be measured, so the choice drops
	// back to the band-pass score; and the disc-containment probe reads 0.0% present and reports the
	// clear sky as cloud. All three fired on the 12 Aug 2026 sheet, and all three were this.
	var warn []string
	if hydrate != nil {
		near, warn = hydrate(near)
	}
	best, sWarn := sharpestAgreeingWithTheSky(near, site, ignoreWholeDisc)
	warn = append(warn, sWarn...)
	return best, append(warn, fmt.Sprintf(
		"chromosphere search over %d deeply-eclipsed frames, best sectors: %s",
		len(deep), strings.Join(top, "  ")))
}

// promMinObscuration is how deep the eclipse must be for a frame to be a candidate.
//
// Not as deep as it first looks it should be. Right at maximum the Moon hides almost the whole limb,
// so a flame can only be seen within a few degrees of the cusps and often is not there at all; a
// little further out the crescent is still dramatic and much more of the chromosphere is exposed.
// Three fifths covered is where the picture stops being "a Sun with a bite" and starts being "a
// crescent with things standing off it".
const promMinObscuration = 0.60

// promMomentSeconds is how wide a window the sharpest frame is then picked from, once the moment is
// known. A prominence does not change in twenty seconds; the seeing does, so every further candidate
// inside that window is another free chance at a cleaner picture of the same flame.
const promMomentSeconds = 20

// promMomentCandidates is the floor under that window, and it is the part stating the window in
// SECONDS could not guarantee. Ingest caps a clip at a couple of thousand materialised frames however
// long it runs, so the 12 Aug 2026 session's seventeen-minute clip reached the sequence at under two
// frames a second rather than thirty: six seconds of it was THREE pictures, and choosing the sharpest
// of three is barely choosing. Below this many the window widens to the nearest candidates whatever
// the clock says.
const promMomentCandidates = 24

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// promSampleStride keeps the moment search to a manageable number of decodes however long the deep
// stretch is. A prominence stands for minutes, so a sample every few seconds cannot step over one.
func promSampleStride(n int) int {
	const want = 90
	if n <= want {
		return 1
	}
	return n / want
}

func everyNth(window []solar.Frame, stride int) []solar.Frame {
	if stride < 1 {
		stride = 1
	}
	out := make([]solar.Frame, 0, len(window)/stride+1)
	for i := 0; i < len(window); i += stride {
		out = append(out, window[i])
	}
	return out
}

// wholeDiscPolicy says whether a candidate has to show an unbroken disc to be preferred.
type wholeDiscPolicy bool

const (
	// requireWholeDisc is for a PANEL, which is a picture of the Sun. A disc with a piece missing to
	// cloud, or to the scan crop, is a ruined panel however well it resolves.
	requireWholeDisc wholeDiscPolicy = true
	// ignoreWholeDisc is for the chromosphere picture, whose subject is what stands OFF the limb at
	// the deepest moment the recording holds. There the disc is SUPPOSED to be nearly gone, so asking
	// how much of it is present is not a quality test at all — it is a phase measurement wearing one.
	// On the 12 Aug 2026 session it reported 0.0% of the disc present on every candidate at 85%
	// obscuration and warned about cloud that was not there.
	ignoreWholeDisc wholeDiscPolicy = false
)

// sharpestAgreeingWithTheSky picks the frame a panel is made of, from those whose measured geometry
// matches what the sky says that instant looked like.
//
// The check is not a nicety, it is the difference between a picture and a ruin. The stack masks the
// occulter out of the frame it places, so a frame whose lunar circle is mis-fitted has real Sun
// DELETED from it — on the first sheet of the 12 Aug 2026 session that left one panel as two
// disconnected cusps with the crescent between them eaten away, and nothing downstream could put it
// back. Sharpness cannot catch that: the frame was among the sharpest in its window.
//
// The ephemeris can, because obscuration at a given second is a number and not an opinion. A frame
// that disagrees with it by more than skyAgreeTol has a fit that is wrong, whatever it scored.
func sharpestAgreeingWithTheSky(window []solar.Frame, site eclipsegeom.Site,
	disc wholeDiscPolicy) (solar.Frame, []string) {

	var agree []solar.Frame
	for _, f := range window {
		want := eclipsegeom.At(time.UnixMilli(f.TimeMs).UTC(), site).Obscuration
		if math.Abs(solar.OverlapFraction(f.Limb, f.Moon)-want) <= skyAgreeTol {
			agree = append(agree, f)
		}
	}
	var warnings []string
	if len(agree) == 0 {
		// Never lose a panel over this: a systematic bias would reject every frame, and a picture
		// built from a doubtful fit still beats no picture. Say so rather than hide it.
		agree = window
		warnings = append(warnings, fmt.Sprintf(
			"no frame in this window measured its own phase to within %.0f points of the sky, so the sharpest was used regardless",
			skyAgreeTol*100))
	}
	best, note := sharpestByItsOwnEdge(agree, disc)
	if note != "" {
		warnings = append(warnings, note)
	}
	return best, warnings
}

// sharpestByItsOwnEdge picks the candidate whose SOLAR LIMB is narrowest, over the whole pool.
//
// Two things have ranked this choice before and both were wrong, in the same way: they measured
// something that varies for a reason other than the one being asked about.
//
// FrameSharpness, the cheap band-pass score, measures energy over the frame's own level, and through
// a video codec that band holds block noise as well as detail — on-disc noise has been measured at
// hundreds of times the sky estimate it normalises against. So it flatters the noisiest frames, which
// is the original complaint that a chosen frame looked worse than one picked by eye.
//
// MeasureSharpness is the subtler one, because it looks right. It prefers the OCCULTER's edge, an
// opaque body against the Sun blurred by nothing but the system, and against a synthetic blur it is
// indeed the better probe. On the real clips it is noise. Over the 82%-obscured stretch of the
// 12 Aug 2026 session both probes had thirty-odd usable wedges on every extracted frame and they came
// out ANTI-correlated: the frame the occulter liked best, at 0.94 px, has a solar limb of 1.78 px
// (13.7"), while the sharpest frame in the band — limb 1.11 px, 9.1" — was ranked worst of all at
// 1.19. Across the whole band the occulter's range was 0.94 to 1.40 where the limb's was 1.06 to
// 1.86: the lunar limb sits against the codec-crushed dark side of the crescent, so its measured
// width is set by deblocking ringing and hardly moves with the seeing. Ranking on it chose a frame at
// the band's MEDIAN and reported it as the band's best, which is exactly how the 81% panel of the
// first sheets came to be the softest of the seven while its selection note claimed 0.83 px.
//
// The solar limb is the honest probe here, and it is also the one the panel MASTER ends up graded on:
// the placement masks the occulter out, so MeasureSharpness finds too few wedges there and drops
// through to the limb by itself. Selection and grading therefore now ask the same question, which is
// why a panel no longer measures a whole pixel worse than the frame it was made of.
//
// Measuring every candidate rather than a shortlist costs less than the shortlist did, not more. The
// containment test already reads each candidate's raster; the limb measurement rides along on that
// read, and the forty re-reads the old shortlist paid for are gone.
func sharpestByItsOwnEdge(pool []solar.Frame, disc wholeDiscPolicy) (solar.Frame, string) {
	cands := measureCandidates(pool, disc)
	if len(cands) == 0 {
		return sharpestOf(pool), "no candidate could be read, so the band-pass score chose the frame"
	}
	round, bestWhole := wholeDiscOf(cands)
	best := round[0]
	for _, c := range round[1:] {
		if c.fwhm < best.fwhm {
			best = c
		}
	}
	if math.IsInf(best.fwhm, 1) {
		return sharpestOf(framesOf(round)), "no candidate's limb could be measured, so the band-pass score chose the frame"
	}
	if disc == requireWholeDisc && bestWhole < wholeDiscMin {
		return best.frame, fmt.Sprintf(
			"the roundest of %d candidates still holds only %.1f%% of its disc — cloud or the etalon's sweet spot, not the choice of frame",
			len(cands), bestWhole*100)
	}
	return best.frame, fmt.Sprintf("chosen from %d candidates on its own solar limb, %.2f px (%.1f\")",
		len(round), best.sigma, best.fwhm)
}

// panelCandidate is one candidate with everything the choice needs, measured in a single read of it.
type panelCandidate struct {
	frame solar.Frame
	// whole is the fraction of the solar limb actually present, or 1 when the disc is not being
	// judged at all.
	whole float64
	// sigma is the solar limb's width in pixels, +Inf when it could not be measured, and fwhm the
	// same width in arcseconds. The RANKING uses the arcseconds: pixels are only comparable at one
	// image scale, and a session shot hand-held through a phone changes scale between clips — 270 px
	// of solar radius against 293 on 12 Aug 2026 — so a pixel width silently flatters whichever clip
	// was least magnified. sigma is kept because it is what the run reports, in the units the rest of
	// the sequence talks in.
	sigma float64
	fwhm  float64
}

func measureCandidates(pool []solar.Frame, disc wholeDiscPolicy) []panelCandidate {
	out := make([]panelCandidate, 0, len(pool))
	for _, f := range pool {
		im, err := fits.ReadImage(f.Path)
		if err != nil {
			continue
		}
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		c := panelCandidate{frame: f, whole: 1, sigma: math.Inf(1), fwhm: math.Inf(1)}
		if disc == requireWholeDisc {
			c.whole = discInside(mono, solar.Pair{Sun: f.Limb, Moon: f.Moon})
		}
		if psf := solar.MeasurePSF(mono, f.Limb); psf.OK {
			c.sigma, c.fwhm = psf.SigmaPx, psf.FWHMArcsec
		}
		out = append(out, c)
	}
	return out
}

// wholeDiscOf keeps the candidates holding as much of their disc as the best of them does.
//
// Roundness is settled over EVERY candidate before sharpness looks at any of them. The obvious order
// — shortlist on a score, then prefer the roundest of the shortlist — is wrong and quietly so: the
// shortlist is built by a metric that cannot be trusted, so a frame holding a whole Sun is never even
// examined if forty noisier ones outscored it.
func wholeDiscOf(cands []panelCandidate) ([]panelCandidate, float64) {
	bestWhole := 0.0
	for _, c := range cands {
		if c.whole > bestWhole {
			bestWhole = c.whole
		}
	}
	round := make([]panelCandidate, 0, len(cands))
	for _, c := range cands {
		if c.whole >= bestWhole-wholeDiscSlack {
			round = append(round, c)
		}
	}
	if len(round) == 0 {
		return cands, bestWhole
	}
	return round, bestWhole
}

func framesOf(cands []panelCandidate) []solar.Frame {
	out := make([]solar.Frame, len(cands))
	for i, c := range cands {
		out[i] = c.frame
	}
	return out
}

// wholeDiscSlack is how much less of the disc a sharper candidate may hold before it is refused.
const wholeDiscSlack = 0.005

// discInside is the fraction of the solar limb that is actually PRESENT in the frame.
//
// Not the fraction inside the raster, which is what the first version measured and why it found
// nothing wrong: the extraction pads its square, so a disc whose edge was chopped by the scan crop
// sits comfortably inside a raster whose borders are black, and every containment test passes while
// the picture has a straight side.
//
// So the question asked is whether there is LIGHT just inside the fitted limb, all the way round.
// Azimuths behind the occulter are skipped — the Moon covering the disc is the subject, not damage —
// which is what keeps a 96% crescent from reading as 4% present.
func discInside(im *fits.Image, g solar.Pair) float64 {
	if im == nil || len(im.Pix) == 0 || g.Sun.R <= 0 {
		return 0
	}
	level := solar.CrescentLevel(im, g)
	if level <= 0 {
		return 0
	}
	guard := g.Moon.R + 0.01*g.Sun.R
	present, tested := 0, 0
	for i := 0; i < discProbeRays; i++ {
		a := 2 * math.Pi * float64(i) / discProbeRays
		x := g.Sun.CX + discProbeR*g.Sun.R*math.Cos(a)
		y := g.Sun.CY + discProbeR*g.Sun.R*math.Sin(a)
		if g.Moon.R > 0 {
			if math.Hypot(x-g.Moon.CX, y-g.Moon.CY) <= guard {
				continue
			}
		}
		tested++
		if x < 0 || y < 0 || x >= float64(im.W) || y >= float64(im.H) {
			continue
		}
		if float64(im.Pix[0][int(y)*im.W+int(x)]) > discProbeFrac*level {
			present++
		}
	}
	if tested < discProbeMin {
		return 1 // nothing to judge: the occulter covers the ring, which is not damage
	}
	return float64(present) / float64(tested)
}

const (
	discProbeRays = 512
	// discProbeR sits just inside the limb, past the softest part of the edge.
	discProbeR = 0.95
	// discProbeFrac is how bright a point must be, against the crescent's own level, to count as Sun.
	discProbeFrac = 0.25
	discProbeMin  = 24
)

// skyAgreeTol is how far a frame's measured obscuration may sit from the computed one. Five points
// is loose enough for the ordinary scatter of fitting a circle to a crescent and tight enough to
// catch a fit that has found the wrong body.
const skyAgreeTol = 0.05

// sharpestOf picks the highest-scoring frame of a set.
func sharpestOf(window []solar.Frame) solar.Frame {
	best := window[0]
	for _, f := range window[1:] {
		if f.Score > best.Score {
			best = f
		}
	}
	return best
}

// sharperThan prefers a candidate only when its edge could actually be measured. A frame that is not
// a picture of the Sun still carries band-pass energy and can outscore one that is, so an
// unmeasurable edge is a refusal however well the candidate ranked.
func sharperThan(a, b solar.PSF) bool {
	if !a.OK {
		return false
	}
	return !b.OK || a.SigmaPx < b.SigmaPx
}

// framesAtPhase collects every frame whose phase is within tol of the panel's own, capped to a
// sensible stretch of clock so a slow-changing phase cannot pull in half the eclipse.
//
// This is the search a panel actually wants. A thirty-second window is a STACKING window — it exists
// to bound how far the Moon smears while frames are averaged — and using it to choose one frame
// throws away thousands of equally valid candidates for no reason.
func framesAtPhase(frames []solar.Frame, want eclipsegeom.Panel, site eclipsegeom.Site,
	tol float64, capSeconds float64) []solar.Frame {

	target := want.At.UnixMilli()
	capMs := int64(capSeconds * 1000)
	out := make([]solar.Frame, 0, 256)
	for _, f := range frames {
		if f.TimeMs <= 0 || abs64(f.TimeMs-target) > capMs {
			continue
		}
		at := time.UnixMilli(f.TimeMs).UTC()
		if math.Abs(eclipsegeom.At(at, site).Obscuration-want.Obscuration) <= tol {
			out = append(out, f)
		}
	}
	return out
}

// framesAround collects the frames inside one window centred on an instant, widening to the nearest
// few when the centre lands near the end of a clip and the window would otherwise be a runt.
func framesAround(frames []solar.Frame, at time.Time, windowSeconds float64, minFrames int) []solar.Frame {
	if windowSeconds <= 0 {
		windowSeconds = 30
	}
	if minFrames <= 0 {
		minFrames = 12
	}
	halfMs := int64(windowSeconds * 500)
	target := at.UnixMilli()
	var window []solar.Frame
	for _, f := range frames {
		if f.TimeMs > 0 && abs64(f.TimeMs-target) <= halfMs {
			window = append(window, f)
		}
	}
	if len(window) >= minFrames {
		return window
	}
	return nearestFrames(frames, target, minFrames)
}

// nearestFrames is the fallback: the closest frames in time, whatever the window says. A phase at the
// very start of a sixteen-second clip has no symmetric window and is still worth a panel.
func nearestFrames(frames []solar.Frame, target int64, want int) []solar.Frame {
	pool := make([]solar.Frame, 0, len(frames))
	for _, f := range frames {
		if f.TimeMs > 0 {
			pool = append(pool, f)
		}
	}
	sort.SliceStable(pool, func(i, j int) bool {
		return abs64(pool[i].TimeMs-target) < abs64(pool[j].TimeMs-target)
	})
	if len(pool) > want {
		pool = pool[:want]
	}
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].TimeMs < pool[j].TimeMs })
	return pool
}

// midTime is the instant a window's stack actually represents.
func midTime(window []solar.Frame) time.Time {
	lo, hi := window[0].TimeMs, window[0].TimeMs
	for _, f := range window[1:] {
		if f.TimeMs < lo {
			lo = f.TimeMs
		}
		if f.TimeMs > hi {
			hi = f.TimeMs
		}
	}
	return time.UnixMilli(lo + (hi-lo)/2).UTC()
}

// coverageSpans is which stretches of the eclipse the ingested frames actually cover.
//
// It is built from the frames that SURVIVED, not from the clips' durations, so a minute the cloud
// gate emptied is a gap in the coverage rather than a phase the ladder believes it can render.
func coverageSpans(frames []solar.Frame, windowSeconds float64) []eclipsegeom.Span {
	if windowSeconds <= 0 {
		windowSeconds = 30
	}
	breakMs := int64(windowSeconds * 4000)
	bySource := map[string][]int64{}
	for _, f := range frames {
		if f.TimeMs > 0 {
			bySource[f.Source] = append(bySource[f.Source], f.TimeMs)
		}
	}
	var spans []eclipsegeom.Span
	for _, times := range bySource {
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		start := times[0]
		for i := 1; i <= len(times); i++ {
			if i < len(times) && times[i]-times[i-1] <= breakMs {
				continue
			}
			if end := times[i-1]; end > start {
				spans = append(spans, eclipsegeom.Span{
					From: time.UnixMilli(start).UTC(), To: time.UnixMilli(end).UTC()})
			}
			if i < len(times) {
				start = times[i]
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].From.Before(spans[j].From) })
	return spans
}

// sequenceSite is where the capture was made: the clips' own location tag, unless the run overrides
// it. Without one there is no phase to compute and no sequence to render.
func sequenceSite(group solar.Group, p solar.Preset) (eclipsegeom.Site, bool) {
	if p.SiteLatDeg != 0 || p.SiteLonDeg != 0 {
		return eclipsegeom.Site{LatDeg: p.SiteLatDeg, LonDeg: p.SiteLonDeg}, true
	}
	for _, m := range group.Members {
		if m.Video != nil && m.Video.HasSite {
			return eclipsegeom.Site{
				LatDeg: m.Video.LatDeg, LonDeg: m.Video.LonDeg, ElevM: m.Video.ElevM}, true
		}
	}
	return eclipsegeom.Site{}, false
}

// medianRadius is the shared solar radius the panels are rendered at.
func medianRadius(panels []*seqPanel) float64 {
	radii := make([]float64, 0, len(panels))
	for _, p := range panels {
		if p.Frame.Sun.R > 0 {
			radii = append(radii, p.Frame.Sun.R)
		}
	}
	if len(radii) == 0 {
		return 0
	}
	sort.Float64s(radii)
	return radii[len(radii)/2]
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// obscurationGap is how far the measured phase sits from the computed one, in points.
func obscurationGap(measured, predicted float64) float64 {
	return math.Abs(measured-predicted) * 100
}
