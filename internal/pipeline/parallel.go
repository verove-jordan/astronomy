package pipeline

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// channelParallelism resolves how many channels stack concurrently: ASTRO_CHANNEL_PARALLEL capped
// by the channel count, forced serial in low-disk staged mode (the stager downloads/frees one
// channel wave at a time — its disk accounting is inherently sequential).
func (o Options) channelParallelism(channels int) int {
	n := o.ChannelParallel
	if n <= 1 || o.Stager != nil {
		return 1
	}
	if n > channels {
		n = channels
	}
	return n
}

// stackOneChannel stacks one channel: the proven single-session fast path, or the grouped
// cross-session merge. ref pins the channel's step slot so the grouped path can emit per-session
// sub-step lines.
func stackOneChannel(ctx context.Context, opts Options, plan *ReusePlan, object, filter string,
	masters []calib.Master, flats *flatCache, parity *parityCache, workRun, outDir string,
	gradeOpts grade.Options, prog func(siril.Progress), ref stepRef) ChannelResult {
	groups := plan.byFilter[filter]
	if useFastPath(plan, groups) {
		set := inspect.Set{Key: groups[0].Key, Frames: groups[0].Frames, Count: len(groups[0].Frames)}
		return processChannel(ctx, opts, set, masters, workRun, outDir, gradeOpts, prog)
	}
	return processChannelGroups(ctx, opts, object, filter, groups, masters, flats, parity, workRun, outDir, gradeOpts, prog, ref, plan.AnchorNight)
}

// useFastPath reports whether a channel takes the proven single-set fast path: the current
// capture's lone group, in a run where NO channel merges groups. An anchored run (some channel
// with ≥2 groups) routes every channel — even single-group ones — through the grouped path, so
// all channel masters land on the same anchor-night canvas: mixing the fast path's own-reference
// canvas with anchored ones produced the mixed-dimension masters that killed the combine in
// task #312.
func useFastPath(plan *ReusePlan, groups []lightGroup) bool {
	return !plan.Anchored && len(groups) == 1 && groups[0].Current
}

// channelStepLabel names a channel's stacking step, matching the historical serial labels.
func channelStepLabel(plan *ReusePlan, object, filter string) string {
	groups := plan.byFilter[filter]
	if len(groups) == 1 && groups[0].Current {
		return fmt.Sprintf("grading + stacking %s %s", object, filter)
	}
	return fmt.Sprintf("grading + stacking %s %s (%d groups)", object, filter, len(groups))
}

// runParallelWave stacks the wave's pending channels concurrently (ASTRO_CHANNEL_PARALLEL > 1):
// each channel owns a fixed step slot on the bar, the progress sink is mutex-serialized, and the
// Siril CPU/memory budget is split evenly. Failures stay per-channel (ch.Err) exactly like the
// serial path; results land by wave index.
func runParallelWave(ctx context.Context, opts Options, plan *ReusePlan, object string, wave []string,
	pending []int, waveStart int, results []ChannelResult, masters []calib.Master, flats *flatCache,
	parity *parityCache, workRun, outDir string, gradeOpts grade.Options) {
	wo := opts
	wo.OnProgress = serializedProgress(opts.OnProgress)
	wo.Runner = opts.Runner.WithLimits(dividedLimits(opts.Runner.Limits(), len(pending)))
	if wo.steps != nil {
		wo.steps.finish() // close the running serial step; wave channels own fixed slots below
	}
	g, gctx := errgroup.WithContext(ctx)
	for _, i := range pending {
		filter := wave[i]
		g.Go(func() error {
			// Slot index: masters is step 1, channel k (0-based across the run) is step k+2.
			prog, done, ref := wo.stepAt(waveStart+i+2, channelStepLabel(plan, object, filter))
			defer done()
			results[i] = stackOneChannel(gctx, wo, plan, object, filter, masters, flats, parity,
				workRun, outDir, gradeOpts, prog, ref)
			return nil
		})
	}
	_ = g.Wait() // workers never return errors — failures are per-channel results
	if wo.steps != nil {
		wo.steps.advanceTo(waveStart + len(wave) + 1) // serial cursor resumes after the wave's slots
	}
}

// serializedProgress wraps a progress sink with a mutex: the job-side fan-out mutates shared state
// (log ring, live position), so concurrent channels must not interleave mid-event.
func serializedProgress(inner func(Progress)) func(Progress) {
	if inner == nil {
		return nil
	}
	var mu sync.Mutex
	return func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		inner(p)
	}
}

// dividedLimits splits the Siril resource budget across n concurrent stacks. Zero values (Siril
// defaults) are resolved first — otherwise two "unlimited" instances would each take the whole
// machine (all cores, 90% of RAM each).
func dividedLimits(lim siril.Limits, n int) siril.Limits {
	if n <= 1 {
		return lim
	}
	cpus := lim.MaxCPUs
	if cpus <= 0 {
		cpus = runtime.NumCPU()
	}
	if cpus /= n; cpus < 1 {
		cpus = 1
	}
	lim.MaxCPUs = cpus
	ratio := lim.MemRatio
	if ratio <= 0 {
		ratio = 0.9 // Siril's own default share
	}
	lim.MemRatio = ratio / float64(n)
	return lim
}
