// Package fitstest builds minimal valid 16-bit FITS files for use in tests.
package fitstest

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const blockSize = 2880

// Write creates a w×h 16-bit FITS file (BZERO=32768, all pixels = pixel) with the given
// extra header cards, under dir/name, and returns its full path. Card values must already be
// FITS-formatted (strings quoted, e.g. "'Ha'"; numbers bare, e.g. "300.0").
func Write(t *testing.T, dir, name string, w, h int, pixel uint16, cards map[string]string) string {
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
		hdr = append(hdr, s...)
	}
	add("SIMPLE", "T")
	add("BITPIX", "16")
	add("NAXIS", "2")
	add("NAXIS1", strconv.Itoa(w))
	add("NAXIS2", strconv.Itoa(h))
	add("BZERO", "32768")
	add("BSCALE", "1")
	for k, v := range cards {
		add(k, v)
	}
	hdr = append(hdr, "END"+strings.Repeat(" ", 77)...)
	for len(hdr)%blockSize != 0 {
		hdr = append(hdr, ' ')
	}

	data := make([]byte, w*h*2)
	raw := uint16(int32(pixel) - 32768)
	for i := 0; i < w*h; i++ {
		binary.BigEndian.PutUint16(data[i*2:], raw)
	}
	for len(data)%blockSize != 0 {
		data = append(data, 0)
	}

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, append(hdr, data...), 0o644))
	return path
}
