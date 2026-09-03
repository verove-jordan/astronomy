package inspect

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
				{exposureMs: 60000, peaks: 80, median: 1200, mad: 40, hasStats: true}, // light: stars
				{exposureMs: 1500, peaks: 0, median: 30000, mad: 300, hasStats: true}, // flat: bright + uniform
				{exposureMs: 0, peaks: 0, median: 300, mad: 5, hasStats: true},        // bias: ~0 exposure at floor
				{exposureMs: 60000, peaks: 2, median: 500, mad: 50, hasStats: true},   // dark: long, dim, starless
			},
			want: []FrameType{Light, Flat, Bias, Dark},
		},
		{
			name: "normalized [0,1] float frames",
			stats: []frameStat{
				{exposureMs: 120000, peaks: 60, median: 0.05, mad: 0.002, hasStats: true}, // light
				{exposureMs: 3000, peaks: 0, median: 0.6, mad: 0.01, hasStats: true},      // flat
				{exposureMs: 0, peaks: 0, median: 0.01, mad: 0.001, hasStats: true},       // bias
				{exposureMs: 120000, peaks: 1, median: 0.012, mad: 0.003, hasStats: true}, // dark (dim, long)
			},
			want: []FrameType{Light, Flat, Bias, Dark},
		},
		{
			name: "nebulosity light with sparse stars caught by brightFrac",
			stats: []frameStat{
				{exposureMs: 300000, peaks: 3, brightFrac: 0.05, median: 900, mad: 30, hasStats: true},   // faint Ha light
				{exposureMs: 300000, peaks: 1, brightFrac: 0.0002, median: 800, mad: 60, hasStats: true}, // dark
			},
			want: []FrameType{Light, Dark},
		},
		{
			// The container regression: a frame whose pixels could not be sampled (no sips on Linux)
			// has an all-zero curve that LOOKS like a bias at the dark floor. It must classify LIGHT —
			// never a calibration type — and its zero median must not drag the session floor down.
			name: "unreadable stats never classify as calibration",
			stats: []frameStat{
				{exposureMs: 0}, // e.g. a processed TIFF, stats unreadable
				{exposureMs: 60000, peaks: 80, median: 1200, mad: 40, hasStats: true}, // real light
				{exposureMs: 0, peaks: 0, median: 300, mad: 5, hasStats: true},        // real bias stays a bias
			},
			want: []FrameType{Light, Light, Bias},
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

// TestScan_ColorModel covers the four ways a capture can be colour and the two ways it can be mono,
// because "is this one-shot color?" used to be answerable only from a BAYERPAT card — so a developed
// DSLR raw, a debayered RGB FITS and a colour TIFF all read as monochrome and were stacked as
// luminance (or, in the web UI, silently dropped).
func TestScan_ColorModel(t *testing.T) {
	tests := []struct {
		name      string
		seed      func(t *testing.T, dir string)
		want      ColorModel
		wantLight int // lights the scan must surface
	}{
		{
			name: "mono filter wheel",
			seed: func(t *testing.T, dir string) {
				fitstest.Write(t, dir, "l.fits", 8, 8, 1200, map[string]string{"IMAGETYP": "'Light'", "FILTER": "'L'"})
				fitstest.Write(t, dir, "r.fits", 8, 8, 1200, map[string]string{"IMAGETYP": "'Light'", "FILTER": "'R'"})
			},
			want: ColorMono, wantLight: 2,
		},
		{
			name: "Bayer CFA FITS",
			seed: func(t *testing.T, dir string) {
				fitstest.Write(t, dir, "a.fits", 8, 8, 1200, map[string]string{"IMAGETYP": "'Light'", "BAYERPAT": "'RGGB'"})
			},
			want: ColorOSC, wantLight: 1,
		},
		{
			name: "already-debayered RGB FITS carries no BAYERPAT",
			seed: func(t *testing.T, dir string) {
				fitstest.WriteRGB(t, dir, "rgb.fits", 8, 8, 1200, 1100, 900, map[string]string{"IMAGETYP": "'Light'"})
			},
			want: ColorOSC, wantLight: 1,
		},
		{
			name: "colour TIFF still",
			seed: func(t *testing.T, dir string) { writeTestTIFF(t, filepath.Join(dir, "light_0001.tif"), true) },
			want: ColorOSC, wantLight: 1,
		},
		{
			name: "mono TIFF still keeps the mono path",
			seed: func(t *testing.T, dir string) { writeTestTIFF(t, filepath.Join(dir, "light_0001.tif"), false) },
			want: ColorMono, wantLight: 1,
		},
		{
			name: "colour JPEGs are ingested, not ignored",
			seed: func(t *testing.T, dir string) {
				writeTestJPEG(t, filepath.Join(dir, "light_0001.jpg"))
				writeTestJPEG(t, filepath.Join(dir, "light_0002.jpg"))
			},
			want: ColorOSC, wantLight: 2,
		},
		{
			name: "mono lights beside colour lights is mixed",
			seed: func(t *testing.T, dir string) {
				fitstest.Write(t, dir, "l.fits", 8, 8, 1200, map[string]string{"IMAGETYP": "'Light'", "FILTER": "'L'"})
				fitstest.WriteRGB(t, dir, "rgb.fits", 8, 8, 1200, 1100, 900, map[string]string{"IMAGETYP": "'Light'"})
			},
			want: ColorMixed, wantLight: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.seed(t, dir)
			inv, err := Scan(context.Background(), dir)
			require.NoError(t, err)
			assert.Equal(t, tt.want, inv.ColorModel)
			var lights int
			for _, fr := range inv.Frames {
				if fr.Type == Light {
					lights++
				}
			}
			assert.Equal(t, tt.wantLight, lights, "lights surfaced by the scan")
		})
	}
}

// TestScan_RawsBesideCalibrationFITS pins the promotion guard: a DSLR session that keeps its darks as
// FITS beside the NEFs used to make every raw invisible, because a single non-light FITS was enough
// to suppress the whole camera-raw promotion.
func TestScan_RawsBesideCalibrationFITS(t *testing.T) {
	dir := t.TempDir()
	fitstest.Write(t, dir, "dark_0.fits", 8, 8, 800, map[string]string{"IMAGETYP": "'Dark'"})
	// A .dng we cannot develop in a unit test still classifies by name/extension as a light.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DSC_0001.dng"), []byte("not a real raw"), 0o644))

	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)
	var lights, darks int
	for _, fr := range inv.Frames {
		switch fr.Type {
		case Light:
			lights++
		case Dark:
			darks++
		}
	}
	assert.Equal(t, 1, lights, "the raw must be promoted even though a calibration FITS is present")
	assert.Equal(t, 1, darks)
	assert.Equal(t, ColorOSC, inv.ColorModel)
}

// TestSetKey_ColorSeparatesMixedSets guards the grouping dimension: a mono and a colour light at the
// same exposure/gain/temperature must never land in one set, because nothing downstream could stack
// them together or calibrate one with the other's master.
func TestSetKey_ColorSeparatesMixedSets(t *testing.T) {
	dir := t.TempDir()
	cards := map[string]string{"IMAGETYP": "'Light'", "GAIN": "139", "OFFSET": "21", "EXPOINUS": "60000000"}
	fitstest.Write(t, dir, "mono.fits", 8, 8, 1200, cards)
	fitstest.WriteRGB(t, dir, "colour.fits", 8, 8, 1200, 1100, 900, cards)

	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)
	sets := inv.SetsOfType(Light)
	require.Len(t, sets, 2, "identical exposure/gain but different colour models must not merge")
	assert.NotEqual(t, sets[0].Key.Color, sets[1].Key.Color)
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

// backfillMeta must name a FLAT's filter from the EFW "(Alias: L)" sidecar once the folder token settled
// its type — per-filter SharpCap flats otherwise collapse into one Filter="" set (one merged cross-filter
// master flat). Darks keep Filter="" (their SetKey ignores it) and pre-classification lights are left for
// nameRemainingWheelSlots.
func TestBackfillMeta_FlatFilterFromSlotAlias(t *testing.T) {
	dir := t.TempDir()
	frameAt := func(sub, name, sidecar string) *Frame {
		d := filepath.Join(dir, sub)
		require.NoError(t, os.MkdirAll(d, 0o755))
		p := filepath.Join(d, name)
		require.NoError(t, os.WriteFile(p, []byte("tiff"), 0o644))
		require.NoError(t, os.WriteFile(p+".txt", []byte(sidecar), 0o644))
		fr := &Frame{Path: p, Type: Unknown, ClassSource: SourceExtension}
		backfillMeta(fr, p)
		return fr
	}

	t.Run("flat gains the alias filter", func(t *testing.T) {
		fr := frameAt("flats", "cap_0001.tif", "EFW Slot = 1(Alias: L)\nExposure = 10ms\nGain = 0\n")
		require.Equal(t, Flat, fr.Type)
		assert.Equal(t, "L", fr.Filter)
	})
	t.Run("dark ignores the alias", func(t *testing.T) {
		fr := frameAt("darks", "cap_0001.tif", "EFW Slot = 5(Alias: Ha)\nExposure = 10ms\nGain = 0\n")
		require.Equal(t, Dark, fr.Type)
		assert.Empty(t, fr.Filter, "dark SetKey ignores filter — alias must not name it")
	})
	t.Run("unclassified light is left for the wheel pass", func(t *testing.T) {
		fr := frameAt("session1", "cap_0001.tif", "EFW Slot = 1(Alias: L)\nExposure = 10ms\nGain = 0\n")
		require.Equal(t, Unknown, fr.Type)
		assert.Empty(t, fr.Filter, "lights are named by nameRemainingWheelSlots, not the backfill")
	})
	t.Run("filename filter wins over the alias", func(t *testing.T) {
		fr := frameAt("flats", "filter_R_0001.tif", "EFW Slot = 1(Alias: L)\n")
		require.Equal(t, Flat, fr.Type)
		assert.Equal(t, "R", fr.Filter, "backfill only ever fills blanks")
	})
}

// TestScan_ProcessedLeftoversBesideRaws: exports and live-stack saves living INSIDE the capture tree
// (Autosave001.tif, m33_L_v2.tif — the M33 2019 session) must never become stackable LIGHT frames:
// as zero-metadata "lights" they form a phantom group that fails its whole channel. The name veto
// catches Autosave* (trailing copy counter stripped); a leftover the veto misses is dropped by the
// no-metadata + unreadable-pixels rule. A REAL headerless TIF capture keeps its sidecar exposure and
// must survive as a Light.
func TestScan_ProcessedLeftoversBesideRaws(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "L", "2019-08-26_00_32_30Z")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	fitstest.Write(t, filepath.Join(dir, "L"), "capture_0001.fits", 8, 8, 2400, map[string]string{
		"IMAGETYP": "'Light'", "FILTER": "'L'", "EXPOINUS": "120000000", "GAIN": "300"})
	// Name-vetoed live-stack save (copy counter after the token).
	require.NoError(t, os.WriteFile(filepath.Join(sub, "Autosave001.tif"), []byte("not a tiff"), 0o644))
	// A hand-exported leftover the name veto cannot catch: no metadata, unreadable pixels.
	require.NoError(t, os.WriteFile(filepath.Join(sub, "m33_L_v2.tif"), []byte("not a tiff"), 0o644))
	// A real headerless TIF capture: pixels equally unreadable, but the SharpCap sidecar carries the
	// exposure — it must stay a Light (the Linux-engine path for TIF captures).
	require.NoError(t, os.WriteFile(filepath.Join(sub, "tifcap_0001.tif"), []byte("not a tiff"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "tifcap_0001.tif.txt"),
		[]byte("Exposure = 30s\nGain = 300\n"), 0o644))

	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)

	var lights []string
	for _, s := range inv.SetsOfType(Light) {
		for _, fr := range s.Frames {
			lights = append(lights, filepath.Base(fr.Path))
		}
	}
	assert.ElementsMatch(t, []string{"capture_0001.fits", "tifcap_0001.tif"}, lights,
		"only real captures may enter stackable light sets")
	joined := strings.Join(inv.Warnings, "\n")
	assert.Contains(t, joined, "skipped as a processed leftover", "the drop must be surfaced")
	for _, fr := range inv.Frames {
		assert.NotContains(t, filepath.Base(fr.Path), "Autosave", "name-vetoed files are never ingested")
	}
}
