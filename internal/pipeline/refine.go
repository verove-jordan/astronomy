package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// RefineExistingRun re-runs ONLY the finish on an already-stacked run whose per-channel masters live
// on disk under runDir (output/<object>/<runID>/: run.json + aligned_*.fits / master_*.fits). Nothing
// is re-stacked: it reconstructs the aligned-channel map from disk and runs finishAligned (the local-
// AI-agent supervisor when opts enables it, else the standard GIMP/Siril finish), refreshing final.*
// and run.json in place. opts carries the finish dependencies (Runner/Gimp/Graxpert/Starnet/Supervisor
// /Preset/Solve/Spcc/WorkDir/OnProgress); its InputDir/OutputDir are ignored in favour of runDir.
func RefineExistingRun(ctx context.Context, opts Options, runDir string) (*postprocess.Result, error) {
	if opts.Runner == nil {
		return nil, fmt.Errorf("refine: siril runner is required")
	}
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	outDir, err := filepath.Abs(runDir)
	if err != nil {
		return nil, err
	}
	prior, err := readRunJSON(outDir)
	if err != nil {
		return nil, err
	}
	channels := reconstructChannelsFromDisk(outDir, prior.Channels)
	if len(channels) == 0 {
		return nil, fmt.Errorf("refine: no channel masters (aligned_*/master_*) found in %s", outDir)
	}

	workAbs, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	workRun := filepath.Join(workAbs, "refine_"+time.Now().Format("20060102_150405"))
	if err := fsutil.EnsureDir(workRun); err != nil {
		return nil, err
	}

	prior.OutputDir = outDir
	prior.Final = nil
	onProgress := func(p siril.Progress) {
		opts.report(Progress{Step: "refine finish", Line: p.Line, Sample: p.Sample})
	}
	finishAligned(ctx, opts, channels, prior, workRun, outDir, onProgress)
	if prior.Final == nil {
		if n := len(prior.Warnings); n > 0 {
			return nil, fmt.Errorf("refine finish failed: %s", prior.Warnings[n-1])
		}
		return nil, fmt.Errorf("refine finish produced no result")
	}
	writeRunJSON(outDir, prior) // refresh run.json with the new finish (+ iterations)
	return prior.Final, nil
}

// readRunJSON loads a prior run's record from outDir/run.json.
func readRunJSON(outDir string) (*Result, error) {
	b, err := os.ReadFile(filepath.Join(outDir, "run.json"))
	if err != nil {
		return nil, fmt.Errorf("read run.json: %w", err)
	}
	var res Result
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("parse run.json: %w", err)
	}
	return &res, nil
}

// reconstructChannelsFromDisk maps each prior channel filter to its on-disk master basename in outDir,
// preferring the co-registered aligned_<tag> over the unaligned master_<tag>. Missing files are skipped.
func reconstructChannelsFromDisk(outDir string, prior []ChannelResult) map[string]string {
	channels := map[string]string{}
	for _, ch := range prior {
		if ch.Filter == "" {
			continue
		}
		tag := filterTag(ch.Filter)
		switch {
		case fileExists(filepath.Join(outDir, "aligned_"+tag+".fits")):
			channels[ch.Filter] = "aligned_" + tag
		case fileExists(filepath.Join(outDir, "master_"+tag+".fits")):
			channels[ch.Filter] = "master_" + tag
		}
	}
	return channels
}
