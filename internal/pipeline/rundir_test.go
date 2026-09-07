package pipeline

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReserveRunDir_FirstCallerKeepsThePlainID(t *testing.T) {
	work := t.TempDir()

	id, dir, err := reserveRunDir(work, "20260907_101003")
	require.NoError(t, err)

	assert.Equal(t, "20260907_101003", id, "an uncontended run keeps its timestamp id unchanged")
	assert.Equal(t, filepath.Join(work, "run_20260907_101003"), dir)
	st, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, st.IsDir(), "creating the directory is what makes the claim atomic")
}

// Two runs started in the same second used to land on the same directory and overwrite each other's
// per-channel sequences.
func TestReserveRunDir_SameSecondRunsGetSeparateDirs(t *testing.T) {
	work := t.TempDir()
	const stamp = "20260907_101003"

	id1, dir1, err := reserveRunDir(work, stamp)
	require.NoError(t, err)
	id2, dir2, err := reserveRunDir(work, stamp)
	require.NoError(t, err)
	id3, dir3, err := reserveRunDir(work, stamp)
	require.NoError(t, err)

	assert.Equal(t, stamp, id1)
	assert.Equal(t, stamp+"_2", id2)
	assert.Equal(t, stamp+"_3", id3)
	assert.NotEqual(t, dir1, dir2)
	assert.NotEqual(t, dir2, dir3)
}

func TestReserveRunDir_ConcurrentCallersNeverShareADir(t *testing.T) {
	work := t.TempDir()
	const stamp = "20260907_101003"
	const n = 16

	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, dir, err := reserveRunDir(work, stamp)
			assert.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			assert.False(t, seen[dir], "two concurrent runs claimed the same scratch dir: %s", dir)
			seen[dir] = true
		}()
	}
	wg.Wait()
	assert.Len(t, seen, n)
}

func TestReserveRunDir_CreatesMissingWorkDir(t *testing.T) {
	work := filepath.Join(t.TempDir(), "not", "created", "yet")

	id, dir, err := reserveRunDir(work, "20260907_101003")
	require.NoError(t, err)

	assert.Equal(t, "20260907_101003", id)
	_, err = os.Stat(dir)
	assert.NoError(t, err)
}
