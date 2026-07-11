package transfer

import (
	"context"
	"fmt"
	"os"
	"path"
	"sync"

	"golang.org/x/sync/errgroup"
)

// emitEvery throttles progress callbacks to at most one per this many bytes (plus one per file boundary),
// so a large transfer doesn't flood the SSE stream.
const emitEvery = 1 << 20 // 1 MiB

// runUpload pushes the folder to S3 (classified keys when req.Plan is set, else the legacy mirror),
// transferring up to req.concurrency() files in PARALLEL so the pipe stays saturated (a sequential
// file-by-file loop idles on each file's round-trip). Per file it decides whether the mirror already has
// it (skip) and uploads the rest; when syncOnly the skip decision is by size (default) or content
// (req.Verify). Progress is measured over the WHOLE folder — a skipped file advances the bar like an
// uploaded one — and aggregated across workers, so the bar and byte totals move from the first file with
// no silent up-front scan. Each skip records its key so the local-rel → key ledger self-heals. The
// progress + ledger callbacks are serialized (progressAggregator + mu), so the caller's closures need not
// be concurrency-safe. A manual pause stops SCHEDULING further files (in-flight ones finish, then it
// returns ErrPaused); the first hard error cancels the group and is returned.
func runUpload(ctx context.Context, client s3API, req Request, syncOnly bool, onProgress func(Progress)) (Result, error) {
	files, totalBytes, err := walkLocalFiles(req.folderDir(), req.ExcludeDirs)
	if err != nil {
		return Result{}, fmt.Errorf("scan %s: %w", req.folderDir(), err)
	}
	pm := req.planMap()
	skip, err := uploadSkipFunc(ctx, client, req, pm, syncOnly)
	if err != nil {
		return Result{}, err
	}

	prog := &progressAggregator{total: totalBytes, totalFiles: len(files), onProgress: onProgress}
	var mu sync.Mutex // serializes the ledger callback + tallies
	uploaded, skipped := 0, 0
	var uploadedBytes int64

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(req.concurrency())
	for _, f := range files {
		f := f
		if req.paused() || gctx.Err() != nil { // stop scheduling; in-flight workers finish, then Wait returns
			break
		}
		g.Go(func() error {
			if gctx.Err() != nil { // a sibling already failed → skip (paused() is polled only by the scheduler)
				return nil
			}
			rel, err := req.relOf(f.path)
			if err != nil {
				return err
			}
			key := req.keyForRel(rel, pm)
			if skip != nil {
				present, at, err := skip(f, rel, key)
				if err != nil {
					return err
				}
				if present {
					mu.Lock()
					skipped++
					req.recordStored(rel, at, f.size)
					mu.Unlock()
					prog.fileDone(rel, f.size) // a skip's bytes were not streamed — add them here
					return nil
				}
			}
			var streamed int64
			err = retryFile(gctx, func() error {
				prog.add(-streamed, rel) // undo the failed attempt's partial before restreaming from zero
				streamed = 0
				return client.Upload(gctx, req.Bucket, key, f.path, func(d int64) {
					streamed += d
					prog.add(d, rel)
				})
			})
			if err != nil {
				return err
			}
			mu.Lock()
			uploaded++
			uploadedBytes += f.size
			req.recordStored(rel, key, f.size)
			mu.Unlock()
			prog.fileDone(rel, 0) // bytes already counted via add()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return Result{}, err
	}
	if req.paused() { // a resumed sync re-checks and skips the files already uploaded
		return Result{}, ErrPaused
	}
	prog.finish()
	return Result{Op: req.Op, Files: uploaded, Bytes: uploadedBytes, Skipped: skipped}, nil
}

// skipFn reports whether a walked file is already safely mirrored (skip it) and the S3 key it was found at
// (recorded via OnStored so the classified-key ledger self-heals). It is nil for a full upload, which never
// skips.
type skipFn func(f localFile, rel, key string) (skip bool, at string, err error)

// uploadSkipFunc builds the per-file "already mirrored → skip" decision for runUpload: nil for a full
// OpUpload (nothing is skipped), a size-only check for the default sync, or a byte-for-byte check when
// req.Verify is set. The default sync lists the mirror once up front (cheap, one round trip); the verified
// sync instead Stats + MD5s each candidate file, so it does no bulk listing here.
func uploadSkipFunc(ctx context.Context, client s3API, req Request, pm map[string]string, syncOnly bool) (skipFn, error) {
	if !syncOnly {
		return nil, nil
	}
	if req.Verify {
		return verifySkip(ctx, client, req), nil
	}
	remote, err := remotePresence(ctx, client, req, pm)
	if err != nil {
		return nil, err
	}
	return sizeSkip(req, remote), nil
}

// sizeSkip is the default sync decision: skip when an object of the same size already sits at the file's
// classified or legacy key (see alreadyMirrored). remote is the precomputed key→size listing of the mirror.
// Size-only equality is BY DESIGN — captures are immutable once written, so an equal-size object is the
// same file (Verify opts into content checking when that assumption is not safe).
func sizeSkip(req Request, remote map[string]int64) skipFn {
	return func(f localFile, rel, key string) (bool, string, error) {
		present, at := alreadyMirrored(req, rel, key, f.size, remote)
		return present, at, nil
	}
}

// verifySkip is the content-verifying sync decision (req.Verify): skip only when the object at the file's
// classified or legacy key matches by size AND content (verifyMirrored — MD5 metadata, single-part ETag, or
// a size-plus-mtime fallback for legacy multipart). A missing, wrong-size or same-size-but-corrupted object
// is not skipped, so it is (re-)uploaded. Costs one Stat + one local MD5 per candidate file.
func verifySkip(ctx context.Context, client s3API, req Request) skipFn {
	return func(f localFile, rel, key string) (bool, string, error) {
		for _, k := range candidateKeys(req, rel, key) {
			ok, _, err := verifyMirrored(ctx, client, req.Bucket, k, f)
			if err != nil {
				return false, "", err
			}
			if ok {
				return true, k, nil
			}
		}
		return false, "", nil
	}
}

// candidateKeys returns the S3 keys a file may already live at: its resolved (classified) key first, plus
// the legacy mirror key when different — a file uploaded before classification lives at the legacy key.
// Preferring the classified key means OnStored records the canonical location.
func candidateKeys(req Request, rel, key string) []string {
	keys := []string{key}
	if lk := path.Join(req.baseKey(), rel); lk != key {
		keys = append(keys, lk)
	}
	return keys
}

// remotePresence lists the objects already on S3 that a sync must check against: the legacy mirror prefix
// AND — under a classified plan — each distinct planned-key directory (a capture's files scatter across
// darks/offsets/flats/lum, so one baseKey listing would miss them). Returns key → size.
func remotePresence(ctx context.Context, client s3API, req Request, pm map[string]string) (map[string]int64, error) {
	prefixes := map[string]bool{req.baseKey(): true}
	for _, k := range pm {
		prefixes[path.Dir(k)] = true
	}
	remote := map[string]int64{}
	for p := range prefixes {
		objs, err := client.List(ctx, req.Bucket, p)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			remote[o.Key] = o.Size
		}
	}
	return remote, nil
}

// alreadyMirrored reports whether a file of the given size is already on S3 at its classified key or (for
// files uploaded before classification) its legacy key, returning the key it was found at.
func alreadyMirrored(req Request, rel, key string, size int64, remote map[string]int64) (bool, string) {
	if sz, ok := remote[key]; ok && sz == size {
		return true, key
	}
	if lk := path.Join(req.baseKey(), rel); lk != key {
		if sz, ok := remote[lk]; ok && sz == size {
			return true, lk
		}
	}
	return false, ""
}

// downloadItem is one object to pull, with its local destination and folder-relative path.
type downloadItem struct {
	key, local, rel string
	size            int64
}

// runDownload pulls the folder from S3, skipping objects already present locally at the same size. Under a
// classified plan the objects live at scattered keys (darks/offsets/flats/lum), so the plan drives the
// pull and reassembles them into the original local tree; legacy-keyed leftovers are merged in.
func runDownload(ctx context.Context, client s3API, req Request, onProgress func(Progress)) (Result, error) {
	todo, totalBytes, err := planDownloads(ctx, client, req)
	if err != nil {
		return Result{}, err
	}
	prog := &progressAggregator{total: totalBytes, totalFiles: len(todo), onProgress: onProgress}
	var mu sync.Mutex // serializes the ledger callback
	var pulledBytes int64

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(req.concurrency())
	for _, it := range todo {
		it := it
		if req.paused() || gctx.Err() != nil { // stop scheduling; in-flight workers finish, then Wait returns
			break
		}
		g.Go(func() error {
			if gctx.Err() != nil { // a sibling already failed → skip (paused() is polled only by the scheduler)
				return nil
			}
			var streamed int64
			err := retryFile(gctx, func() error {
				prog.add(-streamed, it.rel) // undo the failed attempt's partial before restreaming from zero
				streamed = 0
				return client.Download(gctx, req.Bucket, it.key, it.local, func(d int64) {
					streamed += d
					prog.add(d, it.rel)
				})
			})
			if err != nil {
				return err
			}
			mu.Lock()
			pulledBytes += it.size
			req.recordStored(it.rel, it.key, it.size)
			mu.Unlock()
			prog.fileDone(it.rel, 0) // bytes already counted via add()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return Result{}, err
	}
	if req.paused() { // a resumed download skips the files already present locally (size match)
		return Result{}, ErrPaused
	}
	prog.finish()
	return Result{Op: req.Op, Files: len(todo), Bytes: pulledBytes}, nil
}

// planDownloads builds the pull set: the plan's classified entries (mapped back into the local tree),
// MERGED with any legacy-keyed objects under baseKey not covered by the plan (so a mixed mirror
// reassembles exactly). With no plan it degrades to "every object under baseKey" — the legacy behaviour.
// Size-only skip BY DESIGN (mirrors the upload sync): immutable captures make an equal-size local file the
// same file, and hashing every present file would make re-downloads as slow as downloads.
func planDownloads(ctx context.Context, client s3API, req Request) ([]downloadItem, int64, error) {
	var todo []downloadItem
	var total int64
	inPlan := map[string]bool{}
	for _, pf := range req.Plan {
		inPlan[pf.Rel] = true
		local, err := req.localForRel(pf.Rel)
		if err != nil {
			continue // reject a plan rel that would escape the folder
		}
		if fi, err := os.Stat(local); err == nil && fi.Size() == pf.Size {
			continue // already have an identical local copy
		}
		todo = append(todo, downloadItem{key: pf.Key, local: local, rel: pf.Rel, size: pf.Size})
		total += pf.Size
	}
	objs, err := client.List(ctx, req.Bucket, req.baseKey())
	if err != nil {
		return nil, 0, err
	}
	for _, o := range objs {
		local, err := req.localFor(o.Key)
		if err != nil {
			continue // skip a key that escapes the folder
		}
		rel, _ := req.relOf(local)
		if inPlan[rel] {
			continue // already scheduled from the plan
		}
		if fi, err := os.Stat(local); err == nil && fi.Size() == o.Size {
			continue
		}
		todo = append(todo, downloadItem{key: o.Key, local: local, rel: rel, size: o.Size})
		total += o.Size
	}
	return todo, total, nil
}

// progressAggregator throttles + AGGREGATES Progress callbacks across the parallel transfer workers:
// bytesDone sums every worker's streamed bytes and completed/skipped file sizes, and files counts finished
// files. It emits at most one callback per emitEvery bytes plus one per file boundary. All state and the
// onProgress call itself are guarded by one mutex, so the caller's onProgress closure (and the ledger it
// may touch) need not be concurrency-safe — the emits are strictly serialized. Contention is low: emits
// fire only on file boundaries / every emitEvery bytes, not per network read.
type progressAggregator struct {
	total      int64
	totalFiles int
	onProgress func(Progress)

	mu       sync.Mutex
	done     int64  // bytes transferred so far: in-flight partials + completed/skipped file sizes
	files    int    // files fully transferred or skipped
	name     string // most recently active file (best-effort "current file" for the UI)
	lastEmit int64  // done at the last emit, for the emitEvery throttle
}

// add records delta transferred bytes under file name (delta is negative on a retry reset), emitting a
// throttled Progress once a full emitEvery has accumulated.
func (p *progressAggregator) add(delta int64, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done += delta
	if name != "" {
		p.name = name
	}
	if p.done-p.lastEmit >= emitEvery {
		p.emitLocked()
	}
}

// fileDone marks one file fully transferred (uploaded/downloaded) or skipped and emits a file-boundary
// Progress. extra is bytes not already streamed via add — a skip's whole size, or 0 for a completed
// transfer whose bytes were streamed.
func (p *progressAggregator) fileDone(name string, extra int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done += extra
	p.files++
	if name != "" {
		p.name = name
	}
	p.emitLocked()
}

// finish emits the terminal Progress (all files done, no current file).
func (p *progressAggregator) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.name = ""
	p.emitLocked()
}

func (p *progressAggregator) emitLocked() {
	p.lastEmit = p.done
	if p.onProgress != nil {
		p.onProgress(Progress{Name: p.name, Files: p.files, TotalFiles: p.totalFiles, BytesDone: p.done, BytesTotal: p.total})
	}
}
