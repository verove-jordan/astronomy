package fits

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// Image is a fully-decoded FITS image in memory: mono (C==1) or planar RGB (C==3), stored in the
// file's row order (Siril writes ROWORDER=TOP-DOWN, so row 0 is the top of the frame). Pixel values
// are the physical values after BZERO/BSCALE — for Siril's processed frames that is the [0,1] range.
type Image struct {
	W, H, C int
	Pix     [][]float32 // Pix[channel][y*W+x]
}

// NewImage allocates a zeroed image with c channels (1 or 3).
func NewImage(w, h, c int) *Image {
	pix := make([][]float32, c)
	for i := range pix {
		pix[i] = make([]float32, w*h)
	}
	return &Image{W: w, H: h, C: c, Pix: pix}
}

// Clone returns a deep copy.
func (im *Image) Clone() *Image {
	out := NewImage(im.W, im.H, im.C)
	for c := range im.Pix {
		copy(out.Pix[c], im.Pix[c])
	}
	return out
}

// ReadImage decodes the primary HDU of a FITS file into an Image. It supports BITPIX -32 (float32),
// 16 (int16, unsigned via BZERO) and 8 (byte), and NAXIS 2 (mono) or 3 with NAXIS3==3 (planar RGB).
func ReadImage(path string) (*Image, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	h := f.Header
	bitpix, ok := h.Int("BITPIX")
	if !ok {
		return nil, fmt.Errorf("fits %s: missing BITPIX", path)
	}
	naxis, _ := h.Int("NAXIS")
	w, _ := h.Int("NAXIS1")
	hgt, _ := h.Int("NAXIS2")
	if w <= 0 || hgt <= 0 {
		return nil, fmt.Errorf("fits %s: bad dimensions %dx%d", path, w, hgt)
	}
	chans := int64(1)
	if naxis >= 3 {
		if n3, ok := h.Int("NAXIS3"); ok && n3 > 0 {
			chans = n3
		}
	}
	if chans != 1 && chans != 3 {
		return nil, fmt.Errorf("fits %s: unsupported channel count %d", path, chans)
	}
	bzero, _ := h.Float("BZERO")
	bscale, ok := h.Float("BSCALE")
	if !ok {
		bscale = 1
	}
	bytesPer := int(absI64(bitpix) / 8)
	if bytesPer == 0 {
		return nil, fmt.Errorf("fits %s: unsupported BITPIX %d", path, bitpix)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.Seek(f.DataOffset, io.SeekStart); err != nil {
		return nil, err
	}

	im := NewImage(int(w), int(hgt), int(chans))
	r := bufio.NewReaderSize(file, 1<<20)
	plane := int(w * hgt)
	buf := make([]byte, plane*bytesPer)
	for c := 0; c < int(chans); c++ {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("fits %s: read plane %d: %w", path, c, err)
		}
		dst := im.Pix[c]
		for i := 0; i < plane; i++ {
			dst[i] = float32(bzero + bscale*decode(buf[i*bytesPer:], bitpix))
		}
	}
	return im, nil
}

// WriteFITS writes the image as 32-bit float, planar RGB (or mono), ROWORDER top-down — matching
// the convention of Siril's own output so the file opens consistently in Siril/GIMP.
func (im *Image) WriteFITS(path string) error {
	return im.WriteFITSWith(path, nil)
}

// WriteFITSWith is WriteFITS with extra pre-padded header cards (e.g. WCS.Cards()) appended before
// END — the way the mosaic assembler stamps a real plate solution into a canvas it synthesized.
func (im *Image) WriteFITSWith(path string, extraCards []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)

	cards := []string{
		card("SIMPLE", "T", "conforms to FITS standard"),
		card("BITPIX", "-32", "array data type"),
	}
	if im.C == 3 {
		cards = append(cards, card("NAXIS", "3", "number of array dimensions"))
	} else {
		cards = append(cards, card("NAXIS", "2", "number of array dimensions"))
	}
	cards = append(cards,
		card("NAXIS1", fmt.Sprintf("%d", im.W), ""),
		card("NAXIS2", fmt.Sprintf("%d", im.H), ""),
	)
	if im.C == 3 {
		cards = append(cards, card("NAXIS3", "3", ""))
	}
	cards = append(cards,
		card("BZERO", "0.", "offset data range"),
		card("BSCALE", "1.", "default scaling factor"),
		strCard("ROWORDER", "TOP-DOWN", "Order of the rows in image array"),
		strCard("PROGRAM", "astrostack", "Software that created this HDU"),
	)
	cards = append(cards, extraCards...)
	if err := writeHeader(w, cards); err != nil {
		return err
	}

	plane := im.W * im.H
	row := make([]byte, plane*4)
	for c := 0; c < im.C; c++ {
		src := im.Pix[c]
		for i := 0; i < plane; i++ {
			binary.BigEndian.PutUint32(row[i*4:], math.Float32bits(src[i]))
		}
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	if err := padBlock(w, int64(plane*im.C*4)); err != nil {
		return err
	}
	return w.Flush()
}

// OverwriteData rewrites only the pixel data of an existing FITS file in place, preserving its header
// (registration WCS, DATE-OBS, EXPTIME, …). The on-disk file must be 32-bit float (BITPIX -32, BZERO 0,
// BSCALE 1) with dimensions and channel count matching im — the case for Siril's registered light
// frames. Used to write transient-masked frames back without disturbing the metadata the stack relies on.
func (im *Image) OverwriteData(path string) error {
	f, err := Open(path)
	if err != nil {
		return err
	}
	if bitpix, _ := f.Header.Int("BITPIX"); bitpix != -32 {
		return fmt.Errorf("fits %s: OverwriteData needs BITPIX -32, got %d", path, bitpix)
	}
	if bz, _ := f.Header.Float("BZERO"); bz != 0 {
		return fmt.Errorf("fits %s: OverwriteData needs BZERO 0, got %g", path, bz)
	}
	if bs, ok := f.Header.Float("BSCALE"); ok && bs != 1 {
		return fmt.Errorf("fits %s: OverwriteData needs BSCALE 1, got %g", path, bs)
	}
	w, _ := f.Header.Int("NAXIS1")
	hgt, _ := f.Header.Int("NAXIS2")
	chans := int64(1)
	if naxis, _ := f.Header.Int("NAXIS"); naxis >= 3 {
		if n3, ok := f.Header.Int("NAXIS3"); ok && n3 > 0 {
			chans = n3
		}
	}
	if int(w) != im.W || int(hgt) != im.H || int(chans) != im.C {
		return fmt.Errorf("fits %s: on-disk %dx%dx%d != image %dx%dx%d", path, w, hgt, chans, im.W, im.H, im.C)
	}

	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(f.DataOffset, io.SeekStart); err != nil {
		return err
	}
	bw := bufio.NewWriterSize(file, 1<<20)
	plane := im.W * im.H
	buf := make([]byte, plane*4)
	for c := 0; c < im.C; c++ {
		src := im.Pix[c]
		for i := 0; i < plane; i++ {
			binary.BigEndian.PutUint32(buf[i*4:], math.Float32bits(src[i]))
		}
		if _, err := bw.Write(buf); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func card(key, val, comment string) string {
	body := fmt.Sprintf("%-8s= %20s", key, val)
	if comment != "" {
		body += " / " + comment
	}
	return pad80(body)
}

func strCard(key, val, comment string) string {
	body := fmt.Sprintf("%-8s= %-20s", key, "'"+val+"'")
	if comment != "" {
		body += " / " + comment
	}
	return pad80(body)
}

func pad80(s string) string {
	if len(s) > cardSize {
		return s[:cardSize]
	}
	return s + strings.Repeat(" ", cardSize-len(s))
}

func writeHeader(w io.Writer, cards []string) error {
	var b strings.Builder
	for _, c := range cards {
		b.WriteString(c)
	}
	b.WriteString(pad80("END"))
	out := b.String()
	if rem := len(out) % blockSize; rem != 0 {
		out += strings.Repeat(" ", blockSize-rem)
	}
	_, err := io.WriteString(w, out)
	return err
}

// padBlock writes zero bytes to round a data segment of n bytes up to a full 2880-byte block.
func padBlock(w io.Writer, n int64) error {
	rem := n % blockSize
	if rem == 0 {
		return nil
	}
	_, err := w.Write(make([]byte, blockSize-rem))
	return err
}
