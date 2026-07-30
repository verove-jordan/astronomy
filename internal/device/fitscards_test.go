package device

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// The contract test: a frame written by the capture subsystem must be classified CORRECTLY by the
// very scanner the processing pipeline uses. Everything about a run — which darks match, which
// night a frame belongs to, whether the ZWO gain law applies — is decided from these fields, and a
// mistake degrades silently. If this test fails, captures are landing in the wrong sets.
func TestFrameMeta_RoundTripsThroughInspect(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 7, 27, 22, 14, 3, 250*int(time.Millisecond), time.Local)

	meta := FrameMeta{
		Type: "light", Filter: "Ha",
		ExposureUs: 120_000_000, Gain: 139, Offset: 50, Bin: 1,
		TempMilliC: -15200, HasTemp: true,
		TargetTempC: -15, HasTargetTemp: true,
		Object: "M31", Instrume: "ZWO ASI1600MM Pro", Telescop: "FC-100DF",
		FocalLenMM: 740, PixelSizeUm: 3.8,
		RADeg: 10.6847, DecDeg: 41.2687, HasCoord: true,
		Panel: "p03", StartedAt: started,
	}
	name := meta.FileName(1)
	path := filepath.Join(dir, name)
	pix := make([]uint16, 16*16)
	require.NoError(t, fits.Write16(path, 16, 16, pix, meta.Cards()))

	inv, err := inspect.Scan(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, inv.Frames, 1, "the capture file must not be vetoed as a processed output")
	fr := inv.Frames[0]

	assert.Equal(t, inspect.Light, fr.Type)
	assert.Equal(t, "Ha", fr.Filter)
	assert.Equal(t, int64(120000), fr.ExposureMs)
	assert.Equal(t, int64(139), fr.Gain)
	assert.True(t, fr.HasGain, "HasGain gates the ZWO gain law in photometric normalisation")
	assert.Equal(t, int64(50), fr.Offset)
	assert.Equal(t, 1, fr.BinX)
	assert.True(t, fr.HasTemp)
	assert.Equal(t, int64(-15200), fr.TempMilliC)
	assert.Equal(t, "M31", fr.Object)
	assert.Equal(t, "ZWO ASI1600MM Pro", fr.Instrument)
	assert.Equal(t, "astrostack", fr.Creator, "the software tag is SWCREATE, not CREATOR")
	assert.InDelta(t, 740, fr.FocalLenMM, 1e-9)
	assert.InDelta(t, 3.8, fr.PixelSizeUm, 1e-9)
	assert.NotEmpty(t, fr.ObjCtRA)
	assert.NotEmpty(t, fr.ObjCtDec)

	// The night key is the whole point of a naive DATE-OBS: a "Z" suffix makes this empty and
	// silently breaks sessionization, per-night flats and calibration reuse.
	assert.Equal(t, "2026-07-27", fr.Session)
	assert.NotZero(t, fr.DateObsMs)
}

func TestFrameMeta_CalibrationTypesClassify(t *testing.T) {
	tests := []struct {
		kind string
		want inspect.FrameType
	}{
		{"light", inspect.Light},
		{"dark", inspect.Dark},
		{"flat", inspect.Flat},
		{"bias", inspect.Bias},
		{"darkflat", inspect.DarkFlat},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			dir := t.TempDir()
			meta := FrameMeta{
				Type: tt.kind, ExposureUs: 1_000_000, Gain: 100, Bin: 1,
				StartedAt: time.Date(2026, 7, 27, 22, 0, 0, 0, time.Local),
			}
			path := filepath.Join(dir, meta.FileName(1))
			require.NoError(t, fits.Write16(path, 8, 8, make([]uint16, 64), meta.Cards()))

			inv, err := inspect.Scan(context.Background(), dir)
			require.NoError(t, err)
			require.Len(t, inv.Frames, 1)
			assert.Equal(t, tt.want, inv.Frames[0].Type)
		})
	}
}

func TestFrameMeta_FileNameAvoidsProcessedTokens(t *testing.T) {
	// A filter literally called "master" would otherwise produce a name inspect drops as processed
	// output, silently losing every frame of that channel.
	meta := FrameMeta{Type: "light", Filter: "master", ExposureUs: 1_000_000, Gain: 1, Bin: 1}
	name := meta.FileName(1)
	for _, bad := range processedTokens {
		assert.NotContains(t, strings.ToLower(name), bad, "filename must not contain %q", bad)
	}
}

func TestFrameMeta_CardsAreExactly80Columns(t *testing.T) {
	meta := FrameMeta{
		Type: "light", Filter: "OIII", ExposureUs: 300_000_000, Gain: 200, Offset: 30, Bin: 2,
		Object: "NGC 7000 North America Nebula", Instrume: "ZWO ASI1600MM Pro",
		RADeg: 314.75, DecDeg: 44.31, HasCoord: true,
		StartedAt: time.Now(),
	}
	for _, c := range meta.Cards() {
		assert.Len(t, c, 80, "card %q must be padded to exactly 80 columns", c)
	}
}

func TestDateObs_HasNoZoneSuffix(t *testing.T) {
	got := DateObs(time.Date(2026, 7, 27, 22, 14, 3, 250*int(time.Millisecond), time.UTC))
	assert.Equal(t, "2026-07-27T22:14:03.250", got)
	assert.NotContains(t, got, "Z")
	assert.NotContains(t, got, "+")
}

func TestRADecStrings(t *testing.T) {
	assert.Equal(t, "00 42 44.33", RAString(10.68472))
	assert.Equal(t, "+41 16 07.4", DecString(41.26872))
	assert.Equal(t, "-05 23 27.6", DecString(-5.39100))
	// RA wraps rather than going negative.
	assert.Equal(t, "23 56 00.00", RAString(-1))
}

func TestFrameMeta_WrittenFileIsReadableAsPixels(t *testing.T) {
	dir := t.TempDir()
	meta := FrameMeta{Type: "light", ExposureUs: 1_000_000, Gain: 1, Bin: 1, StartedAt: time.Now()}
	path := filepath.Join(dir, meta.FileName(1))
	pix := []uint16{0, 1000, 40000, 65535}
	require.NoError(t, fits.Write16(path, 2, 2, pix, meta.Cards()))

	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	for i, want := range pix {
		assert.InDelta(t, float64(want), float64(im.Pix[0][i]), 0.5)
	}
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Positive(t, st.Size())
}
