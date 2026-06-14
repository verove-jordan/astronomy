package inspect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

func TestClassifyImageType(t *testing.T) {
	cases := []struct {
		in   string
		want FrameType
	}{
		{"Light Frame", Light},
		{"LIGHT", Light},
		{"Dark", Dark},
		{"Dark Frame", Dark},
		{"Bias Frame", Bias},
		{"OFFSET", Bias},
		{"Flat Field", Flat},
		{"FlatDark", DarkFlat},
		{"Dark Flat", DarkFlat},
		{"weird", Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyImageType(tc.in))
		})
	}
}

func TestNormalizeFilter(t *testing.T) {
	cases := map[string]string{
		"Luminance": "L", "Red": "R", "green": "G", "Blue": "B",
		"H-alpha": "Ha", "ha": "Ha", "OIII": "OIII", "SII": "SII", "Custom": "Custom",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeFilter(in), in)
	}
}

func TestClassifyHeuristic(t *testing.T) {
	assert.Equal(t, Flat, classifyHeuristic(20000, 2000, 100), "bright frame is a flat")
	assert.Equal(t, Bias, classifyHeuristic(500, 100, 100), "shortest dim frame is bias")
	assert.Equal(t, Light, classifyHeuristic(800, 120000, 100), "long dim frame defaults to light")
}

func TestScan_ClassifiesAndGroups(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, imagetyp, filter, exptime string, pixel uint16) {
		cards := map[string]string{
			"GAIN": "139", "OFFSET": "21", "CCD-TEMP": "-15.0", "XBINNING": "1",
			"INSTRUME": "'ZWO ASI1600MM Pro'", "OBJECT": "'M31'",
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
	// Two Ha lights, one L light, one matching dark, one L flat, one bias.
	mk("ha_1.fits", "'Light Frame'", "'Ha'", "300.0", 1200)
	mk("ha_2.fits", "'Light Frame'", "'Ha'", "300.0", 1250)
	mk("l_1.fits", "'Light Frame'", "'L'", "120.0", 900)
	mk("dark_1.fits", "'Dark Frame'", "", "300.0", 600)
	mk("flat_l.fits", "'Flat Field'", "'L'", "2.0", 30000)
	mk("bias_1.fits", "'Bias Frame'", "", "0.0", 600)
	// A video file should be detected by extension.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "moon.ser"), []byte("fake"), 0o644))

	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)

	counts := inv.CountsByType()
	assert.Equal(t, 3, counts[Light])
	assert.Equal(t, 1, counts[Dark])
	assert.Equal(t, 1, counts[Flat])
	assert.Equal(t, 1, counts[Bias])
	assert.Len(t, inv.Videos, 1)

	assert.ElementsMatch(t, []string{"Ha", "L"}, inv.LightFilters())

	// The two Ha lights (same object/filter/exposure/gain/offset/temp) collapse into one set.
	var haSet *Set
	for i := range inv.Sets {
		if inv.Sets[i].Key.Type == Light && inv.Sets[i].Key.Filter == "Ha" {
			haSet = &inv.Sets[i]
		}
	}
	require.NotNil(t, haSet)
	assert.Equal(t, 2, haSet.Count)
	assert.Equal(t, int64(600_000), haSet.TotalIntegrationMs)
}
