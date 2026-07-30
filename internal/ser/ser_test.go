package ser

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The header is a binary contract with every other planetary tool (Siril, AutoStakkert, PIPP). One
// wrong offset makes a file that either fails to open or, worse, opens with subtly wrong dimensions
// — so every field is asserted at its documented byte position.
func TestWriter_HeaderLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ser")
	start := time.Date(2026, 7, 27, 22, 14, 3, 250_000_000, time.UTC)

	w, err := Create(path, Options{
		Width: 640, Height: 480, BitDepth: 16, ColorID: ColorMono,
		Observer: "Jordan", Camera: "ZWO ASI1600MM Pro", Telescope: "FC-100 DF",
		UTCStart: start,
	})
	require.NoError(t, err)
	require.NoError(t, w.WriteFrame16(make([]uint16, 640*480), start))
	require.NoError(t, w.Close())

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(b), HeaderSize)

	assert.Equal(t, "LUCAM-RECORDER", string(b[0:14]), "the signature every reader checks first")
	assert.Equal(t, uint32(ColorMono), binary.LittleEndian.Uint32(b[18:22]))
	assert.Equal(t, uint32(1), binary.LittleEndian.Uint32(b[22:26]),
		"1 means the pixel data is little-endian, which is what we write")
	assert.Equal(t, uint32(640), binary.LittleEndian.Uint32(b[26:30]))
	assert.Equal(t, uint32(480), binary.LittleEndian.Uint32(b[30:34]))
	assert.Equal(t, uint32(16), binary.LittleEndian.Uint32(b[34:38]))
	assert.Equal(t, uint32(1), binary.LittleEndian.Uint32(b[38:42]), "frame count, rewritten at Close")
	assert.Equal(t, "Jordan"+"                                  ", string(b[42:82]))
	assert.Equal(t, "ZWO ASI1600MM Pro", string(b[82:99]))
	assert.Equal(t, "FC-100 DF", string(b[122:131]))
}

// The epoch conversion is the subtle one: SER counts 100 ns ticks from year 1, a range that
// overflows time.Duration outright. A round trip through a real date proves the arithmetic.
func TestSerTime_RoundTripsARealDate(t *testing.T) {
	for _, want := range []time.Time{
		time.Date(2026, 7, 27, 22, 14, 3, 250_000_000, time.UTC),
		time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2099, 12, 31, 23, 59, 59, 900_000_000, time.UTC),
	} {
		got := FromSerTime(toSerTime(want))
		assert.True(t, want.Equal(got), "round trip changed %s into %s", want, got)
	}

	// The known anchor: the Unix epoch is exactly 621355968000000000 ticks after year 1. If this is
	// wrong every timestamp in every file is wrong by a constant, and no reader would agree with us.
	assert.Equal(t, uint64(621355968000000000),
		toSerTime(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)))

	// A modern date must NOT saturate — the bug this test exists to prevent.
	a := toSerTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	b := toSerTime(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	assert.Greater(t, b, a, "consecutive years must produce increasing, distinct timestamps")
}

// Frame payloads must land back to back straight after the header, with no padding — that is the
// whole file format.
func TestWriter_FrameBytesAndTrailer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ser")
	const w0, h0, n = 4, 3, 5

	w, err := Create(path, Options{Width: w0, Height: h0, BitDepth: 16})
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		pix := make([]uint16, w0*h0)
		pix[0] = uint16(1000 + i) // a marker to find the frame boundary
		require.NoError(t, w.WriteFrame16(pix, time.Date(2026, 7, 27, 22, i, 0, 0, time.UTC)))
	}
	assert.Equal(t, n, w.Frames())
	require.NoError(t, w.Close())

	b, err := os.ReadFile(path)
	require.NoError(t, err)

	frameBytes := w0 * h0 * 2
	assert.Equal(t, HeaderSize+n*frameBytes+n*8, len(b),
		"header + frames + one 8-byte timestamp per frame, and nothing else")

	for i := 0; i < n; i++ {
		off := HeaderSize + i*frameBytes
		assert.Equal(t, uint16(1000+i), binary.LittleEndian.Uint16(b[off:off+2]),
			"frame %d must start exactly where the format says", i)
	}

	// The trailer must carry the timestamps in frame order.
	trailer := HeaderSize + n*frameBytes
	for i := 0; i < n; i++ {
		got := FromSerTime(binary.LittleEndian.Uint64(b[trailer+i*8:]))
		assert.Equal(t, i, got.Minute(), "timestamp %d is out of order or wrong", i)
	}
}

// Without Close the frame count stays zero, which reads as an empty file. Worth asserting so the
// capture path is never tempted to skip it.
func TestWriter_CloseIsWhatMakesTheFileReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ser")
	w, err := Create(path, Options{Width: 4, Height: 4, BitDepth: 16})
	require.NoError(t, err)
	require.NoError(t, w.WriteFrame16(make([]uint16, 16), time.Now()))

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), binary.LittleEndian.Uint32(b[38:42]),
		"before Close the header still says zero frames")

	require.NoError(t, w.Close())
	b, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), binary.LittleEndian.Uint32(b[38:42]))
}

// 8-bit files are legal SER and some planetary workflows want them for speed.
func TestWriter_EightBit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ser")
	w, err := Create(path, Options{Width: 4, Height: 2, BitDepth: 8})
	require.NoError(t, err)
	require.NoError(t, w.WriteFrame8([]byte{1, 2, 3, 4, 5, 6, 7, 8}, time.Now()))
	require.NoError(t, w.Close())

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(8), binary.LittleEndian.Uint32(b[34:38]))
	assert.Equal(t, []byte{1, 2, 3, 4, 5, 6, 7, 8}, b[HeaderSize:HeaderSize+8])
}

// Colour files carry three planes per pixel, so the frame size must account for that — getting it
// wrong would misalign every frame after the first.
func TestWriter_RGBFrameSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ser")
	w, err := Create(path, Options{Width: 4, Height: 2, BitDepth: 16, ColorID: ColorRGB})
	require.NoError(t, err)
	require.NoError(t, w.WriteFrame16(make([]uint16, 4*2*3), time.Now()))
	require.NoError(t, w.Close())

	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, int64(HeaderSize+4*2*3*2+8), st.Size())
}

// Mismatched frames must be refused, not written. A short frame silently shifts every subsequent
// one, and the file only looks wrong hours later when it is stacked.
func TestWriter_RejectsWrongSizedFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ser")
	w, err := Create(path, Options{Width: 4, Height: 4, BitDepth: 16})
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	require.Error(t, w.WriteFrame16(make([]uint16, 15), time.Now()))
	require.Error(t, w.WriteFrame16(make([]uint16, 17), time.Now()))
	assert.Zero(t, w.Frames(), "a rejected frame must not be counted")

	err = w.WriteFrame8(make([]byte, 16), time.Now())
	assert.ErrorContains(t, err, "16-bit", "an 8-bit write into a 16-bit file must be refused")
}

func TestCreate_RejectsNonsense(t *testing.T) {
	dir := t.TempDir()
	_, err := Create(filepath.Join(dir, "a.ser"), Options{Width: 0, Height: 4, BitDepth: 16})
	assert.Error(t, err)
	_, err = Create(filepath.Join(dir, "b.ser"), Options{Width: 4, Height: 4, BitDepth: 12})
	assert.ErrorContains(t, err, "bit depth", "12-bit data is padded into 16; the file cannot say 12")
}

// Writing after Close must fail rather than corrupt the trailer that was just written.
func TestWriter_RefusesWritesAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ser")
	w, err := Create(path, Options{Width: 2, Height: 2, BitDepth: 16})
	require.NoError(t, err)
	require.NoError(t, w.Close())
	assert.Error(t, w.WriteFrame16(make([]uint16, 4), time.Now()))
	assert.NoError(t, w.Close(), "closing twice must be harmless")
}
