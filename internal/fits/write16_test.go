package fits

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrite16_RoundTripsThroughReadImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Light_L_0001.fit")

	// Span the full unsigned range, including both sides of the BZERO offset.
	w, h := 4, 3
	pix := []uint16{
		0, 1, 32767, 32768,
		32769, 40000, 65534, 65535,
		100, 200, 300, 400,
	}
	require.NoError(t, Write16(path, w, h, pix, nil))

	im, err := ReadImage(path)
	require.NoError(t, err)
	require.Equal(t, w, im.W)
	require.Equal(t, h, im.H)
	require.Equal(t, 1, im.C)

	for i, want := range pix {
		assert.InDelta(t, float64(want), float64(im.Pix[0][i]), 0.5, "pixel %d", i)
	}
}

func TestWrite16_WritesExtraCardsAndUnsignedHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.fit")
	extra := []string{
		strCardForTest("IMAGETYP", "Light Frame"),
		strCardForTest("FILTER", "Ha"),
	}
	require.NoError(t, Write16(path, 2, 2, []uint16{1, 2, 3, 4}, extra))

	f, err := Open(path)
	require.NoError(t, err)
	hdr := f.Header

	bitpix, ok := hdr.Int("BITPIX")
	require.True(t, ok)
	assert.Equal(t, int64(16), bitpix)
	bzero, ok := hdr.Float("BZERO")
	require.True(t, ok)
	assert.Equal(t, float64(unsignedBZero), bzero)

	imagetyp, ok := hdr.String("IMAGETYP")
	require.True(t, ok)
	assert.Equal(t, "Light Frame", imagetyp)
	filter, ok := hdr.String("FILTER")
	require.True(t, ok)
	assert.Equal(t, "Ha", filter)
}

func TestWrite16_RejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	assert.Error(t, Write16(filepath.Join(dir, "a.fit"), 0, 4, nil, nil))
	assert.Error(t, Write16(filepath.Join(dir, "b.fit"), 2, 2, []uint16{1, 2}, nil),
		"a short pixel slice must be refused, not silently padded")
}

// TestWrite16_FileSizeIsHalfOfFloat32 pins the whole point of this writer: a raw sub takes half the
// disk of the float32 path.
func TestWrite16_FileSizeIsHalfOfFloat32(t *testing.T) {
	dir := t.TempDir()
	w, h := 64, 64
	pix := make([]uint16, w*h)
	small := filepath.Join(dir, "small.fit")
	require.NoError(t, Write16(small, w, h, pix, nil))

	im := NewImage(w, h, 1)
	big := filepath.Join(dir, "big.fit")
	require.NoError(t, im.WriteFITS(big))

	sizeOf := func(p string) int64 {
		st, err := os.Stat(p)
		require.NoError(t, err)
		return st.Size()
	}
	ratio := float64(sizeOf(big)) / float64(sizeOf(small))
	assert.InDelta(t, 2, ratio, 0.35, "16-bit data should be about half the size of float32")
	assert.False(t, math.IsNaN(ratio))
}

func strCardForTest(k, v string) string { return strCard(k, v, "") }
