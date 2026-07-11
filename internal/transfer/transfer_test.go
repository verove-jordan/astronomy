package transfer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testReq() Request {
	return Request{LocalRoot: "/data", RelPath: "M101", Bucket: "b", KeyPrefix: "acct/data"}
}

func TestRequest_KeyFor(t *testing.T) {
	r := testReq()
	tests := []struct {
		local string
		want  string
	}{
		{"/data/M101/x.fits", "acct/data/M101/x.fits"},
		{"/data/M101/lights/y.fits", "acct/data/M101/lights/y.fits"},
	}
	for _, tt := range tests {
		got, err := r.keyFor(tt.local)
		require.NoError(t, err)
		assert.Equal(t, tt.want, got)
	}
}

func TestRequest_LocalFor_RoundTrip(t *testing.T) {
	r := testReq()
	key := "acct/data/M101/lights/y.fits"
	local, err := r.localFor(key)
	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/data/M101/lights/y.fits"), local)
	// round-trips back to the same key
	back, err := r.keyFor(local)
	require.NoError(t, err)
	assert.Equal(t, key, back)
}

func TestRequest_LocalFor_RejectsEscape(t *testing.T) {
	r := testReq()
	_, err := r.localFor("acct/data/M101/../../etc/passwd")
	assert.Error(t, err, "a key escaping the folder must be rejected")
}

func TestWalkLocalFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lights"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.fits"), []byte("12345"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lights", "b.fits"), []byte("678"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644))     // skipped
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.fits.part"), []byte("x"), 0o644)) // skipped

	files, total, err := walkLocalFiles(dir, nil)
	require.NoError(t, err)
	assert.Len(t, files, 2, "dotfiles and .part temps are skipped")
	assert.Equal(t, int64(8), total, "5 + 3 bytes")
}

func TestWalkLocalFiles_MissingDirIsEmpty(t *testing.T) {
	files, total, err := walkLocalFiles(filepath.Join(t.TempDir(), "nope"), nil)
	require.NoError(t, err)
	assert.Empty(t, files)
	assert.Zero(t, total)
}

func TestWalkLocalFiles_ExcludeDirs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "master_A.fits"), []byte("12345"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "catalogues"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "catalogues", "big.dat"), []byte("huge"), 0o644))

	files, total, err := walkLocalFiles(dir, []string{"catalogues"})
	require.NoError(t, err)
	assert.Len(t, files, 1, "the excluded catalogues/ subtree is skipped")
	assert.Equal(t, int64(5), total, "only master_A.fits counts")
}

func TestRemoveEmptyDirs(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "c"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "c", "keep.txt"), []byte("x"), 0o644))
	removeEmptyDirs(root)
	assert.NoDirExists(t, filepath.Join(root, "a", "b"))
	assert.NoDirExists(t, filepath.Join(root, "a"))
	assert.DirExists(t, filepath.Join(root, "c"), "a non-empty dir is kept")
	assert.DirExists(t, root, "root itself is kept")
}
