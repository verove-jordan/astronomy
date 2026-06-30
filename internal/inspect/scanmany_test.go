package inspect

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

// writeFrame writes one test FITS frame into dir with the common ASI cards plus the given type/filter.
func writeFrame(t *testing.T, dir, name, imagetyp, filter, exptime string, pixel uint16) {
	t.Helper()
	cards := map[string]string{
		"GAIN": "139", "OFFSET": "21", "CCD-TEMP": "-15.0", "XBINNING": "1", "OBJECT": "'M31'",
	}
	if imagetyp != "" {
		cards["IMAGETYP"] = imagetyp
	}
	if filter != "" {
		cards["FILTER"] = filter
	}
	if exptime != "" {
		cards["EXPTIME"] = exptime
	}
	fitstest.Write(t, dir, name, 8, 8, pixel, cards)
}

// Lights in one folder and their calibration in another must merge into one complete inventory: the
// union counts every frame and emits no missing-calibration warning (the cross-folder multi-select).
func TestScanMany_MergesRootsAndFixesCrossDirWarnings(t *testing.T) {
	lights := t.TempDir()
	writeFrame(t, lights, "l_1.fits", "'Light Frame'", "'L'", "120.0", 900)
	writeFrame(t, lights, "l_2.fits", "'Light Frame'", "'L'", "120.0", 920)

	calib := t.TempDir() // matching darks/flats/bias live in a *different* parent folder
	writeFrame(t, calib, "dark_1.fits", "'Dark Frame'", "", "120.0", 600)
	writeFrame(t, calib, "flat_l.fits", "'Flat Field'", "'L'", "2.0", 30000)
	writeFrame(t, calib, "bias_1.fits", "'Bias Frame'", "", "0.0", 600)

	// The lights folder alone warns about missing calibration.
	solo, err := Scan(context.Background(), lights)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(solo.Warnings, "\n"), "no darks found")

	// Merged with the calibration folder, the union is complete: no missing-calibration warnings.
	merged, err := ScanMany(context.Background(), []string{lights, calib}, DefaultScanOptions())
	require.NoError(t, err)

	counts := merged.CountsByType()
	assert.Equal(t, 2, counts[Light])
	assert.Equal(t, 1, counts[Dark])
	assert.Equal(t, 1, counts[Flat])
	assert.Equal(t, 1, counts[Bias])

	joined := strings.Join(merged.Warnings, "\n")
	assert.NotContains(t, joined, "no darks found")
	assert.NotContains(t, joined, "no flats found")
}

// A single root through ScanMany must be byte-identical to Scan — the single-folder path is unchanged.
func TestScanMany_SingleRootMatchesScan(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "l_1.fits", "'Light Frame'", "'L'", "120.0", 900)
	writeFrame(t, dir, "dark_1.fits", "'Dark Frame'", "", "120.0", 600)

	one, err := Scan(context.Background(), dir)
	require.NoError(t, err)
	many, err := ScanMany(context.Background(), []string{dir}, DefaultScanOptions())
	require.NoError(t, err)

	assert.Equal(t, one.Root, many.Root)
	assert.Equal(t, one.CountsByType(), many.CountsByType())
	assert.Equal(t, len(one.Sets), len(many.Sets))
	assert.Equal(t, one.Warnings, many.Warnings)
}

// ScanMany with no roots is an error, not a silent empty inventory.
func TestScanMany_NoRoots(t *testing.T) {
	_, err := ScanMany(context.Background(), nil, DefaultScanOptions())
	require.Error(t, err)
}

func TestCommonParent(t *testing.T) {
	cases := []struct {
		name  string
		roots []string
		want  string
	}{
		{"siblings", []string{"/a/b/c", "/a/b/d"}, "/a/b"},
		{"nested", []string{"/a/b", "/a/b/c"}, "/a/b"},
		{"single", []string{"/a/b/c"}, "/a/b/c"},
		{"only root in common", []string{"/a/x", "/b/y"}, "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, commonParent(tc.roots))
		})
	}
}
