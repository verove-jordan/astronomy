package fits

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

// Stats summarizes a sample of an image's physical pixel values (after BZERO/BSCALE).
type Stats struct {
	Count  int
	Mean   float64
	Median float64
	Min    float64
	Max    float64
	// MAD is the median absolute deviation from the median — a robust noise estimate.
	MAD float64
	// P90 is the 90th-percentile value — a robust bright-signal proxy.
	P90 float64
	// BrightFrac is the fraction of sampled pixels brighter than Median+3·MAD — a coarse
	// star/structure-richness proxy (broadband frames have many more bright pixels than narrowband).
	BrightFrac float64
	// Peaks counts local maxima brighter than Median+6·MAD in the sampled run — a coarse count of
	// distinct bright point sources (stars/hot pixels). Lights show many; darks a few; flats/bias ~0.
	Peaks int
}

// Dimensions returns the image width and height from NAXIS1/NAXIS2 (0,0 if absent).
func (f *File) Dimensions() (w, h int) {
	n1, _ := f.Header.Int("NAXIS1")
	n2, _ := f.Header.Int("NAXIS2")
	return int(n1), int(n2)
}

// Stats reads up to maxSample contiguous pixels from the center of the primary data array
// and summarizes them. Centering avoids edge vignetting biasing the mean (used to tell a
// flat from a dark/bias when IMAGETYP is missing).
func (f *File) Stats(maxSample int) (Stats, error) {
	h := f.Header
	bitpix, ok := h.Int("BITPIX")
	if !ok {
		return Stats{}, fmt.Errorf("fits %s: missing BITPIX", f.path)
	}
	naxis, _ := h.Int("NAXIS")
	if naxis < 2 {
		return Stats{}, fmt.Errorf("fits %s: NAXIS=%d, expected >=2", f.path, naxis)
	}
	n1, _ := h.Int("NAXIS1")
	n2, _ := h.Int("NAXIS2")
	n3 := int64(1)
	if v, ok := h.Int("NAXIS3"); ok && v > 0 {
		n3 = v
	}
	total := n1 * n2 * n3
	if total <= 0 {
		return Stats{}, fmt.Errorf("fits %s: bad dimensions %dx%dx%d", f.path, n1, n2, n3)
	}
	bzero, _ := h.Float("BZERO")
	bscale, ok := h.Float("BSCALE")
	if !ok {
		bscale = 1
	}
	bytesPer := int64(absI64(bitpix) / 8)
	if bytesPer == 0 {
		return Stats{}, fmt.Errorf("fits %s: unsupported BITPIX %d", f.path, bitpix)
	}

	sample := int64(maxSample)
	if sample <= 0 || sample > total {
		sample = total
	}
	start := (total - sample) / 2
	offset := f.DataOffset + start*bytesPer

	file, err := os.Open(f.path)
	if err != nil {
		return Stats{}, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return Stats{}, fmt.Errorf("fits %s: seek to data: %w", f.path, err)
	}
	raw := make([]byte, sample*bytesPer)
	if _, err := io.ReadFull(file, raw); err != nil {
		return Stats{}, fmt.Errorf("fits %s: read data sample: %w", f.path, err)
	}

	vals := make([]float64, sample)
	for i := int64(0); i < sample; i++ {
		vals[i] = bzero + bscale*decode(raw[i*bytesPer:], bitpix)
	}
	return summarize(vals), nil
}

// decode reads one big-endian sample of the given BITPIX from b.
func decode(b []byte, bitpix int64) float64 {
	switch bitpix {
	case 8:
		return float64(b[0])
	case 16:
		return float64(int16(binary.BigEndian.Uint16(b)))
	case 32:
		return float64(int32(binary.BigEndian.Uint32(b)))
	case 64:
		return float64(int64(binary.BigEndian.Uint64(b)))
	case -32:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case -64:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	default:
		return 0
	}
}

func summarize(vals []float64) Stats {
	if len(vals) == 0 {
		return Stats{}
	}
	sum, min, max := 0.0, vals[0], vals[0]
	for _, v := range vals {
		sum += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]

	devs := make([]float64, len(sorted))
	for i, v := range sorted {
		devs[i] = math.Abs(v - median)
	}
	sort.Float64s(devs)
	mad := devs[len(devs)/2]

	thresh := median + 3*mad
	bright := 0
	for _, v := range vals {
		if v > thresh {
			bright++
		}
	}

	return Stats{
		Count:      len(vals),
		Mean:       sum / float64(len(vals)),
		Median:     median,
		Min:        min,
		Max:        max,
		MAD:        mad,
		P90:        sorted[(len(sorted)*9)/10],
		BrightFrac: float64(bright) / float64(len(vals)),
		Peaks:      countPeaks(vals, median+6*mad),
	}
}

// countPeaks counts local maxima in the (spatially-ordered) sample that exceed thresh — a cheap
// proxy for distinct bright point sources. A star field yields many; a dark a few hot pixels; a
// flat or bias essentially none. Works on the contiguous center run Stats already read.
func countPeaks(vals []float64, thresh float64) int {
	peaks := 0
	for i := 1; i < len(vals)-1; i++ {
		if vals[i] > thresh && vals[i] > vals[i-1] && vals[i] >= vals[i+1] {
			peaks++
		}
	}
	return peaks
}

func absI64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
