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
// meta overrides the Stat result for a key (e.g. an archived class) to exercise the thaw path.
type fakeLibS3 struct {
	objects   map[string][]byte
	meta      map[string]s3store.Object
	downloads int
	restores  []string
}

func (f *fakeLibS3) Stat(_ context.Context, _, key string) (s3store.Object, bool, error) {
	if o, ok := f.meta[key]; ok {
		return o, true, nil
	}
	_, ok := f.objects[key]
	return s3store.Object{}, ok, nil
}

func (f *fakeLibS3) Restore(_ context.Context, _, key string, _ int, _ s3store.RestoreTier) error {
	f.restores = append(f.restores, key)
	return nil
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

// An archived (Glacier) master is not downloaded — the puller kicks off its restore for a later run and
// falls back to the local rebuild this run (a run never blocks on a cold master).
func TestS3LibPuller_ArchivedMasterRestoresAndFallsBack(t *testing.T) {
	libDir := t.TempDir()
	coldMaster := filepath.Join(libDir, "master_DARK_180000ms_g200o0_b1_-25C.fits")
	key := "backups/library/master_DARK_180000ms_g200o0_b1_-25C.fits"
	fake := &fakeLibS3{
		objects: map[string][]byte{key: []byte("darkbytes")},
		meta:    map[string]s3store.Object{key: {Key: key, StorageClass: "GLACIER"}}, // archived, no restore
	}
	p := &s3LibPuller{client: fake, bucket: "bkt", prefix: "backups", libDir: libDir}

	require.NoError(t, p.Ensure(context.Background(), []string{coldMaster}))
	assert.Equal(t, 0, fake.downloads, "an archived master is not downloaded")
	assert.Equal(t, []string{key}, fake.restores, "its thaw is kicked off for a later run")
	assert.NoFileExists(t, coldMaster, "nothing landed locally — the caller falls back to rebuild")
	assert.NotEmpty(t, p.Notes(), "a warning explains the fallback")
}
