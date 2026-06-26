package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/skycat"
	"github.com/verove-jordan/astronomy/internal/store"
)

// processChannelGroups integrates several calibration groups (this session + prior sessions) into one
// channel master. Each group is calibrated with its own masters — crucially, its own session's flat —
// then every calibrated frame is co-registered and stacked together, growing the total integration.
func processChannelGroups(ctx context.Context, opts Options, object, filter string, groups []lightGroup,
	masters []calib.Master, flats *flatCache, workRun, outDir string, gradeOpts grade.Options,
	onProgress func(siril.Progress)) ChannelResult {
	rep := groups[0]
	ch := ChannelResult{
		Object:     object,
		Filter:     filter,
		ExposureMs: rep.Key.ExposureMs,
		Selection:  calib.MatchForLight(rep.Key, masters), // representative selection (notes/UI)
	}

	var calibrated []string     // calibrated frame paths, in registration order
	var frames []*inspect.Frame // matching frame metadata, same order, for grading
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
		calibrated = append(calibrated, calibratedFramePaths(grpDir, base, len(g.Frames))...)
		frames = append(frames, g.Frames...)
		ch.InputFrames += len(g.Frames)
	}

	// Co-register all calibrated frames into one sequence (no further calibration), then grade+stack.
	mergedDir := filepath.Join(workRun, "merged_"+sanitize(filter))
	if _, err := fsutil.LinkFrames(mergedDir, calibrated); err != nil {
		ch.Err = err.Error()
		return ch
	}
	noMasters := siril.CalibMasters{}
	if _, err := opts.Runner.Run(ctx, mergedDir, siril.CalibrateRegisterScript("light", noMasters), onProgress); err != nil {
		ch.Err = err.Error()
		return ch
	}
	finishStackedChannel(ctx, opts, mergedDir, siril.CalibratedSeq("light", noMasters),
		siril.RegisteredSeq("light", noMasters), filter, frames, outDir, gradeOpts, onProgress, &ch)
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
	sel := calib.MatchForLight(g.Key, masters)
	dark, flat, bias := sel.Masters()
	if g.Current {
		return siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias}, nil
	}
	// Prior session: replace the (possibly wrong-session) flat with this session's own.
	sessionFlat, note := c.sessionFlat(ctx, opts, g, bias, workRun)
	var notes []string
	if note != "" {
		notes = append(notes, note)
	}
	return siril.CalibMasters{Dark: dark, Flat: sessionFlat, Bias: bias}, notes
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
	for _, r := range rows {
		if r.Filter == g.Filter {
			paths = append(paths, r.Path)
		}
	}
	if len(paths) == 0 {
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
