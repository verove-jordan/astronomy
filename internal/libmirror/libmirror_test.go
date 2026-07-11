package libmirror

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsMasterFile(t *testing.T) {
	yes := []string{
		"master_BIAS_g0o0_b1.fits", "master_DARK_180000ms_g200o0_b1_-25C.fits",
		"master_FLAT_L_1000ms_g100o10_b1_0C.sig", "phone_master_DARK_iso3200_30000ms_4032x3024.fits",
		"phone_master_FLAT_iso100_4032x3024.sig",
		"master_DARK_180000ms_g200o0_b1_-25C_defects.lst", // bad-pixel-map sidecar rides the mirror
	}
	no := []string{"siril_cat_healpix8_astro.dat", "catalogues", "readme.txt", "master.txt", "notes.fits", "defects.lst"}
	for _, n := range yes {
		assert.True(t, IsMasterFile(n), "%s should be a master file", n)
	}
	for _, n := range no {
		assert.False(t, IsMasterFile(n), "%s should NOT be a master file", n)
	}
}

func TestKeyFor(t *testing.T) {
	lib := "/data/library"
	assert.Equal(t, "backups/library/master_BIAS_g0o0_b1.fits",
		KeyFor("backups", lib, "/data/library/master_BIAS_g0o0_b1.fits"))
	assert.Equal(t, "library/master_DARK.fits", // empty user prefix → just library/<file>
		KeyFor("", lib, "/data/library/master_DARK.fits"))
	// A path outside the library dir yields "" so callers skip it.
	assert.Equal(t, "", KeyFor("backups", lib, "/other/master_X.fits"))
	assert.Equal(t, "", KeyFor("backups", lib, lib)) // the dir itself, not a file
}

func TestMasterFiles_SkipsSubdirsAndNonMasters(t *testing.T) {
	lib := t.TempDir()
	// Flat masters + sidecars (mirrored) alongside noise + a big catalogues subdir (never mirrored).
	for _, n := range []string{
		"master_BIAS_g0o0_b1.fits", "master_BIAS_g0o0_b1.sig",
		"phone_master_DARK_iso3200_30000ms_4032x3024.fits", "notes.txt",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(lib, n), []byte("x"), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(lib, "catalogues"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lib, "catalogues", "siril_cat.dat"), []byte("big"), 0o644))

	files, err := MasterFiles(lib)
	require.NoError(t, err)
	var bases []string
	for _, f := range files {
		bases = append(bases, filepath.Base(f))
	}
	assert.Equal(t, []string{ // sorted by name; notes.txt + catalogues/ excluded
		"master_BIAS_g0o0_b1.fits", "master_BIAS_g0o0_b1.sig",
		"phone_master_DARK_iso3200_30000ms_4032x3024.fits",
	}, bases)
}

func TestMasterFiles_MissingDirIsEmpty(t *testing.T) {
	files, err := MasterFiles(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestNopPuller(t *testing.T) {
	var p Puller = Nop{}
	assert.NoError(t, p.Ensure(context.Background(), []string{"/x/master_A.fits"}))
	p.FreePulled(context.Background())
	assert.Nil(t, p.Notes())
}
