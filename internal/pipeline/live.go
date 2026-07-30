package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Live-stacking stack primitives. They reuse the same Siril building blocks as the batch pipeline
// (processChannel / processChannelGroups) but split calibration from stacking so the orchestrator can
// (a) calibrate each sub exactly once — caching the result across batches — and (b) re-register and
// re-stack the whole growing pool every batch (the user-chosen "continuous full re-stack"), without the
// per-batch finish (AI background extraction, denoise, channel combine) that belongs only to the final
// pass. They live in package pipeline so they can reuse the unexported gradeChannel / calibratedFramePaths
// / filterTag helpers — keeping the grading rules identical to a normal run.

// CalibrateLightsLive calibrates newly-arrived light frames with the matched masters and moves the
// calibrated FITS into poolDir with stable, unique names (cal_<n>.fits, numbered from startIndex),
// returning their paths in input order. Only the new frames are calibrated — already-cached frames are
// never recomputed. With no masters available yet it returns the raw light paths unchanged: the live
// preview then stacks uncalibrated subs until calibration frames arrive, and the final pass (a full
// pipeline.Process) recalibrates everything from the complete pool.
func CalibrateLightsLive(ctx context.Context, runner *siril.Runner, newLights []string, m siril.CalibMasters,
	workDir, poolDir string, startIndex int, onProgress func(siril.Progress)) ([]string, error) {
	if len(newLights) == 0 {
		return nil, nil
	}
	if err := fsutil.EnsureDir(poolDir); err != nil {
		return nil, err
	}
	if (m == siril.CalibMasters{}) { // no masters yet → pool the raw subs as-is
		return append([]string(nil), newLights...), nil
	}

	stageDir := filepath.Join(workDir, "cal_stage")
	if err := os.RemoveAll(stageDir); err != nil {
		return nil, fmt.Errorf("clear calibration staging: %w", err)
	}
	if _, err := fsutil.LinkFrames(stageDir, newLights); err != nil {
		return nil, err
	}
	// A poll tick often delivers a single new frame: link writes no .seq for one image, so the
	// sequence calibrate would fail — calibrate_single produces the same pp_ output.
	calScript := siril.CalibrateOnlyScript("light", m)
	if len(newLights) == 1 {
		calScript = siril.CalibrateSingleScript("light", m)
	}
	if _, err := runner.Run(ctx, stageDir, calScript, onProgress); err != nil {
		return nil, fmt.Errorf("calibrate new lights: %w", err)
	}
	base := siril.CalibratedSeq("light", m) // "pp_light"
	produced := calibratedFramePaths(stageDir, base, len(newLights))
	out := make([]string, 0, len(produced))
	for i, src := range produced {
		dst := filepath.Join(poolDir, fmt.Sprintf("cal_%05d.fits", startIndex+i))
		if err := os.Rename(src, dst); err != nil {
			if cerr := fsutil.CopyFile(src, dst); cerr != nil { // cross-device fallback
				return nil, fmt.Errorf("stage calibrated frame %s: %w", src, cerr)
			}
		}
		out = append(out, dst)
	}
	_ = os.RemoveAll(stageDir)
	return out, nil
}

// StackLinearLive registers the already-calibrated pool frames of one channel, grades them with the
// normal (session-relative) rules, and winsorized-stacks the survivors into outDir/master_<filter>.fits,
// then writes a downscaled preview PNG. frames must be 1:1 with poolPaths (registration order) for
// grading. Unlike finishStackedChannel it KEEPS the pool (it grows across batches) and SKIPS the heavy
// finish — the live counterpart used by the live-stacking orchestrator. The returned ChannelResult
// carries the linear master path, the preview path and per-frame metrics for the UI.
func StackLinearLive(ctx context.Context, runner *siril.Runner, poolPaths []string, frames []*inspect.Frame,
	filter, workDir, outDir string, gradeOpts grade.Options, onProgress func(siril.Progress)) (ChannelResult, error) {
	ch := ChannelResult{Filter: filter, InputFrames: len(poolPaths)}
	if len(poolPaths) == 0 {
		ch.Err = "no frames to stack"
		return ch, nil
	}
	if err := fsutil.EnsureDir(outDir); err != nil {
		return ch, err
	}

	// The pool's first frame arrives alone: Siril has no one-image sequences to register or stack,
	// so publish the calibrated frame as the running master directly — the live view gets a first
	// image immediately, and the next batch re-stacks the grown pool normally.
	if len(poolPaths) == 1 {
		masterName := "master_" + filterTag(filter)
		outBase := filepath.Join(outDir, masterName)
		if err := fsutil.CopyFile(poolPaths[0], outBase+".fits"); err != nil {
			ch.Err = err.Error()
			return ch, err
		}
		ch.StackedFrames = 1
		ch.OutputPath = outBase + ".fits"
		ch.Selection.Notes = append(ch.Selection.Notes, "only 1 frame pooled — showing it as the master (no stacking yet)")
		if _, err := runner.Run(ctx, outDir, siril.PreviewScript(masterName+".fits", masterName+"_preview", 0.5), nil); err == nil {
			ch.PreviewPath = filepath.Join(outDir, masterName+"_preview.png")
		}
		return ch, nil
	}

	// Re-link the whole pool into a fresh sequence and register it (no further calibration — the pool is
	// already calibrated). Clean any prior batch's sequence first so disk does not grow unbounded.
	seqDir := filepath.Join(workDir, "live_"+filterTag(filter))
	if err := os.RemoveAll(seqDir); err != nil {
		return ch, fmt.Errorf("clear live sequence: %w", err)
	}
	if _, err := fsutil.LinkFrames(seqDir, poolPaths); err != nil {
		ch.Err = err.Error()
		return ch, err
	}
	noMasters := siril.CalibMasters{}
	if _, err := runner.Run(ctx, seqDir, siril.CalibrateRegisterScript("live", noMasters), onProgress); err != nil {
		ch.Err = err.Error()
		return ch, err
	}

	base := siril.CalibratedSeq("live", noMasters) // "live"
	reg := siril.RegisteredSeq("live", noMasters)  // "r_live"
	metrics, rejected, regCount, err := gradeChannel(seqDir, base, frames, gradeOpts, false, nil, nil)
	if err != nil {
		ch.Err = "grading: " + err.Error()
		return ch, err
	}
	ch.Metrics = metrics
	ch.StackedFrames = regCount - len(rejected)
	if regCount == 0 {
		ch.Err = "no frames could be registered yet"
		return ch, nil
	}

	masterName := "master_" + filterTag(filter)
	outBase := filepath.Join(outDir, masterName)
	_, stackNote, err := stackSelectedOrCopy(ctx, runner, seqDir, reg, regCount, rejected, outBase, "wfwhm", onProgress)
	if err != nil {
		ch.Err = err.Error()
		return ch, err
	}
	if stackNote != "" {
		ch.Selection.Notes = append(ch.Selection.Notes, stackNote)
	}
	ch.OutputPath = outBase + ".fits"

	// A downscaled, auto-stretched preview for the live UI (no AI finish — that is the final pass).
	if _, err := runner.Run(ctx, outDir, siril.PreviewScript(masterName+".fits", masterName+"_preview", 0.5), nil); err == nil {
		ch.PreviewPath = filepath.Join(outDir, masterName+"_preview.png")
	}
	return ch, nil
}
