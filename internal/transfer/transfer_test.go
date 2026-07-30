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

	files, total, err := walkLocalFiles(dir, nil, false)
	require.NoError(t, err)
	assert.Len(t, files, 2, "dotfiles and .part temps are skipped")
	assert.Equal(t, int64(8), total, "5 + 3 bytes")
}

func TestWalkLocalFiles_MissingDirIsEmpty(t *testing.T) {
	files, total, err := walkLocalFiles(filepath.Join(t.TempDir(), "nope"), nil, false)
	require.NoError(t, err)
	assert.Empty(t, files)
	assert.Zero(t, total)
}

func TestWalkLocalFiles_ExcludeDirs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "master_A.fits"), []byte("12345"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "catalogues"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "catalogues", "big.dat"), []byte("huge"), 0o644))

	files, total, err := walkLocalFiles(dir, []string{"catalogues"}, false)
	require.NoError(t, err)
	assert.Len(t, files, 1, "the excluded catalogues/ subtree is skipped")
	assert.Equal(t, int64(5), total, "only master_A.fits counts")
}

// TestWalkLocalFiles_SkipSymlinks pins the WorkDir-copy guard: a symlink to a large file is included (with
// its tiny Lstat size) when skipSymlinks is off, and dropped — bytes and all — when it is on. A symlinked
// DIRECTORY is likewise never descended nor listed when skipping.
func TestWalkLocalFiles_SkipSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.fits")
	require.NoError(t, os.WriteFile(target, []byte("0123456789"), 0o644)) // 10 bytes
	link := filepath.Join(dir, "link.fits")
	require.NoError(t, os.Symlink(target, link))
	// A symlinked subdirectory (Siril `convert` style) pointing outside the walked tree.
	extern := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(extern, "frame.fits"), []byte("abcd"), 0o644))
	require.NoError(t, os.Symlink(extern, filepath.Join(dir, "frames")))

	kept, _, err := walkLocalFiles(dir, nil, false)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range kept {
		names[filepath.Base(f.path)] = true
	}
	assert.True(t, names["link.fits"], "symlink included when not skipping")

	skipped, total, err := walkLocalFiles(dir, nil, true)
	require.NoError(t, err)
	for _, f := range skipped {
		assert.NotEqual(t, "link.fits", filepath.Base(f.path), "file symlink dropped when skipping")
	}
	assert.Len(t, skipped, 1, "only the real file remains (both symlinks dropped)")
	assert.Equal(t, int64(10), total, "only real.fits bytes counted; the symlinked dir is not descended")
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
