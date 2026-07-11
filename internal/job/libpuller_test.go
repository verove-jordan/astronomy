package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// fakeLibS3 serves a fixed set of mirror keys (key → bytes): Stat reports presence, Download writes them.
type fakeLibS3 struct {
	objects   map[string][]byte
	downloads int
}

func (f *fakeLibS3) Stat(_ context.Context, _, key string) (s3store.Object, bool, error) {
	_, ok := f.objects[key]
	return s3store.Object{}, ok, nil
}

func (f *fakeLibS3) Download(_ context.Context, _, key, localPath string, _ func(int64)) error {
	b, ok := f.objects[key]
	if !ok {
		return fmt.Errorf("no such key %q", key)
	}
	f.downloads++
	return os.WriteFile(localPath, b, 0o644)
}

func TestS3LibPuller_PullsMissingThenFrees(t *testing.T) {
	libDir := t.TempDir()
	// One master already local (must NOT be re-pulled), one only on the mirror (pulled), one nowhere (skipped).
	localMaster := filepath.Join(libDir, "master_BIAS_g0o0_b1.fits")
	require.NoError(t, os.WriteFile(localMaster, []byte("localbytes"), 0o644))
	remoteMaster := filepath.Join(libDir, "master_DARK_180000ms_g200o0_b1_-25C.fits")
	absentMaster := filepath.Join(libDir, "master_FLAT_L_1000ms_g100o10_b1_0C.fits")

	fake := &fakeLibS3{objects: map[string][]byte{
		"backups/library/master_BIAS_g0o0_b1.fits":                 []byte("MIRROR-should-not-download"),
		"backups/library/master_DARK_180000ms_g200o0_b1_-25C.fits": []byte("darkbytes"),
	}}
	p := &s3LibPuller{client: fake, bucket: "bkt", prefix: "backups", libDir: libDir}

	// The empty path and any non-library path are ignored; only real absent-locally masters are pulled.
	require.NoError(t, p.Ensure(context.Background(), []string{localMaster, remoteMaster, absentMaster, ""}))

	assert.Equal(t, 1, fake.downloads, "only the missing-but-mirrored master is downloaded")
	body, _ := os.ReadFile(remoteMaster)
	assert.Equal(t, "darkbytes", string(body), "the pulled master has the mirror's bytes")
	local, _ := os.ReadFile(localMaster)
	assert.Equal(t, "localbytes", string(local), "an already-local master is never overwritten")
	assert.NoFileExists(t, absentMaster, "a master absent from the mirror is left absent")

	// FreePulled removes ONLY the transiently-pulled master, keeping the pre-existing local one (mirror mode).
	p.FreePulled(context.Background())
	assert.NoFileExists(t, remoteMaster, "the pulled master is freed after the run")
	assert.FileExists(t, localMaster, "the pre-existing local master is kept")

	notes := p.Notes()
	require.NotEmpty(t, notes)
	assert.Contains(t, notes[0], "pulled 1")
}
