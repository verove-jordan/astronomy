package pipeline

// nightpano.go is the sky-panorama mode: a hand-swept arc of pointings assembled onto ONE spherical
// canvas.
//
// It is the milkyway recipe run N times and then joined. Each panel stacks through
// nightscape.Process exactly as a single-pointing milkyway run does — same registration, same
// calibration, same clean-sky stack — and what this file adds is everything that only makes sense
// once there is more than one pointing: grouping the frames into panels from the phone's own
// pointing metadata, plate-solving each panel at a field width Siril's solver cannot touch, fitting
// the LENS the panels share, and reprojecting them onto a canvas.
//
// The mosaic mode is not reusable for this and the reason is geometric, not incidental: it projects
// onto a gnomonic (TAN) tangent plane, which is undefined 90 degrees from its centre. A Milky Way
// arch is wider than that, so it is not distorted in TAN, it is unrepresentable. See
// internal/skypano.

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/nightscape"
	"github.com/verove-jordan/astronomy/internal/panelgroup"
	"github.com/verove-jordan/astronomy/internal/pointing"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
	"github.com/verove-jordan/astronomy/internal/skypano"
	"github.com/verove-jordan/astronomy/internal/starfield"
)

// The panorama's tuning constants. Each is a measured choice, not a default that happened to work.
const (
	// panoMaxStars is how deep both star lists go. The two lists must reach COMPARABLE DEPTH or the
	// quads never agree: 400 detections against the 800 brightest catalogue stars matched 17, while
	// 2000 against 4000 matched 64. A quad needs all four of its stars present on both sides.
	panoMaxStars = 8000
	// panoCatRadiusDeg covers a phone's field with room for the pointing to be wrong.
	panoCatRadiusDeg = 50

	// panoBundleStartPx is where the shared-lens fit begins matching. It must exceed the distortion
	// being measured — on a phone that is up to ~18 px — or the fit never sees the outer-field stars
	// that carry it and reports a small residual over the inner field it kept.
	panoBundleStartPx = 60
	panoBundleFinalPx = 3
	panoBundleRounds  = 10

	panoPhotomSamples = 40000
	panoPhotomRounds  = 8

	// panoSharpFromBestPx is the two-band blend's low-pass radius on the canvas: below it the sky
	// comes from every panel averaged, above it the detail comes from the best-covering panel alone.
	// See RenderOptions.SharpFromBestPx.
	panoSharpFromBestPx = 64

	// The panorama's look, measured against a reference frame the photographer was happy with
	// (p5=20.5 p25=34.5 p50=49.1 p75=65.7 p95=93.2 in 8-bit sRGB over the sky).
	//
	// panoGradeBlackPct is 1 rather than the stock 20 because the stock value clips a FIFTH of the
	// canvas to pure black. panoGradeFloor is the pedestal that stops the darkest sky being black at
	// all — every other control only takes light away, so without it the floor lands wherever the
	// subtraction left it. Together with the background target they reproduce the reference to within
	// a couple of values at every percentile, and crush nothing: measured 0.00% at pure black.
	//
	// panoGradeTargetBg was 0.085 while the canvas was still half extrapolated fill. Since the sky
	// selection stopped throwing away the dark half of every frame the canvas carries all of its real
	// sky, which moved the histogram the grade keys off: re-measured against the same reference, 0.028
	// puts the median at 49.4 against its 49.1 and the shadows at 20.0 against its 20.5.
	panoGradeBlackPct = 1.0
	panoGradeTargetBg = 0.028
	panoGradeFloor    = 0.007
)

// panoPanel is one pointing: its frames, its stack, and — once solved — where it looks.
type panoPanel struct {
	label   string
	frames  []string
	center  pointing.Frame
	spread  float64
	skyFITS string
	outDir  string // this panel's own nightscape run directory, where its linear layers live
	preview string
	stacked int
	cam     skypano.Camera
	img     *fits.Image
	det     []skypano.Detection // detected once; the bundle needs the same list the solve used
	cat     [][3]float64        // the catalogue field around the solved centre
	solved  bool
	stars   int
	rms     float64
	// valid is the panel's occluder mask (nil = the whole panel is usable), computed once and shared
	// by every canvas the run draws.
	valid    []float32
	validSet bool
}

// ProcessNightpano stacks every pointing, solves them against one shared lens, and assembles the
// panorama.
func ProcessNightpano(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	defer opts.freePulledMasters(ctx)

	frames, err := inspect.ListRawFramesMany(opts.scanRoots())
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no raw frames found in %s", opts.InputDir)
	}

	workAbs, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	outAbs, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return nil, err
	}
	runID := time.Now().Format("20060102_150405")
	object := smartObject(opts.InputDir)
	if object == "" || object == "." || object == string(filepath.Separator) {
		object = "nightpano"
	}
	workRun := filepath.Join(workAbs, object, "run_"+runID)
	outDir := filepath.Join(outAbs, object, runID)
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}
	res := &Result{InputDir: opts.InputDir, OutputDir: outDir, Object: object, RunID: runID}
	opts.PriorObject = object

	// Calibration frames first: a dark shot without moving the tripod sits at the same pointing as
	// the panel before it, so no amount of geometry can tell them apart — only the pixels can.
	lights, darkFrames, flatFrames, biasFrames := splitCalibrationFrames(ctx, frames)
	panels, groupWarns := groupPanels(lights, opts.Preset)
	res.Warnings = append(res.Warnings, groupWarns...)
	for _, w := range groupWarns {
		opts.report(Progress{Line: "nightpano: " + w})
	}
	if len(panels) < 2 {
		// One pointing is a milkyway run, and saying so is better than assembling a panorama of one.
		opts.report(Progress{Line: "only one pointing found — processing as a milkyway nightscape"})
		single := opts
		p := *opts.Preset
		p.Mode = mode.Milkyway
		single.Preset = &p
		r, perr := ProcessOSC(ctx, single)
		if r != nil {
			r.Warnings = append(r.Warnings,
				"nightpano: only one pointing was found — processed as a plain milkyway nightscape")
		}
		return r, perr
	}

	opts.steps = newStepper(opts.report, len(panels)+2)
	defer opts.finishSteps()

	stackNightpanoPanels(ctx, opts, res, panels, workRun, outDir, darkFrames, flatFrames, biasFrames)
	usable := 0
	for _, p := range panels {
		if p.skyFITS != "" {
			usable++
		}
	}
	if usable < 2 {
		writeRunJSON(outDir, res)
		return res, fmt.Errorf("nightpano: only %d panel(s) stacked — see the run warnings", usable)
	}

	opts.beginStep("solve panels + fit the shared lens")
	solved := solveNightpanoPanels(ctx, opts, res, panels)
	if len(solved) < 2 {
		writeRunJSON(outDir, res)
		return res, fmt.Errorf("nightpano: only %d panel(s) could be plate-solved — see the run warnings", len(solved))
	}

	opts.beginStep("assemble the canvas")
	outputs := assembleNightpano(ctx, opts, res, solved, outDir)
	if len(outputs) == 0 {
		writeRunJSON(outDir, res)
		return res, fmt.Errorf("nightpano: the canvas could not be assembled — see the run warnings")
	}

	res.Final = &postprocess.Result{
		Mode: "OSC-RGB nightpano", Channels: []string{"RGB"}, Outputs: outputs,
		Notes: []string{fmt.Sprintf("%d pointings on one spherical canvas", len(solved))},
	}
	res.StagePreviews = collectStagePreviews(outDir)
	stampFinishQuality(res)
	opts.finishSteps()
	writeRunJSON(outDir, res)
	return res, nil
}

// groupPanels reads each light's pointing from its own metadata and segments the session.
//
// The phone records where it was aimed, so this is a measurement rather than a guess — and it is why
// the input can be one flat folder of the whole night instead of hand-sorted per-pointing folders.
func groupPanels(lights []string, preset *mode.Preset) ([]*panoPanel, []string) {
	var warns []string
	var pf []panelgroup.Frame
	noPointing := 0
	for _, path := range lights {
		m := rawmeta.Read(path)
		p, ok := pointing.FromMeta(m)
		if !ok {
			noPointing++
			continue
		}
		pf = append(pf, panelgroup.Frame{Path: path, At: time.UnixMilli(m.TakenAtMs).UTC(), Pointing: p})
	}
	if noPointing > 0 {
		warns = append(warns, fmt.Sprintf(
			"%d of %d frame(s) carry no usable pointing metadata and were skipped — nightpano groups "+
				"pointings from the camera's own gravity vector and heading", noPointing, len(lights)))
	}
	if len(pf) == 0 {
		return nil, warns
	}

	o := panelgroup.DefaultOptions()
	if preset != nil && preset.PanoGroupStepDeg > 0 {
		o.StepDeg = preset.PanoGroupStepDeg
	}
	var out []*panoPanel
	for _, g := range panelgroup.Group(pf, o) {
		p := &panoPanel{label: g.Label, center: g.Center, spread: g.SpreadDeg}
		for _, f := range g.Frames {
			p.frames = append(p.frames, f.Path)
		}
		out = append(out, p)
	}
	return out, warns
}

// stackNightpanoPanels runs the nightscape recipe once per pointing, sequentially. Sequential
// deliberately: Siril is already parallel inside, and a panel's stack is the memory high-water mark
// of the whole run, so overlapping them would multiply peak memory rather than save wall clock.
func stackNightpanoPanels(ctx context.Context, opts Options, res *Result, panels []*panoPanel,
	workRun, outDir string, darks, flats, bias []string) {
	workAbs, _ := filepath.Abs(opts.WorkDir)
	libDir, _ := libraryDir(opts, workAbs)
	var grax *graxpert.Runner
	if opts.Preset != nil && opts.Preset.BackgroundAI {
		grax = opts.Graxpert
	}

	for i, p := range panels {
		step := fmt.Sprintf("panel %s · stack %d frames", p.label, len(p.frames))
		prog := opts.beginStep(step)
		pOut := filepath.Join(outDir, "panels", p.label)
		if err := fsutil.EnsureDir(pOut); err != nil {
			warnLive(opts, res, fmt.Sprintf("nightpano: panel %s dropped — %v", p.label, err))
			continue
		}
		panelWork := filepath.Join(workRun, "panel_"+p.label)
		nres, err := nightscape.Process(ctx, nightscape.Options{
			Siril:                 opts.Runner,
			Graxpert:              grax,
			Frames:                p.frames,
			WorkDir:               panelWork,
			OutDir:                pOut,
			Look:                  nightscape.LookByName(opts.Preset.Look),
			Brightness:            opts.Preset.BackgroundLevel,
			SaturationScale:       opts.Preset.Saturation,
			HighlightCeilOverride: opts.Preset.HighlightCeil,
			ColorCalibration:      opts.Preset.ColorCalibration,
			Meteors:               opts.Preset.KeepMeteors,
			FlatRadialOnly:        opts.Preset.FlatRadialOnly,
			Solve:                 opts.Solve,
			Spcc:                  opts.Spcc,
			Focal35mm:             nightscape.ReadFocal35mm(p.frames),
			DarkDir:               opts.DarkDir,
			FlatDir:               opts.FlatDir,
			BiasDir:               opts.BiasDir,
			DarkFrames:            darks,
			FlatFrames:            flats,
			BiasFrames:            bias,
			PhoneCalib:            opts.PhoneCalib,
			LibraryDir:            libDir,
			LibraryMirror:         opts.LibraryMirror,
			Orientation:           opts.Preset.Orientation,
			OnProgress:            prog,
		})
		if err != nil {
			warnLive(opts, res, fmt.Sprintf("nightpano: panel %s failed to stack — %v", p.label, err))
			continue
		}
		res.Warnings = append(res.Warnings, nres.Warnings...)
		p.skyFITS, p.preview, p.stacked = nres.SkyFITS, nres.PreviewPNG, nres.StackedFrames
		p.outDir = pOut

		// This panel's Siril scratch is dead the moment its stack is written: everything later stages
		// read — the stack, the linear layers, the sky mask — lives in pOut, not here. Left behind it
		// is about 8 GB per panel of converted and registered FITS, so a nine-panel run ends holding
		// seventy gigabytes of files nothing will open again. Two of those filled a 926 GB disk and
		// killed the run that was using it.
		if err := os.RemoveAll(panelWork); err != nil {
			warnLive(opts, res, fmt.Sprintf("nightpano: panel %s scratch not reclaimed — %v", p.label, err))
		}
		res.Channels = append(res.Channels, ChannelResult{
			Filter: "RGB", InputFrames: nres.InputFrames, StackedFrames: nres.StackedFrames,
			OutputPath: nres.SkyFITS, PreviewPath: nres.PreviewPNG,
		})
		if p.preview != "" {
			idx, tot := opts.stepPos()
			opts.report(Progress{Step: step, Index: idx, Total: tot, Preview: p.preview, Session: p.label})
			captureSessionPreview(ctx, opts, outDir, ordSession+i*2, stageStacked, "RGB", p.label, p.preview, false)
		}
	}
}

// solveNightpanoPanels plate-solves every panel and then fits the ONE lens they share.
//
// The shared fit is not a refinement, it is the difference between a panorama and a smear. Solved
// separately, each panel matches its own stars well and they still disagree with each other by more
// than a canvas pixel — because a per-panel fit matches inside a few pixels and so drops exactly the
// outer-field stars that carry the distortion. See internal/skypano/bundle.go.
func solveNightpanoPanels(ctx context.Context, opts Options, res *Result, panels []*panoPanel) []*panoPanel {
	cat, deep := deepstars.Load(opts.DeepStarCat)
	if !deep {
		warnLive(opts, res, "nightpano: the deep star catalogue is not installed — run `just download-deepstars`; "+
			"panels cannot be plate-solved at this field width without it")
		return nil
	}

	var ready []*panoPanel
	var dropped int
	for _, p := range panels {
		if p.skyFITS == "" {
			continue
		}
		im, ferr := fits.ReadImage(p.skyFITS)
		if ferr != nil || im == nil {
			warnLive(opts, res, fmt.Sprintf("nightpano: panel %s stack could not be read back — %v", p.label, ferr))
			continue
		}
		p.img = im

		m := rawmeta.Read(p.frames[len(p.frames)/2])
		var det []starfield.Star
		detectAt := func(sigma float64) {
			det = starfield.Detect(im.Pix[1], im.W, im.H,
				starfield.Options{Sigma: sigma, BoxRadius: 6, MinSeparation: 10, Max: panoMaxStars})
			p.det, dropped = skyOnlyDetections(det, p.outDir, im.W, im.H)
			if dropped > 0 {
				opts.report(Progress{Line: fmt.Sprintf(
					"panel %s: %d of %d detections are on the ground, not sky — left out of the solve",
					p.label, dropped, len(det))})
			}
		}
		detectAt(panoDetectSigma)
		epoch := time.UnixMilli(m.TakenAtMs).UTC()
		catFor := func(ra, dec float64) [][3]float64 {
			cs := cat.InField(ra, dec, panoCatRadiusDeg, panoMaxStars, epoch)
			v := make([][3]float64, len(cs))
			for i, s := range cs {
				v[i] = skypano.RADecToVec(s.RADeg, s.DecDeg)
			}
			return v
		}
		// FITS rows run bottom-up, so the array is a MIRROR of the picture — and quad codes are not
		// reflection-invariant, so getting this wrong returns exactly chance rather than a poor answer.
		solve := func() (skypano.Camera, skypano.Solution, float64, bool) {
			return skypano.AutoSolve(p.center, m.Orientation, im.W, im.H,
				float64(m.FocalLength35mm), true, catFor, p.det, skypano.DefaultQuadSolveOptions())
		}
		cam, sol, az, ok := solve()
		if ok && !plausibleSolve(sol, az, p.center.AzDeg) {
			opts.report(Progress{Line: fmt.Sprintf(
				"panel %s: rejecting a solve on %d stars pointing %.0f degrees from the recorded %.0f",
				p.label, sol.Matches, angleGapDeg(az, p.center.AzDeg), p.center.AzDeg)})
			ok = false
		}
		if !ok {
			// A panel aimed low is hazy, light-polluted and simply has fewer stars over the detection
			// threshold: measured on a real one, the default found 1535 objects where the zenith panels
			// hit the 8000 cap, and it would not solve. Digging deeper doubles them (3142 at sigma 3)
			// while the ground contamination stays flat at about 170 — those are the same fixed street
			// lamps either way. So the second attempt is worth making, and only when the first failed:
			// panels that solve straight away never pay for it.
			opts.report(Progress{Line: fmt.Sprintf(
				"panel %s did not solve on %d stars — looking deeper", p.label, len(p.det))})
			detectAt(panoDeepDetectSigma)
			cam, sol, az, ok = solve()
			if ok && !plausibleSolve(sol, az, p.center.AzDeg) {
				opts.report(Progress{Line: fmt.Sprintf(
					"panel %s: the deeper search matched only %d stars, %.0f degrees off the recorded bearing — "+
						"refused", p.label, sol.Matches, angleGapDeg(az, p.center.AzDeg))})
				ok = false
			}
		}
		if !ok {
			warnLive(opts, res, fmt.Sprintf(
				"nightpano: panel %s could not be plate-solved (%d detections) — it is left out of the canvas",
				p.label, len(p.det)))
			continue
		}
		p.cam, p.solved, p.stars, p.rms = cam, true, sol.Matches, sol.RMSPx
		ra, dec := skypano.VecToRADec(cam.Axis())
		// The catalogue around the SOLVED centre, which the bundle refits against.
		for _, s := range cat.InField(ra, dec, panoCatRadiusDeg, panoMaxStars, epoch) {
			p.cat = append(p.cat, skypano.RADecToVec(s.RADeg, s.DecDeg))
		}
		opts.report(Progress{Line: fmt.Sprintf(
			"panel %s solved: %d stars, rms %.2f px, RA %.2f Dec %+.2f (azimuth %.0f, recorded %.0f)",
			p.label, sol.Matches, sol.RMSPx, ra, dec, az, p.center.AzDeg)})
		ready = append(ready, p)
	}
	if len(ready) < 2 {
		return ready
	}

	cams := make([]skypano.Camera, len(ready))
	cats := make([][][3]float64, len(ready))
	dets := make([][]skypano.Detection, len(ready))
	for i, p := range ready {
		cams[i], cats[i], dets[i] = p.cam, p.cat, p.det
	}
	so := skypano.DefaultSolveOptions()
	so.MatchRadiusPx, so.FitRadiusPx = panoBundleStartPx, panoBundleFinalPx
	got, sols, ok := skypano.BundleLens(cams, cats, dets, so, panoBundleRounds)
	if !ok {
		warnLive(opts, res, "nightpano: the shared-lens fit did not converge — keeping the per-panel solutions, "+
			"which usually means stars are drawn as short dashes where panels overlap")
		return ready
	}
	for i, p := range ready {
		p.cam, p.stars, p.rms = got[i], sols[i].Matches, sols[i].RMSPx
	}
	opts.report(Progress{Line: fmt.Sprintf(
		"shared lens: %.2f arcsec/px, distortion k1 %+.4f k2 %+.4f k3 %+.4f",
		sols[0].ScaleArcsecPerPix, got[0].K1, got[0].K2, got[0].K3)})
	return ready
}

// assembleNightpano renders every requested projection and returns the files written.
func assembleNightpano(ctx context.Context, opts Options, res *Result, panels []*panoPanel, outDir string) []string {
	sky := make([]skypano.Panel, len(panels))
	for i, p := range panels {
		sky[i] = skypano.Panel{Name: p.label, Cam: p.cam, Img: p.img, Valid: panelValid(opts, res, p)}
	}
	scale := opts.Preset.PanoScaleDegPerPix
	if scale <= 0 {
		scale = 0.03
	}

	// The horizon frame needs to know where and when it is standing; the sky-only canvases do not. It
	// also needs its OWN panels: an arch is one sky over one horizon, so a run spanning two sites or
	// two nights draws it from the largest single session and leaves the rest to the sky canvases.
	arch, siteLat, lst, epoch, haveEpoch := panoArchCluster(panels)

	var outputs []string
	for _, proj := range panoProjections(opts.Preset.PanoProjection) {
		use := sky
		if proj.frame == skypano.Horizon {
			if !haveEpoch {
				warnLive(opts, res, "nightpano: no arch canvas — the frames carry no position or no time, "+
					"so there is no horizon to draw them over")
				continue
			}
			use = make([]skypano.Panel, len(arch))
			for i, p := range arch {
				use[i] = skypano.Panel{Name: p.label, Cam: p.cam, Img: p.img, Valid: panelValid(opts, res, p)}
			}
			opts.report(Progress{Line: fmt.Sprintf("arch drawn as the sky stood at %s UTC from %.4f N, "+
				"from %d of %d panels", epoch.UTC().Format("15:04:05"), siteLat, len(arch), len(panels))})
			if len(arch) < len(panels) {
				warnLive(opts, res, fmt.Sprintf(
					"nightpano: the arch uses %d of %d panels — the rest were shot from another place or on "+
						"another night, and an arch is one sky over one horizon", len(arch), len(panels)))
			}
		}
		c, err := skypano.PlanCanvasAt(use, proj.projection, proj.frame, scale, siteLat, lst)
		if err != nil {
			warnLive(opts, res, fmt.Sprintf("nightpano: %s canvas could not be planned — %v", proj.name, err))
			continue
		}
		opts.report(Progress{Line: fmt.Sprintf("%s canvas %dx%d at %.3f deg/px", proj.name, c.W, c.H, scale)})

		skypano.MatchPhotometry(use, c, panoPhotomSamples, panoPhotomRounds)
		ro := skypano.DefaultRenderOptions()
		// Take the detail from one panel at a time. Panels are fitted to the catalogue independently
		// and land about 2.3 px from it, so two of them place the same star some 3 px apart —
		// measured on this session as a 45% broader autocorrelation skirt where panels overlap than
		// where they do not. Averaging them draws the star twice. 64 px is comfortably above that
		// disagreement and comfortably below the ~2400 px a panel spans, so stars come from one panel
		// and the sky's level still comes from all of them.
		ro.SharpFromBestPx = panoSharpFromBestPx
		img, cov, err := skypano.Render(use, c, ro)
		if err != nil {
			warnLive(opts, res, fmt.Sprintf("nightpano: %s render failed — %v", proj.name, err))
			continue
		}
		// A panel's stack covers its WHOLE frame, and the panel that carries the horizon is aimed low
		// enough that a third of its frame is beach. Rendered as sky, that beach lands under the
		// horizon as a smooth, smeared copy of itself — the sigma-clipped stack of a landscape the
		// frames drifted across — complete with the town's lights. It is why the arch had a pale slab
		// under it long before anything was composited on top, and why painting the clean landscape
		// over the middle of it left the smear showing round the edges as a bright frame.
		//
		// So the sky's coverage stops at the horizon. Below it the picture is the landscape or it is
		// nothing, which is what Canvas.PixToSky's own comment always said it should be.
		// The landscape is PREPARED first — it is not mixed in until after both layers are curved,
		// but the sky's horizon cut needs to know where it lands. See clearBelowHorizon.
		var ground *archLayer
		var noMeasure []bool
		if proj.frame == skypano.Horizon && opts.Preset.PanoForeground {
			if ground = archForeground(opts, res, arch, cov, c, lst); ground != nil {
				noMeasure = ground.NoMeasure
			}
		}
		if proj.frame == skypano.Horizon {
			cleared := clearBelowHorizon(cov, c, ground)
			opts.report(Progress{Line: fmt.Sprintf(
				"%s: %.1f%% of the canvas is below the horizon — the sky does not draw there", proj.name,
				100*float64(cleared)/float64(len(cov)))})
		}

		// The background comes off while the canvas is still nothing but sky. A background model is
		// evaluated and subtracted EVERYWHERE, so doing this after the landscape was composited took a
		// surface fitted to the sky off the ground as well — see nightpano_background.go. GraXpert
		// first (it follows the real dome rather than a low-order surface); the polynomial is what
		// happens when it is absent or fails.
		if opts.Preset.PanoBackground {
			if !graxpertCanvasBackground(ctx, opts, res, img, cov, outDir, proj.name) {
				fo := skypano.DefaultFlattenOptions()
				if opts.Preset.PanoBandMaskLatDeg > 0 {
					fo.MaskLatDeg = opts.Preset.PanoBandMaskLatDeg
				}
				bg, ferr := skypano.Flatten(img, cov, c, fo)
				if ferr != nil {
					warnLive(opts, res, fmt.Sprintf(
						"nightpano: %s background not removed (%v) — the sky keeps its light-pollution dome", proj.name, ferr))
				} else {
					opts.report(Progress{Line: fmt.Sprintf(
						"%s background: order %d from %d tiles outside the band", proj.name, bg.Order, bg.Tiles)})
				}
			}
		}

		base := filepath.Join(outDir, "pano_"+proj.name)
		if err := img.WriteFITS(base + "_linear.fits"); err != nil {
			warnLive(opts, res, fmt.Sprintf("nightpano: %s linear canvas not written — %v", proj.name, err))
		} else {
			outputs = append(outputs, base+"_linear.fits")
		}
		// Everything the grade consumes, persisted beside the canvas it belongs to. The linear canvas
		// alone is not enough to reproduce a render: the grade measures through the coverage, the band
		// mask needs the canvas geometry, and the landscape is a separate layer. Without these, re-
		// grading meant re-stacking every panel — an hour to answer a question about a black point.
		persistGradeInputs(opts, res, base, img, cov, c, ground)

		go2 := skypano.DefaultGradeOptions()
		go2.BlackPct, go2.TargetBg, go2.Floor = panoGradeBlackPct, panoGradeTargetBg, panoGradeFloor
		if opts.Preset.PanoBandMaskLatDeg > 0 {
			go2.BandMaskLatDeg = opts.Preset.PanoBandMaskLatDeg
		}
		// BackgroundLevel is deliberately NOT read here. It is already this run's per-panel Brightness
		// (see stackNightpanoPanels), and a panel's stacked_sky.fits is written AFTER gradeCompose has
		// stretched it — so a value set to darken the canvas silently re-stretches every panel the
		// canvas is assembled from. That is what made one run's whole sky flat and its blacks crushed.
		// A canvas-brightness control needs its own preset field, not a second meaning for this one.
		if opts.Preset.Saturation > 0 {
			go2.Saturation = opts.Preset.Saturation
		}
		go2.Exclude = noMeasure
		keep := skypano.Grade(img, cov, c, go2)

		// The landscape joins the picture here, in display-referred light, each layer having been
		// curved on its own terms. Composited in LINEAR light before the grade — which is what this
		// used to do — a beach an order of magnitude and a half below the horizon glow gets the sky's
		// curve and comes back black; see nightscape.StretchForeground.
		if ground != nil {
			look := nightscape.LookByName(opts.Preset.Look)
			nightscape.StretchForeground(ground.Img, ground.White, look.AsinhIntensityFG)
			compositeGround(img, keep, ground)
		}
		if err := skypano.WritePNG(img, keep, base+".png", 0); err != nil {
			warnLive(opts, res, fmt.Sprintf("nightpano: %s image not written — %v", proj.name, err))
			continue
		}
		outputs = append(outputs, base+".png")
		idx, tot := opts.stepPos()
		opts.report(Progress{Step: "assemble the canvas", Index: idx, Total: tot, Preview: base + ".png"})
		capturePreview(ctx, opts, outDir, ordFinal, stageFinal, "", base+".png", false)
	}
	return outputs
}

const (
	// panoOneNight and panoOneSiteKm bound what can be drawn as a single arch. Twelve hours admits a
	// dusk-to-dawn session and rejects two nights; twenty-five kilometres admits moving along a beach
	// and rejects driving to another region.
	// panoMinSolveStars is the fewest detections worth handing the solver. Below it the ground mask is
	// refused rather than applied: a contaminated list that might solve beats a clean one that cannot.
	panoMinSolveStars = 60

	// panoDetectSigma is the detection threshold a panel is solved on, and panoDeepDetectSigma the one
	// it is retried at when that fails. See the retry for the measurement behind the second value.
	panoDetectSigma     = 8.0
	panoDeepDetectSigma = 3.0

	// panoMinSolveMatches is the fewest matched stars a solution may rest on. Real panels on this
	// session matched 541 to 923; the one false solve matched 90.
	panoMinSolveMatches = 200
	// panoAzTolDeg is how far a solved bearing may sit from the recorded one, around either 0 or 180
	// degrees. Generous because the phone's compass is only ever roughly right — but 90 degrees off is
	// not a compass error, it is a wrong answer.
	panoAzTolDeg = 45.0

	panoOneNight  = 12 * time.Hour
	panoOneSiteKm = 25.0
)

// panoProjection names one requested output canvas.
type panoProjection struct {
	name       string
	projection skypano.Projection
	frame      skypano.Frame
}

// panoProjections resolves the preset's projection choice. Unknown values fall back to the
// stereographic canvas rather than failing a run that has already done all its stacking.
func panoProjections(s string) []panoProjection {
	stereo := panoProjection{"stereographic", skypano.Stereographic, skypano.Equatorial}
	// The galactic strip lays the Milky Way out as a level band, which is the classic panorama of it.
	galactic := panoProjection{"galactic_strip", skypano.Equirectangular, skypano.Galactic}
	// The arch: the sky as it stood over the ground.
	//
	// STEREOGRAPHIC, not equirectangular, and the difference is not cosmetic. Laying azimuth straight
	// across the canvas is fine near the horizon and ruinous near the top: any field that contains the
	// ZENITH spans every azimuth, so the crown of the arch is stretched over the whole width. Measured
	// on a real session that made an 11871-pixel canvas whose top was a smear. Stereographic has no
	// pole to blow up — only the antipode, which is under the observer's feet — and being conformal it
	// keeps star fields the right shape, which is the whole reason the sky canvases use it too.
	arch := panoProjection{"altaz_arch", skypano.Stereographic, skypano.Horizon}
	switch s {
	case "galactic":
		return []panoProjection{galactic}
	case "altaz":
		return []panoProjection{arch}
	case "both":
		return []panoProjection{stereo, galactic}
	case "all":
		return []panoProjection{stereo, galactic, arch}
	default:
		return []panoProjection{stereo}
	}
}

// panelValid finds whatever is in the way in a panel — a bag on the ground, a tripod leg, the drift
// rim the stack could not fill — and returns the mask that keeps it off the canvas. Computed once per
// panel and cached, because every canvas a run draws asks for the same answer.
func panelValid(opts Options, res *Result, p *panoPanel) []float32 {
	if p.validSet {
		return p.valid
	}
	p.validSet = true
	if p.img == nil {
		return nil
	}
	p.valid = skypano.FindOccluders(p.img, skypano.DefaultOccluderOptions())
	if p.valid != nil {
		n := 0
		for _, v := range p.valid {
			if v < 0.5 {
				n++
			}
		}
		warnLive(opts, res, fmt.Sprintf(
			"nightpano: panel %s has something in the way over %.1f%% of its frame — that part is left out of the canvas",
			p.label, 100*float64(n)/float64(len(p.valid))))
	}
	return p.valid
}

// panoEpoch picks the instant the arch is drawn for, and the site it is drawn from.
//
// The panels span two hours, over which the sky turned about thirty degrees, so "where was this
// relative to the ground" has a different answer for every one of them. The middle of the session is
// chosen because it minimises the largest correction any single panel gets, and the answer is
// reported so that the arch is understood as the sky at a moment rather than an average of the night.
//
// ok is false when the frames carry no site or no time — and also when they were not all shot from ONE
// PLACE on ONE NIGHT.
//
// That second refusal matters more than it looks. An arch is a picture of a particular sky over a
// particular horizon, so panels from two sites average to a horizon neither session had, and panels
// from two nights average to an instant when nothing was being shot. Both produce a confident,
// plausible, wrong picture rather than an obvious failure. A real session combining the Loire-Atlantique
// coast with a site 390 km inland three nights later would have been drawn over a horizon in between.
// The SKY canvases have no such problem and are unaffected: stars are at the same right ascension and
// declination from anywhere on Earth, which is exactly why a mosaic can span sessions at all.
func panoEpoch(panels []*panoPanel) (latDeg, lstDeg float64, at time.Time, ok bool) {
	var withTime []*panoPanel
	for _, p := range panels {
		if p.center.HasSite && p.center.HasTime {
			withTime = append(withTime, p)
		}
	}
	if len(withTime) == 0 {
		return 0, 0, time.Time{}, false
	}
	first, last := withTime[0].center.At, withTime[0].center.At
	var sumLat, sumLon float64
	for _, p := range withTime {
		if p.center.At.Before(first) {
			first = p.center.At
		}
		if p.center.At.After(last) {
			last = p.center.At
		}
		sumLat += p.center.LatDeg
		sumLon += p.center.LonDeg
	}
	if span := last.Sub(first); span > panoOneNight {
		return 0, 0, time.Time{}, false
	}
	if spreadKm(withTime) > panoOneSiteKm {
		return 0, 0, time.Time{}, false
	}
	mid := first.Add(last.Sub(first) / 2)
	lat := sumLat / float64(len(withTime))
	lon := sumLon / float64(len(withTime))
	return lat, astro.LST(mid, lon), mid, true
}

// panoArchCluster picks the panels that belong to ONE sky over ONE horizon, and the instant to draw
// them for.
//
// An arch is a picture of a particular night from a particular spot. Panels from two sites average to
// a horizon neither session had, and panels from two nights to an instant when nothing was being shot
// — both give a confident, plausible, wrong picture rather than an obvious failure. Refusing outright
// was the first answer and it is unhelpful: a run combining two sessions then yields no arch at all,
// and so no foreground either, forcing the whole thing to be repeated single-session.
//
// So the panels are split into sessions and the LARGEST one draws the arch, while every panel still
// contributes to the sky canvases — those are in equatorial coordinates, where a star is in the same
// place whoever photographs it and from wherever.
//
// Splitting walks the panels in time order and starts a session whenever the gap or the move from the
// session's start exceeds what one night from one spot can be. Measuring against the session's start
// rather than the previous panel matters: a slow drift of a few kilometres at a time would otherwise
// chain across a whole region without ever tripping the test.
func panoArchCluster(panels []*panoPanel) (arch []*panoPanel, latDeg, lstDeg float64, at time.Time, ok bool) {
	var usable []*panoPanel
	for _, p := range panels {
		if p.center.HasSite && p.center.HasTime {
			usable = append(usable, p)
		}
	}
	if len(usable) == 0 {
		return nil, 0, 0, time.Time{}, false
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].center.At.Before(usable[j].center.At) })

	var sessions [][]*panoPanel
	cur := []*panoPanel{usable[0]}
	for _, p := range usable[1:] {
		head := cur[0].center
		sameNight := p.center.At.Sub(head.At) <= panoOneNight
		sameSpot := greatCircleKm(head.LatDeg, head.LonDeg, p.center.LatDeg, p.center.LonDeg) <= panoOneSiteKm
		if sameNight && sameSpot {
			cur = append(cur, p)
			continue
		}
		sessions = append(sessions, cur)
		cur = []*panoPanel{p}
	}
	sessions = append(sessions, cur)

	best := sessions[0]
	for _, s := range sessions[1:] {
		if len(s) > len(best) {
			best = s
		}
	}
	lat, lon := 0.0, 0.0
	for _, p := range best {
		lat += p.center.LatDeg
		lon += p.center.LonDeg
	}
	lat, lon = lat/float64(len(best)), lon/float64(len(best))
	mid := best[0].center.At.Add(best[len(best)-1].center.At.Sub(best[0].center.At) / 2)
	return best, lat, astro.LST(mid, lon), mid, true
}

// spreadKm is the greatest distance between any two panels' capture sites.
func spreadKm(panels []*panoPanel) float64 {
	worst := 0.0
	for i := range panels {
		for j := i + 1; j < len(panels); j++ {
			a, b := panels[i].center, panels[j].center
			if d := greatCircleKm(a.LatDeg, a.LonDeg, b.LatDeg, b.LonDeg); d > worst {
				worst = d
			}
		}
	}
	return worst
}

func greatCircleKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthKm = 6371.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * earthKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// skyOnlyDetections keeps the detections that lie in the SKY, using the panel's own sky/foreground
// mask, and reports how many it dropped.
//
// This is what lets a panel with a horizon in it be plate-solved at all. The quad solver matches
// asterisms against a star catalogue, and the ground supplies shapes that are bright, sharp and
// completely unlike stars: measured on the low panel of a real session — half its field dark land and
// sea, with a town's chain of street lamps running through it — the detector returned 1535 objects
// and the solve failed outright. A street lamp is not in any catalogue, and enough of them drown the
// asterisms that are.
//
// If the mask is missing, the wrong size, or would leave too few stars to solve with, everything is
// kept: a panel that might solve on a contaminated list is better than one guaranteed not to solve at
// all, and the caller already reports a failure honestly.
func skyOnlyDetections(det []starfield.Star, panelDir string, w, h int) ([]skypano.Detection, int) {
	all := make([]skypano.Detection, len(det))
	for i, d := range det {
		all[i] = skypano.Detection{X: d.X, Y: d.Y}
	}
	mask, err := fits.ReadImage(filepath.Join(panelDir, "sky_alpha.fits"))
	if err != nil || mask == nil || len(mask.Pix) == 0 {
		return all, 0
	}
	// The mask is written BEFORE the display orientation and the stack AFTER it, so on a portrait
	// panel they arrive transposed — 4002x2988 against 2988x4002. Comparing them directly makes this
	// filter refuse itself on every panel and do nothing at all, which is exactly what it did until it
	// was measured on a real one. The run persists the transform it chose; apply the same one.
	if b, rerr := os.ReadFile(filepath.Join(panelDir, "grade.orient")); rerr == nil && len(b) > 0 {
		mask = nightscape.Orient(mask, strings.TrimSpace(string(b)))
	}
	if mask.W != w || mask.H != h {
		return all, 0
	}
	const minSky = 0.9 // the mask feathers at the horizon; a star half in the ground is not trustworthy
	kept := all[:0:0]
	for i, d := range det {
		x, y := int(d.X), int(d.Y)
		if x < 0 || y < 0 || x >= w || y >= h {
			continue
		}
		if float64(mask.Pix[0][y*w+x]) >= minSky {
			kept = append(kept, all[i])
		}
	}
	if len(kept) < panoMinSolveStars {
		return all, 0
	}
	return kept, len(all) - len(kept)
}

// plausibleSolve rejects a solution that is arithmetically valid and physically wrong.
//
// A quad solver can always find SOME set of four stars whose shape matches a catalogue quad; on a
// star-poor panel searched deeply, the detections it has to choose from are mostly noise, and the
// answer it returns is a coincidence rather than the field. That is strictly worse than failing —
// a panel left out is missing, a panel in the wrong place corrupts the canvas around it.
//
// Measured on the low panel of a real session: the deep retry returned 90 matched stars where every
// other panel matched 541 to 923, put the field 45 degrees from the two panels it was shot BETWEEN,
// and pointed 90 degrees from the recorded bearing. Two independent checks catch it.
//
// The bearing check has to allow for the compass being wrong, because on this hardware it often is —
// but it is wrong in a PARTICULAR way. Measured across a full session, the errors are either near
// zero or near 180 degrees (three panels solved to 215/215/224 against a recorded 35/35/44). Nothing
// legitimate lands near 90. So the test is not "does the bearing agree" but "is the disagreement one
// of the two the hardware actually makes".
func plausibleSolve(sol skypano.Solution, solvedAz, recordedAz float64) bool {
	if sol.Matches < panoMinSolveMatches {
		return false
	}
	if recordedAz == 0 {
		return true // no bearing recorded; the match count is all there is
	}
	d := angleGapDeg(solvedAz, recordedAz)
	return d <= panoAzTolDeg || math.Abs(d-180) <= panoAzTolDeg
}

// angleGapDeg is the separation of two bearings, in [0, 180].
func angleGapDeg(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 360)
	if d > 180 {
		d = 360 - d
	}
	return d
}
