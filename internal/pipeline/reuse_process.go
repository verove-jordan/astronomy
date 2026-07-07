package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/photom"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/skycat"
	"github.com/verove-jordan/astronomy/internal/store"
)

// processChannelGroups integrates several calibration groups (this session + prior sessions) into one
// channel master. Each group is calibrated with its own masters — crucially, its own session's flat —
// then every calibrated frame is co-registered and stacked together, growing the total integration.
func processChannelGroups(ctx context.Context, opts Options, object, filter string, groups []lightGroup,
	masters []calib.Master, flats *flatCache, parity *parityCache, workRun, outDir string, gradeOpts grade.Options,
	onProgress func(siril.Progress)) ChannelResult {
	rep := groups[0]
	ch := ChannelResult{
		Object:     object,
		Filter:     filter,
		ExposureMs: rep.Key.ExposureMs,
		Selection:  calib.MatchForLightExcluding(rep.Key, masters, opts.CalibExclude), // representative selection (notes/UI)
	}

	var calibrated []string         // calibrated frame paths, in registration order
	var frames []*inspect.Frame     // matching frame metadata, same order, for grading
	var photomGroups []photom.Group // per-group calibrated frames, for photometric normalization
	for gi, g := range groups {
		cm, notes := flats.mastersFor(ctx, opts, g, masters, workRun)
		ch.Selection.Notes = append(ch.Selection.Notes, notes...)

		grpDir := filepath.Join(workRun, fmt.Sprintf("light_%s_g%d", sanitize(filter), gi))
		if _, err := fsutil.LinkFrames(grpDir, framePaths(g.Frames)); err != nil {
			ch.Err = err.Error()
			return ch
		}
		if _, err := opts.Runner.Run(ctx, grpDir, siril.CalibrateOnlyScript("light", cm), onProgress); err != nil {
			ch.Err = err.Error()
			return ch
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
		if note := parity.correct(ctx, g, grpDir, base, len(g.Frames)); note != "" {
			ch.Selection.Notes = append(ch.Selection.Notes, note)
		}
		calibrated = append(calibrated, gp...)
		frames = append(frames, g.Frames...)
		photomGroups = append(photomGroups, buildPhotomGroup(g, gp))
		ch.InputFrames += len(g.Frames)
	}

	// Photometric normalization: sessions shot at different exposure/gain/temperature have different
	// linear scales, which Siril's addscale only partly absorbs. Measure each group's curve and map it
	// onto the reference group's scale/offset (in place) before the merge, so a mixed-settings stack is
	// clean. Single-session (one group) → identity → skipped. Soft-fail: notes only, never abort.
	if opts.Preset != nil && opts.Preset.PhotomNorm && len(photomGroups) > 1 {
		markReferenceGroup(photomGroups, groups)
		records, notes := photom.NormalizeGroups(ctx, photomGroups)
		ch.Photom = records
		ch.Selection.Notes = append(ch.Selection.Notes, notes...)
	}

	// Co-register all calibrated frames into one sequence (no further calibration), then grade+stack.
	mergedDir := filepath.Join(workRun, "merged_"+sanitize(filter))
	if _, err := fsutil.LinkFrames(mergedDir, calibrated); err != nil {
		ch.Err = err.Error()
		return ch
	}
	// Register with field rotation (homography, Siril's default) and crop the output to the common
	// field-of-view (framing=min) so two differently-oriented sessions integrate over their shared sky
	// with no ragged low-coverage borders. Stack weighted by wFWHM so the sharper/deeper subs dominate.
	noMasters := siril.CalibMasters{}
	if _, err := opts.Runner.Run(ctx, mergedDir, siril.CalibrateRegisterFramedScript("light", noMasters, "homography", "min"), onProgress); err != nil {
		ch.Err = err.Error()
		return ch
	}
	finishStackedChannel(ctx, opts, mergedDir, siril.CalibratedSeq("light", noMasters),
		siril.RegisteredSeq("light", noMasters), filter, frames, outDir, gradeOpts, "wfwhm", onProgress, &ch)
	return ch
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

// flatCache builds and memoizes one master flat per (session, filter) from a prior session's own raw
// flats, so reused light frames are flat-fielded with the optical train that actually produced them.
type flatCache struct {
	provider ReuseProvider
	built    map[string]string // "sessionID|filter" → flat master path ("" = none/failed)
}

func newFlatCache(p ReuseProvider) *flatCache {
	return &flatCache{provider: p, built: map[string]string{}}
}

// mastersFor returns the calibration masters for a group. Darks/bias come from the shared deep pool;
// the flat is this run's flat for the current session, or the group's own session flat for prior data.
func (c *flatCache) mastersFor(ctx context.Context, opts Options, g lightGroup,
	masters []calib.Master, workRun string) (siril.CalibMasters, []string) {
	sel := calib.MatchForLightExcluding(g.Key, masters, opts.CalibExclude)
	dark, flat, bias := sel.Masters()
	if g.Current {
		return siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias, DarkOptimize: sel.DarkOptimize}, nil
	}
	// Prior session: replace the (possibly wrong-session) flat with this session's own.
	sessionFlat, note := c.sessionFlat(ctx, opts, g, bias, workRun)
	var notes []string
	if note != "" {
		notes = append(notes, note)
	}
	return siril.CalibMasters{Dark: dark, Flat: sessionFlat, Bias: bias, DarkOptimize: sel.DarkOptimize}, notes
}

// sessionFlat builds (once) a master flat from a prior session's raw flats for the group's filter.
func (c *flatCache) sessionFlat(ctx context.Context, opts Options, g lightGroup, biasPath, workRun string) (string, string) {
	if c.provider == nil {
		return "", ""
	}
	key := fmt.Sprintf("%d|%s", g.SessionID, g.Filter)
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
	var paths []string
	missing := 0
	for _, r := range rows {
		if r.Filter != g.Filter {
			continue
		}
		if !fileExists(r.Path) { // freed to S3 — one ghost symlink would sink the whole flat stack
			missing++
			continue
		}
		paths = append(paths, r.Path)
	}
	if len(paths) == 0 {
		if missing > 0 {
			return "", fmt.Sprintf("session %d: %d raw flat(s) missing on disk (freed to S3?) — flat correction skipped for its frames", g.SessionID, missing)
		}
		return "", fmt.Sprintf("session %d: no flats for filter %q — flat correction skipped for its frames", g.SessionID, g.Filter)
	}

	name := fmt.Sprintf("flat_s%d_%s", g.SessionID, sanitize(g.Filter))
	outBase := filepath.Join(workRun, "session_flats", name)
	buildDir := filepath.Join(workRun, "session_flats", "build_"+name)
	if _, err := fsutil.LinkFrames(buildDir, paths); err != nil {
		return "", fmt.Sprintf("session %d: %v", g.SessionID, err)
	}
	if _, err := opts.Runner.Run(ctx, buildDir, siril.StackFlatScript("flat", outBase, biasPath), nil); err != nil {
		return "", fmt.Sprintf("session %d: build flat failed: %v", g.SessionID, err)
	}
	c.built[key] = outBase + ".fits"
	return c.built[key], ""
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
	seen   map[string]int // parityKey → sign (-1/+1), or 0 when parity could not be determined
}

func newParityCache(runner *siril.Runner, solve siril.SolveOptions) *parityCache {
	return &parityCache{runner: runner, solve: solve, seen: map[string]int{}}
}

// parityKey identifies a physical session. Parity is a property of the optical train, so filter and
// exposure are excluded (a session solves once for all its channels); camera config is included so the
// combined-folder case — both sessions sharing one SessionID but differing by gain — stays separable.
func parityKey(g lightGroup) string {
	return fmt.Sprintf("%d|%d|%d|%d|%d", g.SessionID, g.Key.Gain, g.Key.Offset, g.Key.Bin, g.Key.TempBucket)
}

// correct flips group g's calibrated frames (named base_00001…base_0000n in grpDir) when its parity
// differs from the target, so it can co-register with the other sessions. The flip is then VERIFIED by
// re-probing before it is trusted. It returns a human-facing note when it flips a session, reverts an
// unverified flip, or cannot determine parity, and "" when nothing was needed.
func (pc *parityCache) correct(ctx context.Context, g lightGroup, grpDir, base string, n int) string {
	sign, note := pc.signFor(ctx, g, grpDir, base)
	if sign == 0 || sign == parityTargetSign {
		return note // undetermined (warned) or already aligned (no-op)
	}
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s_%05d", base, i+1)
	}
	if _, err := pc.runner.Run(ctx, grpDir, siril.MirrorFramesScript(names), nil); err != nil {
		return fmt.Sprintf("session %d: parity flip failed (%v) — frames left unmirrored", g.SessionID, err)
	}
	return pc.verifyFlip(ctx, g, grpDir, names)
}

// verifyFlip re-probes the first frame after a mirror flip: measurement and correction must agree
// before opposite-parity frames enter the merge. A probe that STILL reads mirrored means parity cannot
// be trusted for these files (a header quirk steering Siril's load, a solver fluke) — the flip is
// undone, the session cached as undetermined so its sibling groups are left alone, and a warning
// returned. An undetermined re-probe keeps the flip (the original reading stands), with a note.
func (pc *parityCache) verifyFlip(ctx context.Context, g lightGroup, grpDir string, names []string) string {
	sign, _ := probeImageParity(ctx, pc.runner, pc.solve, grpDir, names[0])
	switch sign {
	case parityTargetSign:
		return fmt.Sprintf("session %d: mirror-corrected (parity flip, verified) so it stacks with the other sessions", g.SessionID)
	case 0:
		return fmt.Sprintf("session %d: mirror-corrected (parity flip; verification probe undetermined)", g.SessionID)
	}
	pc.seen[parityKey(g)] = 0 // this session's parity readings are unreliable — don't flip its other groups
	if _, err := pc.runner.Run(ctx, grpDir, siril.MirrorFramesScript(names), nil); err != nil {
		return fmt.Sprintf("session %d: parity flip did not verify AND undo failed (%v) — frames may be mirrored", g.SessionID, err)
	}
	return fmt.Sprintf("session %d: parity flip did not verify (probe still reads mirrored) — frames left as captured", g.SessionID)
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
