package backup

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTarRoundTrip(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.fits"), []byte("alpha"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "b.fits"), []byte("bravo"), 0o644))

	tarPath := filepath.Join(t.TempDir(), "lib.tar")
	n, err := tarDir(src, tarPath)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	dest := t.TempDir()
	require.NoError(t, untar(tarPath, dest))

	got, err := os.ReadFile(filepath.Join(dest, "a.fits"))
	require.NoError(t, err)
	assert.Equal(t, "alpha", string(got))
	got, err = os.ReadFile(filepath.Join(dest, "sub", "b.fits"))
	require.NoError(t, err)
	assert.Equal(t, "bravo", string(got))
}

// An absent source is "nothing to back up", not an error — the library component simply gets skipped.
func TestTarDir_MissingSourceIsEmpty(t *testing.T) {
	n, err := tarDir(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "x.tar"))
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// untar must refuse a member whose path escapes the destination (zip-slip) and write nothing outside it.
func TestUntar_RejectsPathEscape(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "evil.tar")
	f, err := os.Create(tarPath)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	body := []byte("pwned")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "../evil.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())

	dest := t.TempDir()
	err = untar(tarPath, dest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "escapes")

	_, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt"))
	assert.True(t, os.IsNotExist(statErr), "nothing written outside the destination")
}
