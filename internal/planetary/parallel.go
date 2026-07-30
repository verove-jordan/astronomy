// Bounded parallelism for the per-frame hot loops (score / calibrate / warp / stack). Frames are
// 4656×3520 mono (62.5 MiB per float32 plane), so every helper here bounds BOTH workers and the number
// of decoded frames in flight — cores are the budget, not RAM, but an unbounded fan-out would still
// balloon the RSS with ~200 MiB per in-flight warp.
package planetary

import (
	"context"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// planetaryWorkers is the worker bound for the in-process frame loops: all cores, capped at 10 so the
// engine/OS keep breathing room (mirrors noise.workers and the ASTRO_MAX_CPUS default Siril runs with).
func planetaryWorkers() int {
	if n := runtime.NumCPU(); n < 10 {
		return n
	}
	return 10
}

// forEachFrame runs fn(i) for i in [0, n) on up to `workers` goroutines, stopping at the first error or
// context cancellation. fn implementations write only their own slot (scores[i], out[i], …) — no locks.
func forEachFrame(ctx context.Context, n, workers int, fn func(i int) error) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i := 0; i < n; i++ {
		if gctx.Err() != nil {
			break // stop scheduling; the launched workers' error (or ctx.Err below) reports it
		}
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			return fn(i)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return ctx.Err() // a cancellation that raced the loop before any worker observed it
}

// orderedFrames decodes paths with up to `workers` parallel readers and hands each frame to consume IN
// STRICT INDEX ORDER (im == nil for an unreadable frame — the caller keeps its own skip semantics).
// The strict order keeps float accumulation bit-identical to the old serial loops. At most workers+2
// decoded frames are in flight: the window token for slot i is released only once consume(i) returns.
func orderedFrames(ctx context.Context, paths []string, workers int, consume func(i int, im *fits.Image)) error {
	slots := make([]chan *fits.Image, len(paths))
	for i := range slots {
		slots[i] = make(chan *fits.Image, 1) // buffered: a decoder never blocks on delivery
	}
	window := make(chan struct{}, workers+2)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { // dispatcher: bounded decode fan-out
		dec := new(errgroup.Group)
		dec.SetLimit(workers)
		for i, p := range paths {
			select {
			case window <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			dec.Go(func() error {
				im, err := fits.ReadImage(p)
				if err != nil {
					im = nil // frame drops out, exactly like the serial `continue`
				}
				slots[i] <- im
				return nil
			})
		}
		return dec.Wait()
	})
	g.Go(func() error { // single consumer, strict index order
		for i := range paths {
			select {
			case im := <-slots[i]:
				consume(i, im)
				<-window
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		return nil
	})
	return g.Wait()
}
