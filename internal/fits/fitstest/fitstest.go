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
	pix := make([]uint16, w*h)
	for i := range pix {
		pix[i] = pixel
	}
	return WritePixels(t, dir, name, w, h, pix, cards)
}

// WritePixels creates a w×h 16-bit FITS file (BZERO=32768, BSCALE=1) from a caller-supplied grid of
// physical pixel values (row-major, 0..65535), with the given extra header cards, and returns its
// full path. Use this for non-constant fixtures (gradients, synthetic dust donuts, saturated flats).
func WritePixels(t *testing.T, dir, name string, w, h int, pix []uint16, cards map[string]string) string {
	t.Helper()
	require.Len(t, pix, w*h, "pixel grid must be w*h")
	hdr := buildHeader(w, h, cards)

	data := make([]byte, w*h*2)
	for i, v := range pix {
		binary.BigEndian.PutUint16(data[i*2:], uint16(int32(v)-32768))
	}
	for len(data)%blockSize != 0 {
		data = append(data, 0)
	}

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, append(hdr, data...), 0o644))
	return path
}

// WriteRGB creates a w×h×3 16-bit FITS cube (NAXIS=3, NAXIS3=3 — the shape Siril writes after a
// `-debayer` calibrate, and what any already-demosaiced colour image converts to), with one constant
// value per plane, and returns its full path. Use it for fixtures that must be colour WITHOUT
// carrying a BAYERPAT card, which is the case a Bayer-only detector cannot see.
func WriteRGB(t *testing.T, dir, name string, w, h int, r, g, b uint16, cards map[string]string) string {
	t.Helper()
	pix := make([]uint16, w*h*3)
	for i := 0; i < w*h; i++ {
		pix[i], pix[w*h+i], pix[2*w*h+i] = r, g, b
	}
	hdr := buildHeaderPlanes(w, h, 3, cards)

	data := make([]byte, len(pix)*2)
	for i, v := range pix {
		binary.BigEndian.PutUint16(data[i*2:], uint16(int32(v)-32768))
	}
	for len(data)%blockSize != 0 {
		data = append(data, 0)
	}
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, append(hdr, data...), 0o644))
	return path
}

// buildHeader assembles a padded 16-bit primary-HDU header with the supplied extra cards.
func buildHeader(w, h int, cards map[string]string) []byte { return buildHeaderPlanes(w, h, 1, cards) }

// buildHeaderPlanes is buildHeader with an explicit plane count: 1 emits the 2-D header every
// existing fixture uses (byte-identical), 3 emits a NAXIS=3 colour cube.
func buildHeaderPlanes(w, h, planes int, cards map[string]string) []byte {
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
	if planes > 1 {
		add("NAXIS", "3")
	} else {
		add("NAXIS", "2")
	}
	add("NAXIS1", strconv.Itoa(w))
	add("NAXIS2", strconv.Itoa(h))
	if planes > 1 {
		add("NAXIS3", strconv.Itoa(planes))
	}
	add("BZERO", "32768")
	add("BSCALE", "1")
	for k, v := range cards {
		add(k, v)
	}
	hdr = append(hdr, "END"+strings.Repeat(" ", 77)...)
	for len(hdr)%blockSize != 0 {
		hdr = append(hdr, ' ')
	}
	return hdr
}
