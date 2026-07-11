package fits

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// ReadPlaneBand reads rows [y0,y1) of channel ch from a BITPIX -32 / BZERO 0 / BSCALE 1 FITS file as
// float32 (row-major, (y1-y0)*W values). It errors if the file is not -32/0/1 or the band is out of
// range — the same on-disk contract as OverwriteData. It mirrors that method's offset math: planes are
// contiguous (channel ch starts at ch*W*H samples) and rows are top-down, so band y0 begins at
// DataOffset + 4*(ch*W*H + y0*W). Used to scan a large frame one horizontal strip at a time.
func (f *File) ReadPlaneBand(ch, y0, y1 int) ([]float32, error) {
	w, h, c, err := f.floatPlaneDims()
	if err != nil {
		return nil, err
	}
	if ch < 0 || ch >= c {
		return nil, fmt.Errorf("fits %s: channel %d out of range [0,%d)", f.path, ch, c)
	}
	if y0 < 0 || y1 <= y0 || y1 > h {
		return nil, fmt.Errorf("fits %s: band [%d,%d) out of range for height %d", f.path, y0, y1, h)
	}

	file, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	start := f.DataOffset + 4*(int64(ch)*int64(w)*int64(h)+int64(y0)*int64(w))
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	n := (y1 - y0) * w
	buf := make([]byte, n*4)
	if _, err := io.ReadFull(file, buf); err != nil {
		return nil, fmt.Errorf("fits %s: read band [%d,%d) ch %d: %w", f.path, y0, y1, ch, err)
	}
	out := make([]float32, n)
	for i := range out {
		out[i] = math.Float32frombits(binary.BigEndian.Uint32(buf[i*4:]))
	}
	return out, nil
}

// floatPlaneDims validates the BITPIX -32 / BZERO 0 / BSCALE 1 on-disk contract (identical to
// OverwriteData) and returns the image width, height and channel count.
func (f *File) floatPlaneDims() (w, h, c int, err error) {
	if bitpix, _ := f.Header.Int("BITPIX"); bitpix != -32 {
		return 0, 0, 0, fmt.Errorf("fits %s: ReadPlaneBand needs BITPIX -32, got %d", f.path, bitpix)
	}
	if bz, _ := f.Header.Float("BZERO"); bz != 0 {
		return 0, 0, 0, fmt.Errorf("fits %s: ReadPlaneBand needs BZERO 0, got %g", f.path, bz)
	}
	if bs, ok := f.Header.Float("BSCALE"); ok && bs != 1 {
		return 0, 0, 0, fmt.Errorf("fits %s: ReadPlaneBand needs BSCALE 1, got %g", f.path, bs)
	}
	n1, _ := f.Header.Int("NAXIS1")
	n2, _ := f.Header.Int("NAXIS2")
	if n1 <= 0 || n2 <= 0 {
		return 0, 0, 0, fmt.Errorf("fits %s: bad dimensions %dx%d", f.path, n1, n2)
	}
	chans := int64(1)
	if naxis, _ := f.Header.Int("NAXIS"); naxis >= 3 {
		if n3, ok := f.Header.Int("NAXIS3"); ok && n3 > 0 {
			chans = n3
		}
	}
	return int(n1), int(n2), int(chans), nil
}
