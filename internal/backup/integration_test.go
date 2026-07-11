package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// testClient builds an s3store client against a MinIO endpoint from the environment, or skips. Run with:
//
//	ASTRO_TEST_S3_ENDPOINT=localhost:9100 ASTRO_TEST_S3_KEY=minioadmin ASTRO_TEST_S3_SECRET=minioadmin \
//	  go test ./internal/backup/ -run Integration -v
func testClient(t *testing.T) *s3store.Client {
	t.Helper()
	ep := os.Getenv("ASTRO_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("set ASTRO_TEST_S3_ENDPOINT (+ _KEY/_SECRET) to run S3 integration tests")
	}
	c, err := s3store.New(s3store.Config{
		Endpoint:    ep,
		Region:      "us-east-1",
		AccessKeyID: os.Getenv("ASTRO_TEST_S3_KEY"),
		SecretKey:   os.Getenv("ASTRO_TEST_S3_SECRET"),
		UseSSL:      false,
	})
	require.NoError(t, err)
	return c
}

// Full snapshot → list → appstate-fetch → restore round-trip for the library, atlas and appstate components
// (no Postgres needed — the db component's pg_dump/pg_restore are validated by the end-to-end run).
func TestIntegration_SnapshotRestore(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	const bucket = "astro-test"
	require.NoError(t, client.EnsureBucket(ctx, bucket))

	// Source tree: a calibration library (with a nested master) + an LP atlas (bin + json).
	src := t.TempDir()
	libDir := filepath.Join(src, "library")
	require.NoError(t, os.MkdirAll(filepath.Join(libDir, "darks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(libDir, "master.fits"), []byte("MASTER"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(libDir, "darks", "d1.fits"), []byte("DARK1"), 0o644))
	atlasDir := filepath.Join(src, "lightpollution")
	require.NoError(t, os.MkdirAll(atlasDir, 0o755))
	atlasBin := filepath.Join(atlasDir, "atlas.bin")
	atlasJSON := filepath.Join(atlasDir, "atlas.json")
	require.NoError(t, os.WriteFile(atlasBin, []byte("ATLASBIN"), 0o644))
	require.NoError(t, os.WriteFile(atlasJSON, []byte(`{"grid":1}`), 0o644))

	cfg := Config{
		LibraryDir: libDir,
		AtlasBin:   atlasBin,
		AtlasJSON:  atlasJSON,
		WorkDir:    filepath.Join(src, "work"),
	}
	comps := []string{CompLibrary, CompAtlas, CompAppState}
	appstate := `{"version":1,"localStorage":{"astrostack.sky.favorites":"[\"M31\"]"}}`

	userPrefix := "itest/" + t.Name()
	stamp := "20260101T000000Z"
	keyPrefix := userPrefix + "/backup/" + stamp
	man := Manifest{StampMs: 1735689600000, Stamp: stamp}

	// Snapshot → all three components stored.
	got, err := Snapshot(ctx, client, bucket, keyPrefix, man, comps, appstate, cfg, nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, comps, got.Components)

	// List finds the manifest.
	mans, err := List(ctx, client, bucket, userPrefix)
	require.NoError(t, err)
	require.Len(t, mans, 1)
	assert.Equal(t, stamp, mans[0].Stamp)
	assert.ElementsMatch(t, comps, mans[0].Components)

	// AppState round-trips byte-for-byte.
	asData, err := AppState(ctx, client, bucket, keyPrefix)
	require.NoError(t, err)
	assert.JSONEq(t, appstate, string(asData))

	// Restore the file components into fresh dirs.
	dest := t.TempDir()
	rcfg := Config{
		LibraryDir: filepath.Join(dest, "library"),
		AtlasBin:   filepath.Join(dest, "lightpollution", "atlas.bin"),
		AtlasJSON:  filepath.Join(dest, "lightpollution", "atlas.json"),
		WorkDir:    filepath.Join(dest, "work"),
	}
	require.NoError(t, Restore(ctx, client, bucket, keyPrefix, []string{CompLibrary, CompAtlas}, rcfg, nil))

	// Library (incl. nested) + atlas came back intact.
	m, err := os.ReadFile(filepath.Join(rcfg.LibraryDir, "master.fits"))
	require.NoError(t, err)
	assert.Equal(t, "MASTER", string(m))
	d, err := os.ReadFile(filepath.Join(rcfg.LibraryDir, "darks", "d1.fits"))
	require.NoError(t, err)
	assert.Equal(t, "DARK1", string(d))
	ab, err := os.ReadFile(rcfg.AtlasBin)
	require.NoError(t, err)
	assert.Equal(t, "ATLASBIN", string(ab))
	aj, err := os.ReadFile(rcfg.AtlasJSON)
	require.NoError(t, err)
	assert.JSONEq(t, `{"grid":1}`, string(aj))

	// cleanup S3
	objs, _ := client.List(ctx, bucket, userPrefix)
	for _, o := range objs {
		_ = client.Delete(ctx, bucket, o.Key)
	}
}
