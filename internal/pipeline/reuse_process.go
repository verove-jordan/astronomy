package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/filters"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/photom"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/skycat"
	"github.com/verove-jordan/astronomy/internal/store"
)

// processChannelGroups integrates several calibration groups (this session + prior sessions) into one
// channel master. Each group is calibrated with its own masters — crucially, its own night's flat —
// then every calibrated frame is co-registered onto the anchor night's canvas and stacked together,
// growing the total integration. anchorNight names the run-wide anchor (ReusePlan.AnchorNight) so all
// channels share one master geometry. ref pins the channel's step slot so the per-session sub-steps
// (calibrate / normalize / stack) are attributed to their capture night in the live journal without
// touching the bar.
func processChannelGroups(ctx context.Context, opts Options, object, filter string, groups []lightGroup,
	masters []calib.Master, flats *flatCache, parity *parityCache, workRun, outDir string, gradeOpts grade.Options,
	onProgress func(siril.Progress), ref stepRef, anchorNight string) ChannelResult {
	rep := groups[0]
	ch := ChannelResult{
		Object:     object,
		Filter:     filter,
		ExposureMs: rep.Key.ExposureMs,
		Selection:  calib.MatchForLightExcluding(rep.Key, masters, opts.CalibExclude, opts.ForceCalibration), // representative selection (notes/UI)
	}

	var calibrated []string         // calibrated frame paths, in registration order
	var spans []groupSpan           // each group's [start,end) range inside that order
	var frames []*inspect.Frame     // matching frame metadata, same order, for grading
	var photomGroups []photom.Group // per-group calibrated frames, for photometric normalization
	var midFrames []string          // one representative calibrated frame per group (previews)
	for gi, g := range groups {
		glabel := groupSessionLabel(g)
		gstart := time.Now()
		opts.sessionLine(ref, g.Session, fmt.Sprintf("▶ %s · %s — calibrate (%d frames)", glabel, filter, len(g.Frames)))
		cm, flatSrc, notes := flats.mastersFor(ctx, opts, g, masters, workRun)
		ch.Selection.Notes = append(ch.Selection.Notes, notes...)
		gr := groupResultFor(g, filter, cm, flatSrc)
		// Pull from the S3 library mirror if absent locally, then resolve the dark's defect sidecar —
		// after the pull, so a mirrored map is found (missing map = soft -cc=dark fallback).
		opts.ensureMasters(ctx, []string{cm.Dark, cm.Flat, cm.Bias, calib.DefectsListPath(cm.Dark)})
		cm.BadPixelMap = calib.DefectsListFor(cm.Dark)

		grpDir := filepath.Join(workRun, fmt.Sprintf("light_%s_g%d", sanitize(filter), gi))
		if _, err := fsutil.LinkFrames(grpDir, framePaths(g.Frames)); err != nil {
			warnChannel(opts, &ch, fmt.Sprintf("%s · %s: group failed, its %d frame(s) skipped — %v",
				glabel, filter, len(g.Frames), err))
			continue
		}
		// A one-frame group (a night that contributed a single sub of this filter, task #352) is
		// converted by link but gets no .seq — Siril has no one-image sequences — so the sequence
		// calibrate would abort the whole channel; calibrate_single writes the identical pp_ output.
		ingest := seqIngest(g.Frames) // camera raws / colour stills must be decoded, not symlinked
		calScript := siril.CalibrateOnlyScriptWith("light", cm, ingest)
		if len(g.Frames) == 1 {
			calScript = siril.CalibrateSingleScriptWith("light", cm, ingest)
		}
		if _, err := opts.Runner.Run(ctx, grpDir, calScript, onProgress); err != nil {
			if ctx.Err() != nil { // a cancelled run must abort, not "skip" its way through every group
				ch.Err = err.Error()
				return ch
			}
			// One corrupt group (e.g. leftovers that slipped in as lights) must cost only its own
			// frames, never the channel — the remaining groups still merge (M33: a 4-file junk
			// group aborted L with 125 healthy subs behind it).
			warnChannel(opts, &ch, fmt.Sprintf("%s · %s: group failed, its %d frame(s) skipped — %v",
				glabel, filter, len(g.Frames), err))
			continue
		}
		base := siril.CalibratedSeq("light", cm) // "pp_light" with masters, else "light"
		gp := calibratedFramePaths(grpDir, base, len(g.Frames))
		// ASICAP mono captures stamp a spurious BAYERPAT card that Siril propagates onto these calibrated
		// copies — where it can derail the parity plate-solve and the merged registration (the frames are
		// treated as un-debayered CFA). Strip it BEFORE probing; the hardlinked originals stay untouched.
		if note := stripBayerPattern(gp); note != "" {
			ch.Selection.Notes = append(ch.Selection.Notes, note)
		}
		// Correct a mirror/parity flip (e.g. a session shot through a star diagonal) before the merge:
		// a mirrored session can never be aligned by rotation, so its calibrated frames are flipped here —
		// and the flip is verified by re-probing before it is trusted (see parityCache.correct).
		note, flipped := parity.correct(ctx, g, grpDir, base, len(g.Frames))
		if note != "" {
			ch.Selection.Notes = append(ch.Selection.Notes, note)
		}
		gr.ParityFlipped = flipped
		// One representative (middle, post-parity) calibrated frame per group: the "before
		// normalization" milestone, and the SAME frame is re-rendered after the transform so the
		// pair compares directly. Skipped on zero-ref paths (re-stack) — their index namespace
		// would collide across channels.
		mid := gp[len(gp)/2]
		midFrames = append(midFrames, mid)
		if ref.Index > 0 {
			gr.PrenormPreview = captureSessionPreview(ctx, opts, outDir,
				sessionPreviewIndex(ref.Index, gi, false), stagePrenorm, filter, g.Session, mid, true)
		}
		spans = append(spans, groupSpan{Start: len(calibrated), End: len(calibrated) + len(gp)})
		calibrated = append(calibrated, gp...)
		frames = append(frames, g.Frames...)
		photomGroups = append(photomGroups, buildPhotomGroup(g, gp))
		ch.InputFrames += len(g.Frames)
		ch.Groups = append(ch.Groups, gr)
		opts.sessionLine(ref, g.Session, fmt.Sprintf("✓ %s · %s calibrated in %s", glabel, filter, time.Since(gstart).Round(time.Second)))
	}

	// Every group failed (all skipped above) — now the channel is genuinely dead.
	if len(calibrated) == 0 {
		ch.Err = fmt.Sprintf("%s: every calibration group failed — see the group warnings", filter)
		return ch
	}

	// A channel whose ENTIRE integration is one calibrated frame (a single one-frame group) cannot
	// be registered or stacked either — promote that frame to the channel master with the shared
	// linear finishing, so downstream channel alignment and compose see a normal master.
	if len(calibrated) == 1 {
		warnChannel(opts, &ch, filter+": only 1 calibrated frame in total — using it as the channel master (no registration/stacking)")
		promoteLoneCalibrated(ctx, opts, &ch, calibrated[0], filter, outDir, onProgress)
		return ch
	}

	// Photometric normalization: sessions shot at different exposure/gain/temperature have different
	// linear scales, which Siril's addscale only partly absorbs. Measure each group's curve and map it
	// onto the reference group's scale/offset (in place) before the merge, so a mixed-settings stack is
	// clean. Single-session (one group) → identity → skipped. Soft-fail: notes only, never abort.
	if opts.Preset != nil && opts.Preset.PhotomNorm && len(photomGroups) > 1 {
		markReferenceGroup(photomGroups, groups, anchorNight)
		opts.sessionLine(ref, "", fmt.Sprintf("▶ normalize %s (%d groups → shared photometric scale)", filter, len(photomGroups)))
		records, notes := photom.NormalizeGroups(ctx, photomGroups)
		ch.Photom = records
		ch.Selection.Notes = append(ch.Selection.Notes, notes...)
		for i := range records {
			rec := records[i]
			opts.sessionPhotom(ref, rec.Session, rec)
			opts.sessionLine(ref, rec.Session, photomLine(rec))
			if i >= len(ch.Groups) {
				continue // defensive: records mirror photomGroups (one per group) by construction
			}
			ch.Groups[i].Photom = &records[i]
			// The after-normalization render of the SAME representative frame — only when the group's
			// pixels were actually rewritten (a reference/identity group would duplicate its prenorm).
			if rec.Applied && ref.Index > 0 {
				ch.Groups[i].NormalizedPreview = captureSessionPreview(ctx, opts, outDir,
					sessionPreviewIndex(ref.Index, i, true), stageNormalized, filter, groups[i].Session, midFrames[i], true)
			}
		}
		opts.sessionLine(ref, "", fmt.Sprintf("✓ normalize %s done", filter))
	}

	// Co-register all calibrated frames into one sequence (no further calibration), then grade+stack.
	opts.sessionLine(ref, "", fmt.Sprintf("▶ register + stack %s (%d frames from %d groups)", filter, len(calibrated), len(groups)))
	mergedDir := filepath.Join(workRun, "merged_"+sanitize(filter))
	if _, err := fsutil.LinkFrames(mergedDir, calibrated); err != nil {
		ch.Err = err.Error()
		return ch
	}
	review, seqDir, seqBase, err := registerMergedGroups(ctx, opts, &ch, groups, spans, anchorNight, mergedDir, filter, calibrated, onProgress, ref)
	if err != nil {
		ch.Err = err.Error()
		return ch
	}
	noMasters := siril.CalibMasters{}
	stackWeight, weightNote := photomStackWeight(opts.stackWeight(), ch.Photom)
	if weightNote != "" {
		warnChannel(opts, &ch, filter+": "+weightNote)
	}
	geom := &regGeometry{FrameH: review.FrameH}
	if ch.Canvas != nil {
		fw, fh := frameDims(calibrated[0])
		geom.ContentW, geom.ContentH = fw, fh
		geom.Canvas = canvasSpec{W: ch.Canvas.W, H: ch.Canvas.H,
			OffX: float64(ch.Canvas.OffX), OffY: float64(ch.Canvas.OffY)}
	}
	finishStackedChannel(ctx, opts, seqDir, seqBase,
		siril.RegisteredSeq(seqBase, noMasters), filter, frames, outDir, gradeOpts, stackWeight,
		onProgress, &ch, review.PreReject, gradeSpans(spans), geom)
	recordGroupContributions(opts, &ch, spans, filter)
	recordChannelCoverage(ctx, opts, &ch, review, calibrated, outDir, filter)
	return ch
}

// recordChannelCoverage rasterizes where this channel's STACKED frames actually cover the anchor
// canvas (from the review's kept homographies + the final grading outcome) — the input of the
// coverage-aware combine crop, plus the covered_frac/mask-PNG record for run.json and the UI.
func recordChannelCoverage(ctx context.Context, opts Options, ch *ChannelResult, review registrationReview,
	calibrated []string, outDir, filter string) {
	if len(review.FrameH) == 0 || ch.Err != "" {
		return
	}
	grid := ch.coverage // finishStackedChannel already rasterized it for the seam noise equalization
	if grid == nil {
		rejected := func(i int) bool {
			return i >= len(ch.Metrics) || ch.Metrics[i].Rejected || ch.Metrics[i].FWHM <= 0
		}
		w, h := frameDims(calibrated[0])
		if w <= 0 || h <= 0 {
			return
		}
		grid = rasterizeCoverage(review.FrameH, rejected, w, h)
	}
	if grid.Frames == 0 {
		return
	}
	ch.coverage = grid
	minFrac := 0.0
	if opts.Preset != nil {
		minFrac = opts.Preset.CoverageMinFrac
	}
	ch.CoveredFrac = coveredFrac(grid.mask(minFrac))
	maskPath := filepath.Join(outDir, "coverage_"+filterTag(filter)+".png")
	if err := writeCoverageMaskPNG(grid, maskPath); err == nil {
		ch.CoverageMask = maskPath
		// Mosaic runs surface the coverage map as a timeline card (where does each night's data
		// reach?) — gated on the knob so existing runs' preview sets stay byte-identical.
		if opts.Preset != nil && opts.Preset.Mosaic {
			captureSessionPreview(ctx, opts, outDir, ordCoverage+coverageSlot(filter), stageCoverage, filter, "", maskPath, false)
		}
	}
}

// coverageSlot orders the per-channel coverage cards deterministically on the timeline strip.
func coverageSlot(filter string) int {
	if r := filters.Rank(filter); r < len(filters.Canonical) {
		return r
	}
	return len(filters.Canonical) + len(filter)%10
}

// gradeSpans converts the merged order's group spans for the per-night grading scope.
func gradeSpans(spans []groupSpan) []grade.Span {
	out := make([]grade.Span, len(spans))
	for i, sp := range spans {
		out[i] = grade.Span{Start: sp.Start, End: sp.End}
	}
	return out
}

// recordGroupContributions folds the per-frame grading outcome back onto each group (night):
// stacked/rejected counts in run.json, plus a live warning when a night contributed NOTHING — the
// honest signal that its data never reached the master (task #354: whole nights silently evicted).
func recordGroupContributions(opts Options, ch *ChannelResult, spans []groupSpan, filter string) {
	for gi, sp := range spans {
		if gi >= len(ch.Groups) {
			return
		}
		stacked, rejected := 0, 0
		for i := sp.Start; i < sp.End && i < len(ch.Metrics); i++ {
			if ch.Metrics[i].Rejected {
				rejected++
			} else if ch.Metrics[i].FWHM > 0 {
				stacked++
			}
		}
		ch.Groups[gi].StackedFrames, ch.Groups[gi].RejectedFrames = stacked, rejected
		if stacked == 0 && sp.End > sp.Start {
			night := ch.Groups[gi].Session
			if night == "" {
				night = "(undated)"
			}
			warnChannel(opts, ch, fmt.Sprintf("%s: night %s contributed no frames to the stack (%d rejected)",
				filter, night, rejected))
		}
	}
}

// registerMergedGroups registers the merged sequence in two passes with field rotation
// (homography), reviews the computed transforms in Go, then applies them on the ANCHOR group's
// reference-frame canvas (setref + framing=current): every channel master lands on the anchor
// night's full field no matter how the other nights were framed. The old framing=min instead
// intersected EVERY registered footprint — one drifted night or a single false star match
// collapsed the master to a sliver that even dropped the target (task #312). The review also
// excludes physically absurd transforms from the stack and surfaces each night's measured field
// rotation/overlap — the live evidence that the cross-night star match worked.
func registerMergedGroups(ctx context.Context, opts Options, ch *ChannelResult, groups []lightGroup,
	spans []groupSpan, anchorNight, mergedDir, filter string, calibrated []string,
	onProgress func(siril.Progress), ref stepRef) (registrationReview, string, string, error) {
	// Multi-night merges first flatten each frame's own sky gradient (per-frame degree-1 seqsubsky;
	// mean level preserved): the nights' gradients lie at their own field rotations, so they cannot
	// cancel in the stack and would step at footprint boundaries (task #354's seams). Soft-fail —
	// a flatten error falls back to registering the original frames.
	seqBase := "light"
	if opts.Preset != nil && opts.Preset.FlattenBg && len(groups) > 1 {
		if _, err := opts.Runner.Run(ctx, mergedDir, siril.FlattenRegister2PassScript("light", "homography", 1), onProgress); err != nil {
			warnChannel(opts, ch, filter+": per-frame background flatten failed — registering unflattened")
		} else {
			seqBase = siril.FlattenedSeq("light")
		}
	}
	if seqBase == "light" {
		if _, err := opts.Runner.Run(ctx, mergedDir, siril.Register2PassScript("light", "homography"), onProgress); err != nil {
			return registrationReview{}, "", "", err
		}
	}
	w, h := frameDims(calibrated[0])
	review := reviewMergedRegistration(filepath.Join(mergedDir, seqBase+"_.seq"), spans, anchorGroupIndex(groups, anchorNight), w, h)
	for _, warn := range review.Warnings {
		warnChannel(opts, ch, filter+": "+warn)
	}
	for gi := range groups {
		if gi >= len(review.Groups) || gi >= len(ch.Groups) {
			break
		}
		gr := review.Groups[gi]
		if len(groups) > 1 { // a lone group is trivially self-anchored — telemetry would be noise
			ch.Groups[gi].RotationDeg, ch.Groups[gi].OverlapFrac = gr.RotationDeg, gr.OverlapFrac
			if gr.Registered > 0 {
				opts.sessionLine(ref, groups[gi].Session, registrationLine(filter, gr))
			}
		}
	}
	// The homographies are known but no registered pixel is written yet — the seam offset refit
	// can still correct each night's frames in place before seqapplyreg reads them.
	if opts.Preset != nil && opts.Preset.SeamOffsetRefit && len(groups) > 1 {
		refitGroupOffsets(ctx, opts, ch, groups, spans, anchorGroupIndex(groups, anchorNight),
			review, mergedDir, seqBase, filter, w, h, ref)
	}
	// Mosaic: rebuild the merge on the union canvas (pad + re-register) so no night's pixels are
	// cropped by the anchor frame; soft-fails back to the anchor canvas.
	seqDir := mergedDir
	if opts.Preset != nil && opts.Preset.Mosaic && len(groups) > 1 {
		if padDir, padSeq, review2, cv, ok := mosaicReregister(ctx, opts, ch, groups, spans,
			anchorNight, mergedDir, seqBase, filter, review, w, h, ref); ok {
			seqDir, seqBase, review = padDir, padSeq, review2
			ch.Canvas = &CanvasInfo{W: cv.W, H: cv.H, OffX: int(cv.OffX), OffY: int(cv.OffY),
				AnchorW: w, AnchorH: h}
		}
	}
	if _, err := opts.Runner.Run(ctx, seqDir, siril.ApplyRegistrationScript(seqBase, review.RefIndex, "current"), onProgress); err != nil {
		return registrationReview{}, "", "", err
	}
	if ch.Canvas != nil && review.RefIndex > 0 {
		if rw, rh := frameDims(filepath.Join(seqDir, fmt.Sprintf("r_%s_%05d.fits", seqBase, review.RefIndex))); rw > 0 &&
			(rw != ch.Canvas.W || rh != ch.Canvas.H) {
			warnChannel(opts, ch, fmt.Sprintf("%s: mosaic canvas prediction %dx%d vs Siril %dx%d — coverage may be misaligned",
				filter, ch.Canvas.W, ch.Canvas.H, rw, rh))
			ch.Canvas.W, ch.Canvas.H = rw, rh
		}
	}
	return review, seqDir, seqBase, nil
}

// GroupResult is one calibration group's provenance inside a cross-session channel merge — the
// run.json record behind the per-night rows of the completed-run UI.
type GroupResult struct {
	SessionID   int64  `json:"session_id"` // catalog session; 0 = this run's capture
	Current     bool   `json:"current,omitempty"`
	Session     string `json:"session,omitempty"` // capture-night key ("" = undated)
	Filter      string `json:"filter"`
	ExposureMs  int64  `json:"exposure_ms"`
	Gain        int64  `json:"gain"`
	Offset      int64  `json:"offset"`
	TempBucketC int    `json:"temp_bucket_c"`
	Bin         int    `json:"bin"`
	Frames      int    `json:"frames"`
	// Masters actually applied (basenames) + the flat's provenance (run / session-rebuild / none).
	Dark       string `json:"dark,omitempty"`
	Flat       string `json:"flat,omitempty"`
	Bias       string `json:"bias,omitempty"`
	FlatSource string `json:"flat_source,omitempty"`
	// ParityFlipped: the group's frames were mirror-flipped to match the merge's parity convention.
	ParityFlipped bool `json:"parity_flipped,omitempty"`
	// RotationDeg/OverlapFrac: the group's median field rotation and footprint overlap vs the anchor
	// canvas, measured from the merged registration's homographies (multi-group channels only).
	RotationDeg *float64 `json:"rotation_deg,omitempty"`
	OverlapFrac *float64 `json:"overlap_frac,omitempty"`
	// Photom is this group's normalization record (also aggregated in ChannelResult.Photom).
	Photom *photom.GroupRecord `json:"photom,omitempty"`
	// StackedFrames/RejectedFrames: how many of this night's frames actually reached the channel
	// stack vs were rejected (registration + grading) — the per-night contribution ledger.
	StackedFrames  int `json:"stacked_frames"`
	RejectedFrames int `json:"rejected_frames,omitempty"`
	// Before/after-normalization preview PNGs (present when Preset.Previews is on).
	PrenormPreview    string `json:"prenorm_preview,omitempty"`
	NormalizedPreview string `json:"normalized_preview,omitempty"`
}

// groupResultFor seeds one group's provenance record from its identity + applied masters.
func groupResultFor(g lightGroup, filter string, cm siril.CalibMasters, flatSource string) GroupResult {
	return GroupResult{
		SessionID: g.SessionID, Current: g.Current, Session: g.Session,
		Filter: filter, ExposureMs: g.Key.ExposureMs, Gain: g.Key.Gain, Offset: g.Key.Offset,
		TempBucketC: g.Key.TempBucket, Bin: g.Key.Bin, Frames: len(g.Frames),
		Dark: baseOrEmpty(cm.Dark), Flat: baseOrEmpty(cm.Flat), Bias: baseOrEmpty(cm.Bias),
		FlatSource: flatSource,
	}
}

// baseOrEmpty is filepath.Base that keeps "" empty (Base("") is ".").
func baseOrEmpty(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}

// groupSessionLabel names a group for the journal's per-session lines: its capture night, else its
// catalog session, else the current capture.
func groupSessionLabel(g lightGroup) string {
	if g.Session != "" {
		return g.Session
	}
	if g.Current {
		return "current session"
	}
	return fmt.Sprintf("session %d", g.SessionID)
}

// photomLine renders one group's normalization record for the journal.
func photomLine(rec photom.GroupRecord) string {
	state := "not applied"
	switch {
	case rec.Ref:
		state = "reference"
	case rec.Applied:
		state = "applied"
	}
	extra := ""
	switch {
	case rec.Method == photom.MethodBgMatched:
		extra += ", scale from sky backgrounds"
	case rec.Method == photom.MethodOffsetOnly:
		extra += ", background offset only"
	case rec.Method == photom.MethodSeeded || rec.MetaSeeded: // MetaSeeded: records persisted before Method existed
		extra += ", scale from headers"
	}
	if rec.Clamped {
		extra += ", clamped"
	}
	if rec.Reverted {
		extra += ", degraded (would clip)"
	}
	return fmt.Sprintf("  · %s: ×%.3g %+.4g (%s%s)", rec.Label, rec.Scale, rec.Offset, state, extra)
}

// calibratedFramePaths returns the deterministic Siril output paths for a calibrated sequence
// (base_00001.fits … base_n.fits), which calibrate produces 1:1 with the linked inputs.
func calibratedFramePaths(dir, base string, n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, filepath.Join(dir, fmt.Sprintf("%s_%05d.fits", base, i)))
	}
	return out
}

// flatCache builds and memoizes one master flat per (session, filter, night) from a prior session's
// own raw flats, so reused light frames are flat-fielded with the optical train that actually produced
// them. The mutex makes it safe under parallel channel waves; keys are per-filter so waves never contend.
type flatCache struct {
	mu       sync.Mutex
	provider ReuseProvider
	built    map[string]string // "sessionID|filter|night" → flat master path ("" = none/failed)
}

func newFlatCache(p ReuseProvider) *flatCache {
	return &flatCache{provider: p, built: map[string]string{}}
}

// Flat provenance values recorded per group in run.json (GroupResult.FlatSource).
const (
	flatSourceRun     = "run"             // the run's own matched master (library or capture-built)
	flatSourceSession = "session-rebuild" // rebuilt from the prior session's own raw flats
	flatSourceNone    = "none"            // no usable flat — flat correction skipped
)

// mastersFor returns the calibration masters for a group plus the flat's provenance. Darks/bias come
// from the shared deep pool; the flat is this run's flat for the current session, or the group's own
// session (night) flat for prior data.
func (c *flatCache) mastersFor(ctx context.Context, opts Options, g lightGroup,
	masters []calib.Master, workRun string) (siril.CalibMasters, string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Masters shot on another sensor leave the pool before the match: Siril accepts a wrong-sized
	// master, skips that correction and still reports success, so the run would finish "calibrated"
	// with untouched lights. Filtering the pool (not the result) keeps the fallback. See calib/dims.go.
	light := firstFramePath(g.Frames)
	usable, dimNote := calib.KeepMatchingDims(masters, light)
	sel := calib.MatchForLightExcluding(g.Key, usable, opts.CalibExclude, opts.ForceCalibration)
	var dimNotes []string
	if dimNote != "" {
		dimNotes = append(dimNotes, dimNote)
	}
	dark, flat, bias := sel.Masters()
	// One-shot-color lights still in their raw CFA mosaic calibrate CFA-aware and demosaic at the END
	// of calibration, never before it: debayering first interpolates every hot pixel and dust shadow
	// across its neighbours, so the defect map and the flat would be correcting a smeared copy of the
	// artifact rather than the artifact. See siril.calibrateArgs.
	cfa := needsDebayer(g.Frames)
	if g.Current {
		src := flatSourceRun
		if flat == "" {
			src = flatSourceNone
		}
		return siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias, DarkOptimize: sel.DarkOptimize, CFA: cfa}, src, dimNotes
	}
	// Prior session: replace the (possibly wrong-session) flat with this session's own.
	sessionFlat, note := c.sessionFlat(ctx, opts, g, bias, workRun)
	notes := dimNotes
	if note != "" {
		notes = append(notes, note)
	}
	// The replacement came from a DIFFERENT night's raw flats, which is also a different night's
	// camera whenever the rig changed — size-check it exactly like a library master.
	if sessionFlat != "" {
		if same, known := calib.SameDims(sessionFlat, light); known && !same {
			notes = append(notes, fmt.Sprintf(
				"session %d: its flat was shot on another sensor (different pixel dimensions) — flat correction skipped for its frames",
				g.SessionID))
			sessionFlat = ""
		}
	}
	src := flatSourceSession
	if sessionFlat == "" {
		src = flatSourceNone
	}
	return siril.CalibMasters{Dark: dark, Flat: sessionFlat, Bias: bias, DarkOptimize: sel.DarkOptimize, CFA: cfa}, src, notes
}

// sessionFlat builds (once) a master flat from a prior session's raw flats for the group's filter
// and capture night — falling back to the session's other-night flats (with a note) when the night
// shot none, since a wrong-night flat still corrects vignetting and shared dust.
func (c *flatCache) sessionFlat(ctx context.Context, opts Options, g lightGroup, biasPath, workRun string) (string, string) {
	if c.provider == nil {
		return "", ""
	}
	key := fmt.Sprintf("%d|%s|%s", g.SessionID, g.Filter, g.Session)
	if p, ok := c.built[key]; ok {
		return p, ""
	}
	c.built[key] = "" // memoize failure too, so a missing flat is not re-queried per exposure group

	rows, err := c.provider.RawCalibFrames(ctx, store.CalibQuery{
		Types: []string{string(inspect.Flat)}, Gain: g.Key.Gain, Offset: g.Key.Offset, Bin: g.Key.Bin, SessionID: g.SessionID,
	})
	if err != nil {
		return "", fmt.Sprintf("session %d: flat lookup failed: %v", g.SessionID, err)
	}
	paths, missing, note := sessionFlatPaths(rows, g.Filter, g.Session)
	if len(paths) == 0 {
		if missing > 0 {
			return "", fmt.Sprintf("session %d: %d raw flat(s) missing on disk (freed to S3?) — flat correction skipped for its frames", g.SessionID, missing)
		}
		return "", fmt.Sprintf("session %d: no flats for filter %q — flat correction skipped for its frames", g.SessionID, g.Filter)
	}
	if note != "" {
		note = fmt.Sprintf("session %d: %s", g.SessionID, note)
	}

	name := fmt.Sprintf("flat_s%d_%s%s", g.SessionID, sanitize(g.Filter), nightToken(g.Session))
	outBase := filepath.Join(workRun, "session_flats", name)
	buildDir := filepath.Join(workRun, "session_flats", "build_"+name)
	if _, err := fsutil.LinkFrames(buildDir, paths); err != nil {
		return "", fmt.Sprintf("session %d: %v", g.SessionID, err)
	}
	if _, err := opts.Runner.Run(ctx, buildDir, siril.StackFlatScript("flat", outBase, biasPath, len(paths), opts.masterStack(calib.MasterFlat)), nil); err != nil {
		return "", fmt.Sprintf("session %d: build flat failed: %v", g.SessionID, err)
	}
	c.built[key] = outBase + ".fits"
	return c.built[key], note
}

// sessionFlatPaths selects a prior session's on-disk raw flats for one filter and capture night —
// PURE (no I/O beyond the existence check), shared verbatim by the run (sessionFlat) and the pre-run
// plan preview so the two can never disagree. Exact-night flats win; when the night shot none, the
// session's other-night flats are returned with a warning note (vignetting/shared dust still
// corrected — better than skipping). missing counts catalogued flats gone from disk (freed to S3).
func sessionFlatPaths(rows []store.FrameRow, filter, night string) (paths []string, missing int, note string) {
	var sameNight, otherNight []string
	for _, r := range rows {
		if r.Filter != filter {
			continue
		}
		if !fileExists(r.Path) { // freed to S3 — one ghost symlink would sink the whole flat stack
			missing++
			continue
		}
		if night == "" || inspect.NightKey(r.DateObsMs) == night {
			sameNight = append(sameNight, r.Path)
		} else {
			otherNight = append(otherNight, r.Path)
		}
	}
	if len(sameNight) > 0 {
		return sameNight, missing, ""
	}
	if len(otherNight) > 0 {
		return otherNight, missing, fmt.Sprintf(
			"no %s flats for night %s — using the session's other-night flats (dust may have moved)", filter, night)
	}
	return nil, missing, ""
}

// parityTargetSign is the det(CD) sign every session is normalized to. Negative = the standard East-left
// orientation, which the primary rig (ASI1600MM + FC-100) produces natively — so normal data is never
// flipped, only a foreign mirror-flipped session is. Stacking is guaranteed either way: opposite-parity
// frames cannot co-register, so all groups must share one sign.
const parityTargetSign = -1

// parityCache detects and corrects mirror/parity flips across sessions. A session shot through a different
// optical train (e.g. a star diagonal) is mirror-flipped: star registration matches asterisms by chirality,
// so it can never be aligned by rotation and must be physically flipped first. parityCache plate-solves one
// calibrated frame per session to read its parity (the sign of det(CD)) and flips the frames of any session
// whose parity differs from parityTargetSign. It is shared across channels so each session solves once.
type parityCache struct {
	runner *siril.Runner
	solve  siril.SolveOptions
	// mu serializes parallel channel waves: two channels of one session share a parity key, so the
	// lock both protects the map and makes the second caller reuse the first's plate-solve.
	mu   sync.Mutex
	seen map[string]int // parityKey → sign (-1/+1), or 0 when parity could not be determined
}

func newParityCache(runner *siril.Runner, solve siril.SolveOptions) *parityCache {
	return &parityCache{runner: runner, solve: solve, seen: map[string]int{}}
}

// parityKey identifies a physical session. Parity is a property of the optical train, so filter and
// exposure are excluded (a session solves once for all its channels); camera config is included so the
// combined-folder case — both sessions sharing one SessionID but differing by gain — stays separable,
// and the capture night too (a star diagonal may have been used one night only).
func parityKey(g lightGroup) string {
	return fmt.Sprintf("%d|%d|%d|%d|%d|%s", g.SessionID, g.Key.Gain, g.Key.Offset, g.Key.Bin, g.Key.TempBucket, g.Session)
}

// correct flips group g's calibrated frames (named base_00001…base_0000n in grpDir) when its parity
// differs from the target, so it can co-register with the other sessions. The flip is then VERIFIED by
// re-probing before it is trusted. It returns a human-facing note when it flips a session, reverts an
// unverified flip, or cannot determine parity ("" when nothing was needed) — plus whether the group's
// frames ARE mirror-flipped on disk when it returns (the run.json per-group provenance flag).
func (pc *parityCache) correct(ctx context.Context, g lightGroup, grpDir, base string, n int) (string, bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	sign, note := pc.signFor(ctx, g, grpDir, base)
	if sign == 0 || sign == parityTargetSign {
		return note, false // undetermined (warned) or already aligned (no-op)
	}
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s_%05d", base, i+1)
	}
	if _, err := pc.runner.Run(ctx, grpDir, siril.MirrorFramesScript(names), nil); err != nil {
		return fmt.Sprintf("session %d: parity flip failed (%v) — frames left unmirrored", g.SessionID, err), false
	}
	return pc.verifyFlip(ctx, g, grpDir, names)
}

// verifyFlip re-probes the first frame after a mirror flip: measurement and correction must agree
// before opposite-parity frames enter the merge. A probe that STILL reads mirrored means parity cannot
// be trusted for these files (a header quirk steering Siril's load, a solver fluke) — the flip is
// undone, the session cached as undetermined so its sibling groups are left alone, and a warning
// returned. An undetermined re-probe keeps the flip (the original reading stands), with a note.
func (pc *parityCache) verifyFlip(ctx context.Context, g lightGroup, grpDir string, names []string) (string, bool) {
	sign, _ := probeImageParity(ctx, pc.runner, pc.solve, grpDir, names[0])
	switch sign {
	case parityTargetSign:
		return fmt.Sprintf("session %d: mirror-corrected (parity flip, verified) so it stacks with the other sessions", g.SessionID), true
	case 0:
		return fmt.Sprintf("session %d: mirror-corrected (parity flip; verification probe undetermined)", g.SessionID), true
	}
	pc.seen[parityKey(g)] = 0 // this session's parity readings are unreliable — don't flip its other groups
	if _, err := pc.runner.Run(ctx, grpDir, siril.MirrorFramesScript(names), nil); err != nil {
		return fmt.Sprintf("session %d: parity flip did not verify AND undo failed (%v) — frames may be mirrored", g.SessionID, err), true
	}
	return fmt.Sprintf("session %d: parity flip did not verify (probe still reads mirrored) — frames left as captured", g.SessionID), false
}

// signFor returns the cached parity sign for g's session, plate-solving one reference frame on first sight.
func (pc *parityCache) signFor(ctx context.Context, g lightGroup, grpDir, base string) (int, string) {
	key := parityKey(g)
	if s, ok := pc.seen[key]; ok {
		return s, ""
	}
	sign, warn := pc.solveParity(ctx, grpDir, base)
	pc.seen[key] = sign
	if warn != "" {
		return sign, fmt.Sprintf("session %d: %s", g.SessionID, warn)
	}
	return sign, ""
}

// solveParity plate-solves the first calibrated frame (without flipping it) and reads the parity from
// the WCS via the shared probe (parity.go). It returns 0 and a warning when solving or reading fails —
// the group is then left unflipped (no worse than before: a truly mirrored group simply fails to
// register and is graded out).
func (pc *parityCache) solveParity(ctx context.Context, grpDir, base string) (int, string) {
	return probeImageParity(ctx, pc.runner, pc.solve, grpDir, fmt.Sprintf("%s_%05d", base, 1))
}

// orderedPlanFilters returns the plan's channel filters in canonical order (L first).
func orderedPlanFilters(plan *ReusePlan) []string {
	present := map[string]string{}
	for f := range plan.byFilter {
		present[f] = f
	}
	return orderedFilters(present)
}

// targetQueryFor resolves the target used to find prior lights: coordinates from the first current
// light frame that has them, falling back to the Siril catalog by object name.
func targetQueryFor(inv *inspect.Inventory, object, catalogDir string) targetQuery {
	tq := targetQuery{Object: object}
	for _, set := range inv.SetsOfType(inspect.Light) {
		for _, fr := range set.Frames {
			ra, okRA := skycat.ParseRA(fr.ObjCtRA)
			dec, okDec := skycat.ParseDec(fr.ObjCtDec)
			if okRA && okDec {
				tq.RADeg, tq.DecDeg, tq.HasCoords = ra, dec, true
				return tq
			}
		}
	}
	if ra, dec, ok := skycat.ResolveCoords(object, catalogDir); ok {
		tq.RADeg, tq.DecDeg, tq.HasCoords = ra, dec, true
	}
	return tq
}
