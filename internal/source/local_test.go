package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalSource_List_FiltersAndRecurses(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, data, 0o644))
	}
	write("a.fits", []byte("0123456789")) // 10 bytes
	write("b.fit", []byte("ab"))          // 2 bytes
	write("notes.txt", []byte("ignore"))  // not a frame
	write("sub/c.fits", []byte("xyz"))    // 3 bytes, nested

	src, err := NewLocal(dir)
	require.NoError(t, err)

	objs, err := src.List(context.Background())
	require.NoError(t, err)

	sizes := map[string]int64{}
	for _, o := range objs {
		assert.True(t, filepath.IsAbs(o.Key), "key must be absolute: %s", o.Key)
		assert.Positive(t, o.ModTime)
		sizes[filepath.Base(o.Key)] = o.Size
	}
	assert.Len(t, objs, 3)
	assert.Equal(t, int64(10), sizes["a.fits"])
	assert.Equal(t, int64(2), sizes["b.fit"])
	assert.Equal(t, int64(3), sizes["c.fits"])
	assert.NotContains(t, sizes, "notes.txt")
}

func TestLocalSource_Fetch_ReturnsPathUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.fits")
	require.NoError(t, os.WriteFile(p, []byte("data"), 0o644))

	src, err := NewLocal(dir)
	require.NoError(t, err)

	got, err := src.Fetch(context.Background(), Object{Key: p, Size: 4})
	require.NoError(t, err)
	assert.Equal(t, p, got)
	assert.Equal(t, dir, src.LocalRoot())
}

func TestLocalSource_List_MissingDirIsEmpty(t *testing.T) {
	src, err := NewLocal(filepath.Join(t.TempDir(), "not-created-yet"))
	require.NoError(t, err)

	objs, err := src.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, objs)
}
