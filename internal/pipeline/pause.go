package pipeline

import "path/filepath"

// ResumeState is the checkpoint that lets a paused run continue where it left off. The job layer persists
// it and passes it back into a run on resume: the run reuses the SAME output dir so the per-channel masters
// already written to disk (output/<object>/<run_id>/master_<tag>.fits) are found and those channels are
// skipped rather than re-stacked.
type ResumeState struct {
	RunID  string `json:"run_id"`
	OutDir string `json:"out_dir"`
}

// PausedError is returned by a run that stopped at a cooperative pause boundary (the user asked to pause
// mid-stack). It is NOT a failure: the job layer catches it with errors.As and parks the job in the
// resumable "paused" state, keeping the per-channel masters already on disk. RunID/OutDir say where to
// resume from.
type PausedError struct {
	RunID  string
	OutDir string
}

func (e *PausedError) Error() string { return "run paused at " + e.OutDir }

// pauseRequested reports whether the caller asked this run to pause. nil hook (CLI/MCP, non-job runs) never
// pauses, so the ordinary path is unchanged.
func (o Options) pauseRequested() bool {
	return o.PauseRequested != nil && o.PauseRequested()
}

// reuseStackedChannel returns the already-stacked channel master from a prior (paused) attempt so a resume
// skips the expensive calibrate+register+stack for it. Active only when resuming and the channel's
// master_<tag>.fits exists in the reused output dir. The returned ChannelResult carries just what combine
// needs (Filter + OutputPath); its metrics were never persisted (the prior attempt paused before run.json).
func reuseStackedChannel(opts Options, object, filter, outDir string) (ChannelResult, bool) {
	if opts.Resume == nil {
		return ChannelResult{}, false
	}
	tag := filterTag(filter)
	master := filepath.Join(outDir, "master_"+tag+".fits")
	if !fileExists(master) {
		return ChannelResult{}, false
	}
	ch := ChannelResult{Object: object, Filter: filter, OutputPath: master}
	if preview := filepath.Join(outDir, "master_"+tag+"_preview.png"); fileExists(preview) {
		ch.PreviewPath = preview
	}
	return ch, true
}
