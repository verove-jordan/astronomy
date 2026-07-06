package transfer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// fakeS3 is an in-memory, optionally flaky s3API for unit tests. objects answers Stat/List; uploadFails/
// downloadFails make the first N attempts on a key fail with failWith, after emitting partial bytes to
// onBytes — modelling a connection dropped mid-file.
type fakeS3 struct {
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
	var out []s3store.Object
	for _, o := range f.objects {
		if strings.HasPrefix(o.Key, prefix) {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeS3) Stat(_ context.Context, _, key string) (s3store.Object, bool, error) {
	o, ok := f.objects[key]
	return o, ok, nil
}

func (f *fakeS3) Upload(_ context.Context, _, key, localPath string, onBytes func(int64)) error {
	f.uploadCalls[key]++
	fi, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if f.uploadFails[key] > 0 {
		f.uploadFails[key]--
		if onBytes != nil && f.partial > 0 {
			onBytes(min(f.partial, fi.Size()))
		}
		return f.failWith
	}
	if onBytes != nil {
		onBytes(fi.Size())
	}
	f.objects[key] = s3store.Object{Key: key, Size: fi.Size(), ModTime: time.Now().UnixMilli()}
	return nil
}

func (f *fakeS3) Download(_ context.Context, _, key, localPath string, onBytes func(int64)) error {
	f.downloadCalls[key]++
	obj, ok := f.objects[key]
	if !ok {
		return os.ErrNotExist
	}
	if f.downloadFails[key] > 0 {
		f.downloadFails[key]--
		if onBytes != nil && f.partial > 0 {
			onBytes(min(f.partial, obj.Size))
		}
		return f.failWith
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
