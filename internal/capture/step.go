package capture

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/dither"
	"github.com/verove-jordan/astronomy/internal/filters"
	"github.com/verove-jordan/astronomy/internal/platesolve"
)

// One exposure, start to finish: settle the filter, apply the settings, dither if due, expose, wait,
// save. Each of these has a failure mode that quietly ruins frames if skipped — a wheel still
// turning, a gain left from the previous step, a dither that never happened — so none of them is
// optional or fire-and-forget.

// runState carries what persists across the frames of one session.
type runState struct {
	req         Request
	planner     *dither.Planner
	currentSlot int
	sinceDither int
	sequence    int
	night       string
}

func (r *Runner) captureOne(ctx context.Context, state *runState, step Step, index int) error {
	if err := r.applyFilter(ctx, state, step); err != nil {
		return err
	}
	if err := r.applySettings(ctx, step); err != nil {
		return err
	}
	if err := r.maybeDither(ctx, state, step); err != nil {
		return err
	}

	startedAt := time.Now()
	r.mu.Lock()
	r.progress.ExposureEnds = startedAt.Add(time.Duration(step.ExposureUs) * time.Microsecond)
	r.mu.Unlock()
	r.publish()

	if err := r.client.StartExposure(ctx, isCalibration(step.Type)); err != nil {
		return fmt.Errorf("start exposure: %w", err)
	}
	if err := r.waitForExposure(ctx, step); err != nil {
		return err
	}

	state.sequence++
	saved, err := r.client.Save(ctx, r.saveRequestFor(state, step, startedAt))
	if err != nil {
		return fmt.Errorf("save frame: %w", err)
	}
	r.recordFrame(ctx, state, step, saved, startedAt)
	return nil
}

// applyFilter moves the wheel only when the filter actually changes, and always waits for it to
// settle before exposing.
func (r *Runner) applyFilter(ctx context.Context, state *runState, step Step) error {
	slot := step.Slot
	if slot <= 0 {
		if isCalibration(step.Type) && strings.TrimSpace(step.Filter) == "" {
			return nil // darks and bias do not care what is in the beam
		}
		resolved, err := r.resolveSlot(ctx, step.Filter)
		if err != nil {
			return err
		}
		slot = resolved
	}
	if slot == state.currentSlot {
		return nil
	}
	if _, err := r.client.SetFilter(ctx, slot); err != nil {
		return fmt.Errorf("select filter %q (slot %d): %w", step.Filter, slot, err)
	}
	state.currentSlot = slot
	return nil
}

// resolveSlot maps a filter NAME onto a wheel slot, so a sequence is written in terms of filters
// ("Ha") rather than positions that change whenever the wheel is rearranged.
//
// Matching is alias-aware: a step asking for "SII" finds a slot labelled "S2" or "sulfur", because
// both sides go through the same canonicalizer ingest uses. An exact (case-insensitive) match is
// tried first so a genuinely custom name still resolves even if it is not a known token.
func (r *Runner) resolveSlot(ctx context.Context, filter string) (int, error) {
	st, err := r.client.Wheel(ctx)
	if err != nil {
		return 0, fmt.Errorf("read filter wheel: %w", err)
	}
	if !st.Connected {
		return 0, fmt.Errorf("no filter wheel is connected, but the sequence asks for filter %q", filter)
	}
	want := strings.TrimSpace(filter)
	for i, name := range st.Wheel.Names {
		if strings.EqualFold(strings.TrimSpace(name), want) {
			return i + 1, nil
		}
	}
	if canon, ok := filters.Token(want); ok {
		for i, name := range st.Wheel.Names {
			if n, ok := filters.Token(name); ok && n == canon {
				return i + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("filter %q is not in the wheel (%s)", filter, strings.Join(st.Wheel.Names, ", "))
}

func (r *Runner) applySettings(ctx context.Context, step Step) error {
	if err := r.client.SetControl(ctx, device.ControlExposure, step.ExposureUs); err != nil {
		return fmt.Errorf("set exposure: %w", err)
	}
	if step.Gain > 0 {
		if err := r.client.SetControl(ctx, device.ControlGain, step.Gain); err != nil {
			return fmt.Errorf("set gain: %w", err)
		}
	}
	if step.Offset > 0 {
		if err := r.client.SetControl(ctx, device.ControlOffset, step.Offset); err != nil {
			return fmt.Errorf("set offset: %w", err)
		}
	}
	return nil
}

// maybeDither nudges the mount every N frames. The offsets come from the dither planner, which
// spreads them deliberately rather than randomly; the pixel→arcsecond conversion needs the image
// scale, and without it dithering is skipped with a message rather than guessed at.
func (r *Runner) maybeDither(ctx context.Context, state *runState, step Step) error {
	if step.DitherN <= 0 || isCalibration(step.Type) {
		return nil
	}
	state.sinceDither++
	if state.sinceDither < step.DitherN {
		return nil
	}
	state.sinceDither = 0
	scale := state.req.ImageScaleArcsecPx
	if scale <= 0 {
		r.note("dither skipped: the image scale is unknown")
		return nil
	}
	delta, target := state.planner.Next()

	// Measure what the move achieved rather than assuming it landed. Backlash means a commanded eight
	// pixels can deliver three, and a planner told only what it asked for drifts away from the even
	// spread it was chosen to produce. The device server watches one star across the move, so this
	// costs two short exposures and no extra round trips.
	res, err := r.client.NudgeMeasured(ctx, delta.X*scale, delta.Y*scale, ditherMeasureExposureSec)
	if err != nil {
		// A mount that cannot dither is not a reason to lose the night's frames.
		r.note("dither skipped: " + err.Error())
		return nil
	}
	if res.Measured {
		// The measurement already includes the settle, so the shutter can open again straight away.
		state.planner.Achieved(dither.Offset{
			X: state.planner.Current().X + res.DXPx,
			Y: state.planner.Current().Y + res.DYPx,
		})
		return nil
	}

	// Nothing to measure against — believe the command, and settle blindly as before.
	state.planner.Achieved(target)
	if res.Reason != "" {
		r.note("dither not measured: " + res.Reason)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
	}
	return nil
}

// ditherMeasureExposureSec is the framing shot used to see where the stars went. Short: it is
// measuring a few pixels of displacement, not taking a picture.
const ditherMeasureExposureSec = 1.0

// waitForExposure polls until the frame is ready. Polling (rather than a blocking download) is what
// keeps a 10-minute sub abortable.
func (r *Runner) waitForExposure(ctx context.Context, step Step) error {
	// A generous ceiling: twice the exposure plus a minute of download and slack.
	limit := time.Duration(step.ExposureUs)*time.Microsecond*2 + time.Minute
	deadline := time.Now().Add(limit)
	for {
		state, err := r.client.ExposureStatus(ctx)
		if err != nil {
			return fmt.Errorf("poll exposure: %w", err)
		}
		switch state {
		case device.ExposureSuccess:
			return nil
		case device.ExposureFailed:
			return fmt.Errorf("the camera reported a failed exposure")
		}
		if time.Now().After(deadline) {
			_ = r.client.AbortExposure(ctx)
			return fmt.Errorf("exposure did not complete within %s", limit)
		}
		select {
		case <-ctx.Done():
			_ = r.client.AbortExposure(ctx)
			return ctx.Err()
		case <-time.After(waitStep):
		}
	}
}

// frameFolder is the sub-folder of <root>/[panel/] a step's frames land in, so the filter and the
// frame type are recorded in the PATH as well as in the filename and the FITS header. Three
// independent copies of the same fact: a header stripped by a converter, or a file renamed by hand,
// still leaves the folder saying which filter these frames were shot through.
//
// The layout mirrors what inspect already parses out of real-world captures, so nothing downstream
// needs to learn a new convention:
//
//	lights          <root>/[panel/]<Filter>/          e.g. p01/SII/
//	flats           <root>/[panel/]flats/<Filter>/    flats are per-filter (SetKey includes Filter)
//	darks/bias/…    <root>/[panel/]<type>/            filter-agnostic, mirrors inspect
//
// A step with no filter (a mono rig, or a dark) simply gets no filter segment.
func frameFolder(step Step) string {
	filter := device.SanitizeToken(step.Filter)
	switch strings.ToLower(strings.TrimSpace(step.Type)) {
	case "dark":
		return "darks"
	case "bias", "offset", "zero":
		return "bias"
	case "darkflat", "dark_flat", "flatdark":
		return "darkflats"
	case "flat":
		return filepath.Join("flats", filter) // filepath.Join drops an empty segment
	default: // light (the zero value) — grouped by filter alone
		return filter
	}
}

// saveRequestFor builds the destination path and the metadata the header needs. The path layout is
// the one the stacker already understands: <root>/[panel/]<frameFolder>/<canonical name>.
func (r *Runner) saveRequestFor(state *runState, step Step, startedAt time.Time) SaveRequest {
	meta := device.FrameMeta{
		Type: step.Type, Filter: step.Filter,
		ExposureUs: step.ExposureUs, Gain: step.Gain, Offset: step.Offset,
		Bin: step.Bin, StartedAt: startedAt,
	}
	dir := state.req.Root
	if p := strings.TrimSpace(state.req.Panel); p != "" {
		dir = filepath.Join(dir, p)
	}
	if sub := frameFolder(step); sub != "" {
		dir = filepath.Join(dir, sub)
	}
	return SaveRequest{
		Path:      filepath.Join(dir, meta.FileName(state.sequence)),
		Type:      step.Type,
		Filter:    step.Filter,
		Object:    state.req.Object,
		Telescope: state.req.Telescope,
		FocalMM:   state.req.FocalMM,
		Panel:     state.req.Panel,
		SessionID: fmt.Sprintf("%d", r.Progress().SessionID),
	}
}

// recordFrame updates the live progress and persists the frame.
func (r *Runner) recordFrame(ctx context.Context, state *runState, step Step, saved SavedFrame, startedAt time.Time) {
	filter := step.Filter
	if filter == "" {
		filter = step.Type
	}
	r.mu.Lock()
	r.progress.FrameIndex++
	r.progress.LastPath = saved.Path
	if r.progress.Captured == nil {
		r.progress.Captured = map[string]int{}
	}
	r.progress.Captured[filter]++
	r.progress.ETASeconds = r.etaSecondsLocked()
	id := r.progress.SessionID
	snapshot := r.progress
	r.mu.Unlock()
	r.publish()

	if r.recorder == nil || id == 0 {
		return
	}
	_ = r.recorder.RecordFrame(ctx, id, FrameRecord{
		Path: saved.Path, Filter: step.Filter, Type: step.Type,
		ExposureUs: saved.ExposureUs, Gain: saved.Gain, Offset: step.Offset,
		Bin: step.Bin, TempMilliC: saved.TempMilliC, Panel: state.req.Panel,
		StartedAt: startedAt, Sequence: state.sequence,
	})
	_ = r.recorder.UpdateSession(ctx, id, snapshot.Status, snapshot)
	r.observeTracking(ctx, state, step, saved, startedAt, id)
}

// observeTracking hands light frames to the tracking monitor. Only lights: a dark or a flat has no
// sky in it to solve, and a filter change mid-run is irrelevant because the measurement is of the
// mount, not the optical path.
func (r *Runner) observeTracking(ctx context.Context, state *runState, step Step, saved SavedFrame, startedAt time.Time, sessionID int64) {
	r.mu.RLock()
	tracker := r.tracker
	r.mu.RUnlock()
	if tracker == nil || !strings.EqualFold(step.Type, "light") {
		return
	}
	// Mid-exposure, not the start: a 300 s sub's drift belongs at its midpoint, and using the start
	// would shift every sample by half an exposure — a systematic phase error in the fold.
	mid := startedAt.Add(time.Duration(saved.ExposureUs/2) * time.Microsecond)
	tracker.Observe(ctx, sessionID, saved.Path, mid, platesolve.Hint{
		RADeg: state.req.RADeg, DecDeg: state.req.DecDeg,
		HasHint: state.req.RADeg != 0 || state.req.DecDeg != 0,
		FocalMM: state.req.FocalMM, PixelUm: state.req.PixelUm,
	})
}

// etaSecondsLocked estimates the remaining time from the average frame so far — which includes the
// download and filter changes a naive "exposure × frames left" estimate ignores. Caller holds r.mu.
func (r *Runner) etaSecondsLocked() float64 {
	done := r.progress.FrameIndex
	if done <= 0 || r.progress.StartedAt.IsZero() {
		return 0
	}
	elapsed := time.Since(r.progress.StartedAt).Seconds()
	perFrame := elapsed / float64(done)
	return perFrame * float64(r.progress.TotalFrames-done)
}

// note records a non-fatal message on the live progress.
func (r *Runner) note(msg string) {
	r.mu.Lock()
	r.progress.Message = msg
	r.mu.Unlock()
	r.publish()
}
