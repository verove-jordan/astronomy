package fits

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
)

// Native 16-bit writing, for frames coming straight off a camera. The float32 writer above is right
// for pipeline intermediates (it carries calibrated, out-of-range values), but a raw sub is 12–16
// bit integer data: storing it as float32 doubles every file — 33 MB per ASI1600 frame instead of
// 16 MB, hundreds of GB across a season — while adding no information.
//
// The unsigned convention is the FITS-standard one every astro camera and Siril use:
// BITPIX=16 (signed) with BZERO=32768, so stored value = pixel − 32768 and readers add BZERO back.
// ReadImage already understands it, so a frame written here round-trips through the whole pipeline.

// unsignedBZero is the offset that maps 0…65535 onto the signed 16-bit range.
const unsignedBZero = 32768

// Write16 writes a single-channel 16-bit image with the given extra header cards (the capture
// metadata — see internal/device/fitscards.go). pix is row-major, length w*h, in the camera's own
// 0…65535 range; rows are top-down like every other writer here.
func Write16(path string, w, h int, pix []uint16, extraCards []string) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("write16 %s: bad dimensions %dx%d", path, w, h)
	}
	if len(pix) != w*h {
		return fmt.Errorf("write16 %s: have %d pixels, want %d (%dx%d)", path, len(pix), w*h, w, h)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := bufio.NewWriterSize(f, 1<<20)

	cards := []string{
		card("SIMPLE", "T", "conforms to FITS standard"),
		card("BITPIX", "16", "array data type"),
		card("NAXIS", "2", "number of array dimensions"),
		card("NAXIS1", fmt.Sprintf("%d", w), ""),
		card("NAXIS2", fmt.Sprintf("%d", h), ""),
		card("BZERO", fmt.Sprintf("%d.", unsignedBZero), "offset data range to that of unsigned short"),
		card("BSCALE", "1.", "default scaling factor"),
		strCard("ROWORDER", "TOP-DOWN", "Order of the rows in image array"),
		strCard("PROGRAM", "astrostack", "Software that created this HDU"),
	}
	cards = append(cards, extraCards...)
	if err := writeHeader(buf, cards); err != nil {
		return err
	}

	row := make([]byte, w*2)
	for y := 0; y < h; y++ {
		src := pix[y*w : (y+1)*w]
		for x, v := range src {
			// int32 first: uint16 − 32768 underflows for values below the offset.
			binary.BigEndian.PutUint16(row[x*2:], uint16(int32(v)-unsignedBZero))
		}
		if _, err := buf.Write(row); err != nil {
			return err
		}
	}
	if err := padBlock(buf, int64(w*h*2)); err != nil {
		return err
	}
	return buf.Flush()
}
