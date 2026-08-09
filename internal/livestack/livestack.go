// Package livestack drives a live deep-sky imaging session: it watches a source (a local directory or
// an S3 bucket) for newly-arriving subs, calibrates and incrementally re-stacks them so the user sees
// integration grow in near-real-time, and on Stop runs the full high-quality finish to write the
// publishable master. It orchestrates the existing engine — inspect (classify), calib (masters), the
// pipeline live primitives (calibrate + winsorized stack) and pipeline.Process (finalize) — rather than
// reimplementing any of it.
package livestack

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/source"
)

// Options configures a live-stacking run.
type Options struct {
	// Source materializes frames locally (a local dir is served in place; S3 is mirrored to disk).
	Source source.Source
	// Finalize is the fully-built batch pipeline configuration. Its Runner/Preset/Grade/WorkDir/
	// OutputDir/OnProgress are reused for the live previews; InputDir is set to Source.LocalRoot() for
	// the final full pass.
	Finalize pipeline.Options

	ExposureMs   int64         // session exposure fallback when a header lacks EXPTIME (drives integration display)
	Poll         time.Duration // source poll cadence
	Stability    time.Duration // a local file is ingested only after its size is stable this long
	RestackEvery int           // re-stack after at least this many newly-ingested files
	MinInterval  time.Duration // and at least this long since the previous re-stack
}

// Run watches the source and incrementally stacks newly-arrived subs until ctx is cancelled (the user's
// Stop). On stop it runs the full finish over everything collected and returns that result, so the job
// records success rather than cancellation. The finish runs on a fresh context so Stop does not abort it.
func Run(ctx context.Context, opts Options) (*pipeline.Result, error) {
	if opts.Source == nil {
		return nil, fmt.Errorf("livestack: no source configured")
	}
	if opts.Finalize.Runner == nil {
		return nil, fmt.Errorf("livestack: no siril runner configured")
	}
	defer func() { _ = opts.Source.Close() }()

	workAbs, err := filepath.Abs(opts.Finalize.WorkDir)
	if err != nil {
		return nil, err
	}
	outAbs, err := filepath.Abs(opts.Finalize.OutputDir)
	if err != nil {
		return nil, err
	}
	runID := time.Now().Format("20060102_150405")
	liveWork := filepath.Join(workAbs, "live", runID)
	liveOut := filepath.Join(outAbs, "live", runID)
	if err := fsutil.EnsureDir(liveOut); err != nil {
		return nil, err
	}

	sess := newSession(opts, liveWork, liveOut)
	w := newWatcher(opts.Source, opts.Stability.Milliseconds())
	sess.emit(pipeline.Progress{Step: "live stacking started — watching for new subs"})

	poll := opts.Poll
	if poll <= 0 {
		poll = 3 * time.Second
	}
	restackEvery := opts.RestackEvery
	if restackEvery < 1 {
		restackEvery = 1
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	newSince := 0
	var lastRestack time.Time

watch:
	for {
		select {
		case <-ctx.Done():
			break watch
		case <-ticker.C:
			ready, perr := w.poll(ctx, time.Now().UnixMilli())
			if perr != nil {
				if ctx.Err() != nil {
					break watch
				}
				sess.emit(pipeline.Progress{Step: "source poll failed: " + perr.Error()})
				continue
			}
			newSince += len(ready)
			if newSince < restackEvery || time.Since(lastRestack) < opts.MinInterval {
				continue
			}
			if berr := sess.runBatch(ctx, opts.Source.LocalRoot()); berr != nil {
				if ctx.Err() != nil {
					break watch
				}
				sess.emit(pipeline.Progress{Step: "live batch failed: " + berr.Error()})
			}
			newSince = 0
			lastRestack = time.Now()
		}
	}

	return sess.finalize(opts)
}

// finalize runs the full batch pipeline over everything collected, producing the publishable master. It
// uses a fresh context so the Stop that triggered it does not abort the finish.
func (s *session) finalize(opts Options) (*pipeline.Result, error) {
	root := opts.Source.LocalRoot()
	fits, _ := inspect.ListFITSFrames(root)
	raw, _ := inspect.ListRawFrames(root)
	if len(fits) == 0 && len(raw) == 0 {
		s.emit(pipeline.Progress{Step: "live stacking stopped — no frames were captured"})
		return &pipeline.Result{InputDir: root, Warnings: []string{"no frames were captured during the live session"}}, nil
	}
	s.emit(pipeline.Progress{Step: fmt.Sprintf("finalizing — full registration, stacking and finish over %d frame(s)", len(fits)+len(raw))})
	fopts := opts.Finalize
	fopts.InputDir = root
	// Both colour and mono sessions finalize through the standard deep-sky pipeline, which detects
	// one-shot color from the inventory and stacks it as a single RGB channel. Colour used to divert
	// to ProcessOSC, a thinner path with no calibration library, no plate-solving, no SPCC and a
	// plain-curves finish — so a live colour session ended up materially worse than the same frames
	// submitted as a normal run.
	res, err := pipeline.Process(context.Background(), fopts)
	if err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}
	return res, nil
}
