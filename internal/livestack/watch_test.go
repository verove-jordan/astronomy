package livestack

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/source"
)

// fakeSource is a deterministic in-memory Source: the test sets objs before each poll and inspects
// which keys were fetched.
type fakeSource struct {
	objs    []source.Object
	fetched []string
}

func (f *fakeSource) List(context.Context) ([]source.Object, error) { return f.objs, nil }
func (f *fakeSource) Fetch(_ context.Context, o source.Object) (string, error) {
	f.fetched = append(f.fetched, o.Key)
	return "/local/" + o.Key, nil
}
func (f *fakeSource) LocalRoot() string { return "/local" }
func (f *fakeSource) Close() error      { return nil }

func TestWatcher_Poll_SizeStabilityGate(t *testing.T) {
	fs := &fakeSource{}
	w := newWatcher(fs, 10_000) // 10s window so the mtime gate never clears a fresh file
	ctx := context.Background()

	// First sight of a file: not ingested (its size has not yet been confirmed stable).
	fs.objs = []source.Object{{Key: "a.fits", Size: 100, ModTime: 0}}
	ready, err := w.poll(ctx, 100)
	require.NoError(t, err)
	assert.Empty(t, ready)

	// Same size next poll → stable → ingested exactly once.
	ready, err = w.poll(ctx, 200)
	require.NoError(t, err)
	assert.Equal(t, []string{"/local/a.fits"}, ready)

	// Already fetched → never returned again.
	ready, err = w.poll(ctx, 300)
	require.NoError(t, err)
	assert.Empty(t, ready)
	assert.Equal(t, []string{"a.fits"}, fs.fetched)
}

func TestWatcher_Poll_GrowingFileWaits(t *testing.T) {
	fs := &fakeSource{}
	w := newWatcher(fs, 10_000)
	ctx := context.Background()

	fs.objs = []source.Object{{Key: "g.fits", Size: 100, ModTime: 0}}
	ready, _ := w.poll(ctx, 1)
	assert.Empty(t, ready)

	fs.objs = []source.Object{{Key: "g.fits", Size: 250, ModTime: 0}} // still being written
	ready, _ = w.poll(ctx, 2)
	assert.Empty(t, ready, "a file whose size changed must not be ingested")

	fs.objs = []source.Object{{Key: "g.fits", Size: 250, ModTime: 0}} // settled
	ready, _ = w.poll(ctx, 3)
	assert.Equal(t, []string{"/local/g.fits"}, ready)
}

func TestWatcher_Poll_TimeGateIngestsImmediately(t *testing.T) {
	// stabilityMs 0 models an atomic source (S3): an object is ingested on first sight.
	fs := &fakeSource{objs: []source.Object{{Key: "obj.fits", Size: 100, ModTime: 0}}}
	w := newWatcher(fs, 0)

	ready, err := w.poll(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"/local/obj.fits"}, ready)
}
