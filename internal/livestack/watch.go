package livestack

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/source"
)

// watcher tracks which source objects have been observed and materializes the ones that have settled.
// It is the only place that decides a file is "ready": a local capture file may still be mid-write, so
// it is ingested only once its size has stopped changing (or it has been untouched for the stability
// window). S3 objects are written atomically, so the time gate clears them on the first poll past it.
type watcher struct {
	src         source.Source
	stabilityMs int64
	seen        map[string]*fileState // key → state across polls
	prevSize    map[string]int64      // key → size at the previous poll (write-stability check)
}

// fileState is the per-object ingest state.
type fileState struct {
	fetched   bool
	localPath string
}

func newWatcher(src source.Source, stabilityMs int64) *watcher {
	return &watcher{
		src:         src,
		stabilityMs: stabilityMs,
		seen:        map[string]*fileState{},
		prevSize:    map[string]int64{},
	}
}

// poll lists the source and returns the local paths of objects that have newly become stable and were
// fetched this call. nowMs is the current wall-clock in milliseconds.
func (w *watcher) poll(ctx context.Context, nowMs int64) ([]string, error) {
	objs, err := w.src.List(ctx)
	if err != nil {
		return nil, err
	}
	curSize := make(map[string]int64, len(objs))
	var ready []string
	for _, o := range objs {
		if err := ctx.Err(); err != nil {
			return ready, err
		}
		curSize[o.Key] = o.Size
		st := w.seen[o.Key]
		if st == nil {
			st = &fileState{}
			w.seen[o.Key] = st
		}
		if st.fetched || !w.stable(o, nowMs) {
			continue
		}
		local, ferr := w.src.Fetch(ctx, o)
		if ferr != nil {
			return ready, ferr
		}
		st.fetched = true
		st.localPath = local
		ready = append(ready, local)
	}
	w.prevSize = curSize
	return ready, nil
}

// stable reports whether o has settled: its size was unchanged since the previous poll, or it has not
// been modified for at least the stability window.
func (w *watcher) stable(o source.Object, nowMs int64) bool {
	if prev, ok := w.prevSize[o.Key]; ok && prev == o.Size {
		return true
	}
	return nowMs-o.ModTime >= w.stabilityMs
}
