package fits

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFITS builds a minimal valid 16-bit FITS file (BZERO=32768 unsigned) whose pixels all
// equal physical value `pixel`, with the given extra header cards, and returns its path.
func writeFITS(t *testing.T, w, h int, pixel uint16, cards map[string]string) string {
	t.Helper()
	var hdr []byte
	add := func(key, val string) {
		s := key
		for len(s) < 8 {
			s += " "
		}
		s += "= " + val
		if len(s) > 80 {
			s = s[:80]
		}
		for len(s) < 80 {
			s += " "
		}
		hdr = append(hdr, []byte(s)...)
	}
	add("SIMPLE", "T")
	add("BITPIX", "16")
	add("NAXIS", "2")
	add("NAXIS1", itoa(w))
	add("NAXIS2", itoa(h))
	add("BZERO", "32768")
	add("BSCALE", "1")
	for k, v := range cards {
		add(k, v)
	}
	hdr = append(hdr, []byte("END"+strings.Repeat(" ", 77))...)
	for len(hdr)%blockSize != 0 {
		hdr = append(hdr, ' ')
	}

	data := make([]byte, w*h*2)
	raw := uint16(int32(pixel) - 32768) // physical = BZERO + raw(int16)
	for i := 0; i < w*h; i++ {
		binary.BigEndian.PutUint16(data[i*2:], raw)
	}
	for len(data)%blockSize != 0 {
		data = append(data, 0)
	}

	path := filepath.Join(t.TempDir(), "frame.fits")
	require.NoError(t, os.WriteFile(path, append(hdr, data...), 0o644))
	return path
}

func itoa(i int) string { return strings.TrimSpace(itoaSlow(i)) }
func itoaSlow(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestOpen_ParsesHeaderCards(t *testing.T) {
	path := writeFITS(t, 4, 4, 10000, map[string]string{
		"IMAGETYP": "'Light Frame'",
		"FILTER":   "'Ha'",
		"EXPTIME":  "300.0",
		"GAIN":     "139",
		"OFFSET":   "21",
		"CCD-TEMP": "-15.0",
		"XBINNING": "1",
	})

	f, err := Open(path)
	require.NoError(t, err)

	tests := []struct {
		name string
		fn   func() (any, bool)
		want any
	}{
		{"filter", func() (any, bool) { return f.Header.String("FILTER") }, "Ha"},
		{"imagetyp", func() (any, bool) { return f.Header.String("IMAGETYP") }, "Light Frame"},
		{"exptime", func() (any, bool) { return f.Header.Float("EXPTIME") }, 300.0},
		{"gain", func() (any, bool) { return f.Header.Int("GAIN") }, int64(139)},
		{"offset", func() (any, bool) { return f.Header.Int("OFFSET") }, int64(21)},
		{"temp", func() (any, bool) { return f.Header.Float("CCD-TEMP") }, -15.0},
		{"case-insensitive", func() (any, bool) { return f.Header.Int("xbinning") }, int64(1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.fn()
			require.True(t, ok)
			assert.Equal(t, tc.want, got)
		})
	}

	_, ok := f.Header.String("NOSUCHKEY")
	assert.False(t, ok)
}

func TestCountPeaks(t *testing.T) {
	tests := []struct {
		name   string
		vals   []float64
		thresh float64
		want   int
	}{
		{"flat run has no peaks", []float64{100, 100, 100, 100, 100}, 110, 0},
		{"two stars above threshold", []float64{100, 500, 100, 100, 600, 100}, 300, 2},
		{"bright pixel below threshold ignored", []float64{100, 250, 100}, 300, 0},
		{"plateau peak counted once", []float64{100, 500, 500, 100}, 300, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, countPeaks(tt.vals, tt.thresh))
		})
	}
}

func TestStats_CenterSample(t *testing.T) {
	path := writeFITS(t, 8, 8, 12345, nil)
	f, err := Open(path)
	require.NoError(t, err)

	w, h := f.Dimensions()
	assert.Equal(t, 8, w)
	assert.Equal(t, 8, h)

	st, err := f.Stats(64)
	require.NoError(t, err)
	assert.Equal(t, 64, st.Count)
	assert.InDelta(t, 12345.0, st.Mean, 0.001)
	assert.InDelta(t, 12345.0, st.Median, 0.001)
}
