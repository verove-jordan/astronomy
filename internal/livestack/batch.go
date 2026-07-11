package livestack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// liveFilterOrder is the canonical channel order for reporting (L first, matching the batch pipeline).
var liveFilterOrder = []string{"L", "R", "G", "B", "Ha", "OIII", "SII"}

// session holds the mutable state of one live-stacking run: the calibration masters built so far and a
// per-channel pool of already-calibrated frames that grows across batches.
type session struct {
	runner     *siril.Runner
	workDir    string // live scratch (per-channel pools + sequences)
	outDir     string // live output (per-channel linear masters + previews), under OutputDir
	gradeOpts  grade.Options
	exposureMs int64 // session fallback when a header lacks EXPTIME
	sink       func(pipeline.Progress)

	mastersDir string
	masters    []calib.Master
	calibSig   string // signature of the calibration sets the masters were built from
	channels   map[string]*channelState

	oscChecked bool
	isOSC      bool
}

// channelState is the growing per-filter pool. Each light is calibrated exactly once into poolDir; the
// whole pool is re-registered and re-stacked every batch (the chosen "continuous full re-stack").
type channelState struct {
	filter     string
	object     string
	exposureMs int64
	workDir    string
	poolDir    string
	pool       []string         // calibrated frame paths, registration order
	frames     []*inspect.Frame // 1:1 with pool, for grading
	done       map[string]bool  // raw light path → already calibrated (dedup across batches)
	nextIdx    int
}

func newSession(opts Options, workDir, outDir string) *session {
	grd := grade.DefaultOptions()
	if opts.Finalize.Grade != nil {
		grd = *opts.Finalize.Grade
	}
	return &session{
		runner:     opts.Finalize.Runner,
		workDir:    workDir,
		outDir:     outDir,
		gradeOpts:  grd,
		exposureMs: opts.ExposureMs,
		sink:       opts.Finalize.OnProgress,
		mastersDir: filepath.Join(workDir, "masters"),
		channels:   map[string]*channelState{},
	}
}

// runBatch re-inspects the materialized frames, refreshes masters, calibrates any new lights and
// re-stacks every channel that gained frames. It is the unit of work the watch loop debounces.
func (s *session) runBatch(ctx context.Context, localRoot string) error {
	if s.isOSCDir(localRoot) {
		return nil // one-shot-color: collect only; the full color stack runs at finalize
	}
	inv, err := inspect.Scan(ctx, localRoot)
	if err != nil {
		return err
	}
	inv.ExcludeBayer() // mono live path cannot stack Bayer frames; the OSC guard above handles pure-CFA dirs
	if err := s.refreshMasters(ctx, inv); err != nil {
		s.emit(pipeline.Progress{Step: "calibration warning: " + err.Error()})
	}

	dirty := map[string]bool{}
	for _, set := range inv.SetsOfType(inspect.Light) {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.calibrateNew(ctx, set, dirty)
	}
	for _, filter := range orderedFilters(dirty) {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.restack(ctx, s.channels[filter])
	}
	return nil
}

// calibrateNew calibrates the frames of one light set that are not yet in the channel pool, appends
// them and marks the channel dirty. Calibration is per-frame and cached: a frame already in the pool is
// never recalibrated within a master generation.
func (s *session) calibrateNew(ctx context.Context, set inspect.Set, dirty map[string]bool) {
	cs := s.channel(set.Key)
	cm := mastersFor(set.Key, s.masters)

	var newPaths []string
	var newFrames []*inspect.Frame
	for _, f := range set.Frames {
		if !cs.done[f.Path] {
			newPaths = append(newPaths, f.Path)
			newFrames = append(newFrames, f)
		}
	}
	if len(newPaths) == 0 {
		return
	}
	cal, err := pipeline.CalibrateLightsLive(ctx, s.runner, newPaths, cm, cs.workDir, cs.poolDir, cs.nextIdx, s.onSiril("calibrating "+cs.filter))
	if err != nil {
		s.emit(pipeline.Progress{Step: fmt.Sprintf("%s calibration failed: %v", cs.filter, err)})
		return
	}
	cs.pool = append(cs.pool, cal...)
	cs.frames = append(cs.frames, newFrames...)
	for _, p := range newPaths {
		cs.done[p] = true
	}
	cs.nextIdx += len(newPaths)
	dirty[cs.filter] = true
}

// restack re-registers and winsorized-stacks the whole channel pool, then reports the result.
func (s *session) restack(ctx context.Context, cs *channelState) {
	if cs == nil {
		return
	}
	ch, err := pipeline.StackLinearLive(ctx, s.runner, cs.pool, cs.frames, cs.filter, cs.workDir, s.outDir, s.gradeOpts, s.onSiril("stacking "+cs.filter))
	if err != nil {
		if ctx.Err() == nil {
			s.emit(pipeline.Progress{Step: fmt.Sprintf("%s stack failed: %v", cs.filter, err)})
		}
		return
	}
	ch.Object = cs.object
	ch.ExposureMs = cs.exposureMs
	s.report(cs, ch)
}

// report emits the live status line and preview for a channel.
func (s *session) report(cs *channelState, ch pipeline.ChannelResult) {
	if ch.Err != "" {
		s.emit(pipeline.Progress{Step: cs.filter + ": " + ch.Err, Preview: ch.PreviewPath})
		return
	}
	integMs := int64(ch.StackedFrames) * cs.exposureMs
	rejected := ch.InputFrames - ch.StackedFrames
	step := fmt.Sprintf("%s · %d/%d subs · %s integration · %d rejected",
		cs.filter, ch.StackedFrames, ch.InputFrames, humanDur(integMs), rejected)
	s.emit(pipeline.Progress{Step: step, Preview: ch.PreviewPath})
}

// refreshMasters (re)builds the calibration masters when the set of calibration frames has changed.
// When masters first become available it resets the channel pools so the subs collected before any
// calibration existed are recalibrated once; later master refreshes do not reset (they would thrash a
// long session — the final pass recalibrates everything with the complete pool anyway).
func (s *session) refreshMasters(ctx context.Context, inv *inspect.Inventory) error {
	sig := calibSignature(inv)
	if sig == "" || (sig == s.calibSig && s.masters != nil) {
		return nil
	}
	wasEmpty := s.calibSig == ""
	masters, warns, err := calib.BuildMasters(ctx, s.runner, inv, s.mastersDir, s.workDir, s.onSiril("building master calibration frames"))
	if err != nil {
		return err
	}
	s.masters = masters
	s.calibSig = sig
	for _, w := range warns {
		s.emit(pipeline.Progress{Step: "master: " + w})
	}
	if len(masters) > 0 {
		s.emit(pipeline.Progress{Step: fmt.Sprintf("built %d master calibration frame(s)", len(masters))})
		if wasEmpty {
			s.resetChannels()
		}
	}
	return nil
}

// resetChannels drops every channel pool so the next batch recalibrates all collected lights with the
// newly-available masters.
func (s *session) resetChannels() {
	for _, cs := range s.channels {
		_ = os.RemoveAll(cs.poolDir)
		cs.pool = nil
		cs.frames = nil
		cs.done = map[string]bool{}
		cs.nextIdx = 0
	}
}

// channel returns the per-filter state, creating it on first sight.
func (s *session) channel(key inspect.SetKey) *channelState {
	if cs := s.channels[key.Filter]; cs != nil {
		return cs
	}
	exp := key.ExposureMs
	if exp <= 0 {
		exp = s.exposureMs
	}
	cw := filepath.Join(s.workDir, "ch_"+tag(key.Filter))
	cs := &channelState{
		filter:     key.Filter,
		object:     key.Object,
		exposureMs: exp,
		workDir:    cw,
		poolDir:    filepath.Join(cw, "pool"),
		done:       map[string]bool{},
	}
	s.channels[key.Filter] = cs
	return cs
}

// isOSCDir reports whether the watched dir holds one-shot-color frames, caching the verdict once frames
// exist. The mono live preview cannot debayer, so an OSC session is collected silently and stacked in
// full colour at finalize.
func (s *session) isOSCDir(root string) bool {
	if s.oscChecked {
		return s.isOSC
	}
	frames, _ := inspect.ListFITSFrames(root)
	if len(frames) == 0 {
		return false // undecided until the first frame lands
	}
	s.isOSC = inspect.IsOSCDir(root)
	s.oscChecked = true
	if s.isOSC {
		s.emit(pipeline.Progress{Step: "one-shot-color session detected — collecting subs; the full colour stack runs on Stop"})
	}
	return s.isOSC
}

func (s *session) emit(p pipeline.Progress) {
	if s.sink != nil {
		s.sink(p)
	}
}

// onSiril forwards a Siril runner's log lines and resource samples to the live event sink under a step.
func (s *session) onSiril(step string) func(siril.Progress) {
	return func(sp siril.Progress) {
		s.emit(pipeline.Progress{Step: step, Line: sp.Line, Sample: sp.Sample})
	}
}

// mastersFor resolves the dark/flat/bias masters for a light set, plus the dark's measured defect
// map when one exists beside it (per-frame -cc=bpm repair instead of -cc=dark).
func mastersFor(key inspect.SetKey, masters []calib.Master) siril.CalibMasters {
	dark, flat, bias := calib.MatchForLight(key, masters).Masters()
	return siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias, BadPixelMap: calib.DefectsListFor(dark)}
}

// calibSignature fingerprints the calibration sets so masters are rebuilt only when they change.
func calibSignature(inv *inspect.Inventory) string {
	var parts []string
	for _, ft := range []inspect.FrameType{inspect.Bias, inspect.DarkFlat, inspect.Dark, inspect.Flat} {
		for _, set := range inv.SetsOfType(ft) {
			k := set.Key
			parts = append(parts, fmt.Sprintf("%s:%s:e%d:g%d:o%d:b%d:t%d:n%d",
				ft, k.Filter, k.ExposureMs, k.Gain, k.Offset, k.Bin, k.TempBucket, set.Count))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// orderedFilters returns the dirty channels in canonical order (known filters first, then the rest).
func orderedFilters(dirty map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range liveFilterOrder {
		if dirty[f] {
			out = append(out, f)
			seen[f] = true
		}
	}
	var rest []string
	for f := range dirty {
		if !seen[f] {
			rest = append(rest, f)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// humanDur renders an integration time (milliseconds) compactly, e.g. "1h09m", "57m04s", "45s".
func humanDur(ms int64) string {
	sec := ms / 1000
	h, m, s := sec/3600, (sec%3600)/60, sec%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// tag sanitizes a filter name for use in a path segment.
func tag(filter string) string {
	if filter == "" {
		return "mono"
	}
	out := make([]rune, 0, len(filter))
	for _, r := range filter {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		case r == ' ':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "ch"
	}
	return string(out)
}
