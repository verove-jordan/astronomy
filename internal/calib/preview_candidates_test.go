package calib

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// previewCalSet builds a calibration Set the way inspect groups frames (setKeyFor): bias carries no
// exposure/filter/temp; darks no filter; flats no object. (library_test.go owns the shorter calSet.)
func previewCalSet(ft inspect.FrameType, filter string, gain, offset, expMs int64, tempC, bin, count int) inspect.Set {
	key := inspect.SetKey{Type: ft, Gain: gain, Offset: offset, Bin: bin}
	switch ft {
	case inspect.Dark, inspect.DarkFlat:
		key.ExposureMs, key.TempBucket = expMs, tempC
	case inspect.Flat:
		key.Filter, key.ExposureMs, key.TempBucket = filter, expMs, tempC
	}
	return inspect.Set{Key: key, Count: count}
}

func TestPreviewCandidates(t *testing.T) {
	bias := previewCalSet(inspect.Bias, "", 0, 10, 0, 0, 1, 400)
	dark := previewCalSet(inspect.Dark, "", 0, 10, 10, -15, 1, 64)
	flat := previewCalSet(inspect.Flat, "L", 0, 10, 10, -15, 1, 239)
	inv := &inspect.Inventory{Sets: []inspect.Set{bias, dark, flat}}

	t.Run("empty library → one path-less synthetic per cal set", func(t *testing.T) {
		got := PreviewCandidates(inv, nil)
		require.Len(t, got, 3)
		byType := map[MasterType]Master{}
		for _, m := range got {
			assert.Empty(t, m.Path, "synthetic masters carry no path")
			byType[m.Type] = m
		}
		assert.Equal(t, int64(10), byType[MasterBias].Offset)
		assert.Equal(t, 400, byType[MasterBias].FrameCount)
		assert.Equal(t, int64(10), byType[MasterDark].ExposureMs)
		assert.Equal(t, int64(-15000), byType[MasterDark].TempMilliC)
		assert.True(t, byType[MasterDark].HasTemp)
		assert.Equal(t, "L", byType[MasterFlat].Filter)
	})

	t.Run("field-matched set is NOT duplicated — the library master stands", func(t *testing.T) {
		lib := []Master{{Type: MasterBias, Gain: 0, Offset: 10, Bin: 1, FrameCount: 999, Path: "/lib/master_BIAS.fits"}}
		got := PreviewCandidates(inv, lib)
		require.Len(t, got, 3) // library bias + synthetic dark + synthetic flat
		biasCount := 0
		for _, m := range got {
			if m.Type == MasterBias {
				biasCount++
				assert.Equal(t, "/lib/master_BIAS.fits", m.Path, "the library bias is the sole bias candidate")
			}
		}
		assert.Equal(t, 1, biasCount)
	})

	t.Run("mismatched library master does not suppress the synthetic", func(t *testing.T) {
		lib := []Master{{Type: MasterBias, Gain: 300, Offset: 50, Bin: 1, FrameCount: 400, Path: "/lib/stale.fits"}}
		got := PreviewCandidates(inv, lib)
		require.Len(t, got, 4) // stale library bias + 3 synthetics
	})

	t.Run("nil inventory → library passthrough", func(t *testing.T) {
		lib := []Master{{Type: MasterFlat, Filter: "L", Path: "/lib/f.fits"}}
		assert.Equal(t, lib, PreviewCandidates(nil, lib))
	})
}
