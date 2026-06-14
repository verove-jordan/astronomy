package fits

import (
	"bufio"
	"fmt"
	"io"
	"os"
)

// Pooling selects how ReadDownsampled combines source pixels into each output cell.
type Pooling int

const (
	Mean Pooling = iota // average — good for background/level estimates
	Max                 // maximum — preserves thin bright features (trails) when shrinking
)

// ReadDownsampled streams the primary 2-D image and reduces it to at most maxDim on its larger
// axis using the given pooling, returning the row-major grid and its dimensions. Streaming keeps
// memory bounded for 16 MP frames.
func (f *File) ReadDownsampled(maxDim int, pool Pooling) (grid []float64, outW, outH int, err error) {
	h := f.Header
	bitpix, ok := h.Int("BITPIX")
	if !ok {
		return nil, 0, 0, fmt.Errorf("fits %s: missing BITPIX", f.path)
	}
	n1, _ := h.Int("NAXIS1")
	n2, _ := h.Int("NAXIS2")
	if n1 <= 0 || n2 <= 0 {
		return nil, 0, 0, fmt.Errorf("fits %s: bad dimensions %dx%d", f.path, n1, n2)
	}
	bzero, _ := h.Float("BZERO")
	bscale, ok := h.Float("BSCALE")
	if !ok {
		bscale = 1
	}
	bytesPer := int(absI64(bitpix) / 8)
	if bytesPer == 0 {
		return nil, 0, 0, fmt.Errorf("fits %s: unsupported BITPIX %d", f.path, bitpix)
	}

	factor := 1
	for (int(n1)+factor-1)/factor > maxDim || (int(n2)+factor-1)/factor > maxDim {
		factor++
	}
	outW = (int(n1) + factor - 1) / factor
	outH = (int(n2) + factor - 1) / factor
	grid = make([]float64, outW*outH)
	counts := make([]int, outW*outH)
	if pool == Max {
		for i := range grid {
			grid[i] = -1e308
		}
	}

	file, err := os.Open(f.path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()
	if _, err := file.Seek(f.DataOffset, io.SeekStart); err != nil {
		return nil, 0, 0, err
	}
	r := bufio.NewReaderSize(file, 1<<20)
	row := make([]byte, int(n1)*bytesPer)
	for y := 0; y < int(n2); y++ {
		if _, err := io.ReadFull(r, row); err != nil {
			return nil, 0, 0, fmt.Errorf("fits %s: read row %d: %w", f.path, y, err)
		}
		oy := y / factor
		for x := 0; x < int(n1); x++ {
			v := bzero + bscale*decode(row[x*bytesPer:], bitpix)
			idx := oy*outW + x/factor
			if pool == Max {
				if v > grid[idx] {
					grid[idx] = v
				}
			} else {
				grid[idx] += v
				counts[idx]++
			}
		}
	}
	if pool == Mean {
		for i := range grid {
			if counts[i] > 0 {
				grid[i] /= float64(counts[i])
			}
		}
	}
	return grid, outW, outH, nil
}
