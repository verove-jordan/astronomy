package transfer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// fakeS3 is an in-memory, optionally flaky s3API for unit tests. objects answers Stat/List; uploadFails/
// downloadFails make the first N attempts on a key fail with failWith, after emitting partial bytes to
// onBytes — modelling a connection dropped mid-file.
type fakeS3 struct {
	mu            sync.Mutex                // guards the maps: runUpload/runDownload now call in parallel
	objects       map[string]s3store.Object // keyed by full S3 key
	uploadFails   map[string]int            // remaining failing attempts per key
	downloadFails map[string]int
	failWith      error // error returned by failing attempts
	partial       int64 // bytes reported to onBytes before a failing attempt errors
	uploadCalls   map[string]int
	downloadCalls map[string]int
}

func newFakeS3() *fakeS3 {
	return &fakeS3{
		objects:       map[string]s3store.Object{},
		uploadFails:   map[string]int{},
		downloadFails: map[string]int{},
		uploadCalls:   map[string]int{},
		downloadCalls: map[string]int{},
	}
}

func (f *fakeS3) List(_ context.Context, _, prefix string) ([]s3store.Object, error) {
	f.mu.Lock()
	var out []s3store.Object
	for _, o := range f.objects {
		if strings.HasPrefix(o.Key, prefix) {
			out = append(out, o)
		}
	}
	f.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeS3) Stat(_ context.Context, _, key string) (s3store.Object, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	return o, ok, nil
}

// Readiness mirrors the real client: derive readiness from the stored object's class + restore status.
func (f *fakeS3) Readiness(_ context.Context, _, key string) (s3store.Readiness, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	if !ok {
		return 0, os.ErrNotExist
	}
	switch {
	case !o.Archived():
		return s3store.Readable, nil
	case o.RestoreReady():
		return s3store.Readable, nil
	case o.RestorePending():
		return s3store.Pending, nil
	default:
		return s3store.NeedsRestore, nil
	}
}

func (f *fakeS3) Upload(_ context.Context, _, key, localPath string, onBytes func(int64)) error {
	f.mu.Lock()
	f.uploadCalls[key]++
	failing := f.uploadFails[key] > 0
	if failing {
		f.uploadFails[key]--
	}
	partial, failWith := f.partial, f.failWith
	f.mu.Unlock()

	fi, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if failing {
		if onBytes != nil && partial > 0 {
			onBytes(min(partial, fi.Size()))
		}
		return failWith
	}
	// Record the content MD5 like the real Upload does (Astro-Md5 user metadata), so a verified sync/
	// remove-local can actually compare bytes against this fake mirror. The heavy work (Stat, MD5, onBytes)
	// runs OUTSIDE the lock so parallel workers actually overlap — exercising the aggregator/ledger races.
	sum, err := s3store.MD5File(localPath)
	if err != nil {
		return err
	}
	if onBytes != nil {
		onBytes(fi.Size())
	}
	f.mu.Lock()
	f.objects[key] = s3store.Object{Key: key, Size: fi.Size(), MD5: sum, ModTime: time.Now().UnixMilli()}
	f.mu.Unlock()
	return nil
}

func (f *fakeS3) Download(_ context.Context, _, key, localPath string, onBytes func(int64)) error {
	f.mu.Lock()
	f.downloadCalls[key]++
	obj, ok := f.objects[key]
	failing := f.downloadFails[key] > 0
	if failing {
		f.downloadFails[key]--
	}
	partial, failWith := f.partial, f.failWith
	f.mu.Unlock()

	if !ok {
		return os.ErrNotExist
	}
	if failing {
		if onBytes != nil && partial > 0 {
			onBytes(min(partial, obj.Size))
		}
		return failWith
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(localPath, make([]byte, obj.Size), 0o644); err != nil {
		return err
	}
	if onBytes != nil {
		onBytes(obj.Size)
	}
	return nil
}

// fastFileRetry shrinks the per-file retry backoff for one test (nothing depends on exact sleeps).
func fastFileRetry(t *testing.T) {
	t.Helper()
	old := fileRetryBase
	fileRetryBase = time.Millisecond
	t.Cleanup(func() { fileRetryBase = old })
}

// writeTestFolder creates LocalRoot/M101/{a.fits: "aaaaa", lights/b.fits: "bbb"} and returns the Request
// (op unset) plus the folder path.
func writeTestFolder(t *testing.T) (Request, string) {
	t.Helper()
	root := t.TempDir()
	folder := filepath.Join(root, "M101")
	if err := os.MkdirAll(filepath.Join(folder, "lights"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "a.fits"), []byte("aaaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "lights", "b.fits"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Request{LocalRoot: root, RelPath: "M101", Bucket: "b", KeyPrefix: "acct/data"}, folder
}
