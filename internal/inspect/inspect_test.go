package inspect

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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

func TestClassifyByStats(t *testing.T) {
	// One mixed session compared against itself: floor = the dimmest median (the bias). Each frame's
	// curve resolves to a distinct type — crucially including Dark, which the old heuristic never emitted.
	tests := []struct {
		name  string
		stats []frameStat
		want  []FrameType
	}{
		{
			name: "16-bit integer LRGB session",
			stats: []frameStat{
				{exposureMs: 60000, peaks: 80, median: 1200, mad: 40},  // light: stars
				{exposureMs: 1500, peaks: 0, median: 30000, mad: 300},  // flat: bright + uniform
				{exposureMs: 0, peaks: 0, median: 300, mad: 5},         // bias: ~0 exposure at floor
				{exposureMs: 60000, peaks: 2, median: 500, mad: 50},    // dark: long, dim, starless
			},
			want: []FrameType{Light, Flat, Bias, Dark},
		},
		{
			name: "normalized [0,1] float frames",
			stats: []frameStat{
				{exposureMs: 120000, peaks: 60, median: 0.05, mad: 0.002}, // light
				{exposureMs: 3000, peaks: 0, median: 0.6, mad: 0.01},      // flat
				{exposureMs: 0, peaks: 0, median: 0.01, mad: 0.001},       // bias
				{exposureMs: 120000, peaks: 1, median: 0.012, mad: 0.003}, // dark (dim, long)
			},
			want: []FrameType{Light, Flat, Bias, Dark},
		},
		{
			name: "nebulosity light with sparse stars caught by brightFrac",
			stats: []frameStat{
				{exposureMs: 300000, peaks: 3, brightFrac: 0.05, median: 900, mad: 30}, // faint Ha light
				{exposureMs: 300000, peaks: 1, brightFrac: 0.0002, median: 800, mad: 60}, // dark
			},
			want: []FrameType{Light, Dark},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyByStats(tt.stats))
		})
	}
}

func TestScan_EXPOINUSExposureAndBayerOSC(t *testing.T) {
	dir := t.TempDir()
	// Older ASICAP one-shot-color capture: exposure recorded only in EXPOINUS (µs), a CFA pattern, and
	// no IMAGETYP/FILTER. Without the EXPOINUS fallback this reads as exposure 0 and is mis-typed as bias.
	fitstest.Write(t, dir, "osc_0001.fits", 8, 8, 2400, map[string]string{
		"GAIN": "200", "BAYERPAT": "'GRBG'", "EXPOINUS": "90000000",
	})

	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, inv.Frames, 1)
	fr := inv.Frames[0]
	assert.Equal(t, int64(90000), fr.ExposureMs, "EXPOINUS µs → 90 s, so it is a light not a bias")
	assert.Equal(t, Light, fr.Type)
	assert.Equal(t, "GRBG", fr.Bayer)

	// The mono per-filter pipeline drops one-shot-color frames.
	removed := inv.ExcludeBayer()
	assert.Equal(t, 1, removed)
	assert.Empty(t, inv.Frames)
	assert.Empty(t, inv.SetsOfType(Light))
}

// TestScan_SpuriousBayerOnMonoRig reproduces the older-ASICAP capture of a MONO camera (ASI 1600MM Pro):
// every frame carries a BAYERPAT card, yet the lights are shot through a filter wheel (L/R/G/B/Ha). The
// CFA card is spurious — without the veto the whole session is dropped as one-shot-color and the job
// "succeeds" instantly with no channels. The filter wheel proves the rig is mono, so Bayer must be cleared
// on the filtered lights AND their calibration frames; ExcludeBayer must then drop nothing.
func TestScan_SpuriousBayerOnMonoRig(t *testing.T) {
	dir := t.TempDir()
	const bayer = "'GRBG'"
	// Filtered lights (a filter wheel ⇒ mono rig), each tagged with the bogus CFA pattern.
	for i, filt := range []string{"'L'", "'R'", "'G'", "'B'", "'Ha'"} {
		fitstest.Write(t, dir, "light_"+strconv.Itoa(i)+".fits", 8, 8, 2400,
			map[string]string{"IMAGETYP": "'Light'", "FILTER": filt, "BAYERPAT": bayer, "EXPOINUS": "120000000"})
	}
	// Calibration frames carry no filter but belong to the same mono rig — they too must be un-Bayered.
	fitstest.Write(t, dir, "dark_0.fits", 8, 8, 800,
		map[string]string{"IMAGETYP": "'Dark'", "BAYERPAT": bayer, "EXPOINUS": "120000000"})
	fitstest.Write(t, dir, "bias_0.fits", 8, 8, 500,
		map[string]string{"IMAGETYP": "'Bias'", "BAYERPAT": bayer, "EXPOINUS": "1000"})

	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, inv.Frames, 7)
	for _, fr := range inv.Frames {
		assert.Empty(t, fr.Bayer, "%s: spurious BAYERPAT must be cleared on a filter-wheel (mono) session", filepath.Base(fr.Path))
	}
	assert.Zero(t, inv.ExcludeBayer(), "no frame should be dropped as one-shot-color")
	assert.NotEmpty(t, inv.SetsOfType(Light), "the mono lights must survive into stackable sets")
}

func TestIsOSCDir(t *testing.T) {
	t.Run("all CFA is OSC", func(t *testing.T) {
		dir := t.TempDir()
		fitstest.Write(t, dir, "a.fits", 8, 8, 1000, map[string]string{"BAYERPAT": "'GRBG'"})
		fitstest.Write(t, dir, "b.fits", 8, 8, 1000, map[string]string{"BAYERPAT": "'GRBG'"})
		assert.True(t, IsOSCDir(dir))
		got, err := ListFITSFrames(dir)
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})
	t.Run("mono is not OSC", func(t *testing.T) {
		dir := t.TempDir()
		fitstest.Write(t, dir, "m.fits", 8, 8, 1000, map[string]string{"FILTER": "'L'"})
		assert.False(t, IsOSCDir(dir))
	})
	t.Run("mixed mono+CFA is not OSC", func(t *testing.T) {
		dir := t.TempDir()
		fitstest.Write(t, dir, "a_osc.fits", 8, 8, 1000, map[string]string{"BAYERPAT": "'GRBG'"})
		fitstest.Write(t, dir, "b_mono.fits", 8, 8, 1000, map[string]string{"FILTER": "'L'"})
		assert.False(t, IsOSCDir(dir))
	})
	t.Run("empty dir is not OSC", func(t *testing.T) {
		assert.False(t, IsOSCDir(t.TempDir()))
	})
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
