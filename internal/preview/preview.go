// Package preview decodes a capture file of any supported format (FITS, TIFF, one-shot-color raws,
// PNG/JPEG) into a small, linearly-normalized 16-bit buffer the web UI can display and stretch live.
//
// The browser cannot natively render FITS / raw / 16-bit TIFF, and astro frames are linear and dark.
// So the server decodes once, box-downsamples to a bounded edge, normalizes the data to a 0..65535
// "display" range (so the viewer's stretch slider has a Siril-like domain), and reports suggested
// auto-stretch bounds. The browser then applies the black/white-point stretch interactively.
package preview

import (
	"context"
	"encoding/binary"
	"io"
	"path/filepath"
	"strings"
)

// DefaultMaxEdge bounds the longest edge of the decoded preview when the caller passes maxEdge <= 0.
const DefaultMaxEdge = 1500

// Preview is a downsampled, linearly-normalized image ready for client-side display stretching.
// Pix is interleaved by pixel: C==1 is mono (one sample/pixel), C==3 is RGB (R,G,B per pixel). Values
// span 0..65535 (the data's own min→max mapped onto the type). AutoLo/AutoHi are a suggested default
// black/white point (robust percentiles) so the viewer opens on a sensible, non-clipped stretch.
type Preview struct {
	W, H, C        int
	Pix            []uint16
	AutoLo, AutoHi uint16
}

// pixelSource is a decoded image addressed lazily so the downsampler never holds a full float copy of
// a large raw — at(c,x,y) returns the physical sample for channel c at (x,y).
type pixelSource interface {
	dims() (w, h, c int)
	at(c, x, y int) float64
}

// Load decodes path into a downsampled, normalized preview. maxEdge <= 0 uses DefaultMaxEdge.
func Load(ctx context.Context, path string, maxEdge int) (*Preview, error) {
	if maxEdge <= 0 {
		maxEdge = DefaultMaxEdge
	}
	src, err := decodeSource(ctx, path)
	if err != nil {
		return nil, err
	}
	return buildPreview(src, maxEdge), nil
}

// buildPreview box-averages the source down to maxEdge, normalizes to 0..65535 over the data's range,
// and computes robust auto-stretch bounds.
func buildPreview(src pixelSource, maxEdge int) *Preview {
	w, h, c := src.dims()
	outW, outH := fitDims(w, h, maxEdge)
	fpix := downsample(src, outW, outH)

	mn, mx := minMax(fpix)
	pix := make([]uint16, len(fpix))
	if mx > mn {
		span := mx - mn
		for i, v := range fpix {
			n := (v - mn) / span
			if n < 0 {
				n = 0
			} else if n > 1 {
				n = 1
			}
			pix[i] = uint16(n*65535 + 0.5)
		}
	}
	lo, hi := autoBounds(pix)
	return &Preview{W: outW, H: outH, C: c, Pix: pix, AutoLo: lo, AutoHi: hi}
}

// fitDims scales (w,h) down so the longest edge is at most maxEdge (never upscales).
func fitDims(w, h, maxEdge int) (int, int) {
	long := w
	if h > long {
		long = h
	}
	if long <= maxEdge || long == 0 {
		return w, h
	}
	s := float64(maxEdge) / float64(long)
	ow := int(float64(w)*s + 0.5)
	oh := int(float64(h)*s + 0.5)
	if ow < 1 {
		ow = 1
	}
	if oh < 1 {
		oh = 1
	}
	return ow, oh
}

// downsample box-averages src into an outW×outH×C float buffer (interleaved by pixel).
func downsample(src pixelSource, outW, outH int) []float64 {
	w, h, c := src.dims()
	out := make([]float64, outW*outH*c)
	sx := float64(w) / float64(outW)
	sy := float64(h) / float64(outH)
	for oy := 0; oy < outH; oy++ {
		y0, y1 := spanFor(oy, sy, h)
		for ox := 0; ox < outW; ox++ {
			x0, x1 := spanFor(ox, sx, w)
			base := (oy*outW + ox) * c
			n := float64((y1 - y0) * (x1 - x0))
			for ch := 0; ch < c; ch++ {
				var sum float64
				for yy := y0; yy < y1; yy++ {
					for xx := x0; xx < x1; xx++ {
						sum += src.at(ch, xx, yy)
					}
				}
				out[base+ch] = sum / n
			}
		}
	}
	return out
}

// spanFor returns the source [start,end) block mapped to output index i (always at least one sample).
func spanFor(i int, scale float64, limit int) (int, int) {
	s := int(float64(i) * scale)
	e := int(float64(i+1) * scale)
	if e <= s {
		e = s + 1
	}
	if e > limit {
		e = limit
	}
	return s, e
}

func minMax(f []float64) (float64, float64) {
	if len(f) == 0 {
		return 0, 0
	}
	mn, mx := f[0], f[0]
	for _, v := range f[1:] {
		if v < mn {
			mn = v
		} else if v > mx {
			mx = v
		}
	}
	return mn, mx
}

// autoBounds returns a robust default black/white point: the 0.5th and 99.5th percentiles of the
// normalized data, so the viewer opens on a non-clipped stretch the user can then refine.
func autoBounds(pix []uint16) (uint16, uint16) {
	if len(pix) == 0 {
		return 0, 65535
	}
	var hist [65536]int
	for _, v := range pix {
		hist[v]++
	}
	lo := percentile(hist[:], len(pix), 0.005)
	hi := percentile(hist[:], len(pix), 0.995)
	if hi <= lo {
		return 0, 65535
	}
	return lo, hi
}

// percentile walks a cumulative histogram and returns the value at fraction p (0..1).
func percentile(hist []int, total int, p float64) uint16 {
	target := int(p * float64(total))
	cum := 0
	for v, n := range hist {
		cum += n
		if cum >= target {
			return uint16(v)
		}
	}
	return uint16(len(hist) - 1)
}

// Encode writes the preview as a little-endian binary stream the browser parses directly:
// header [w:u32][h:u32][c:u32][autoLo:u16][autoHi:u16] (16 bytes), then W*H*C u16 samples.
func (p *Preview) Encode(w io.Writer) error {
	var hdr [16]byte
	binary.LittleEndian.PutUint32(hdr[0:], uint32(p.W))
	binary.LittleEndian.PutUint32(hdr[4:], uint32(p.H))
	binary.LittleEndian.PutUint32(hdr[8:], uint32(p.C))
	binary.LittleEndian.PutUint16(hdr[12:], p.AutoLo)
	binary.LittleEndian.PutUint16(hdr[14:], p.AutoHi)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	buf := make([]byte, len(p.Pix)*2)
	for i, v := range p.Pix {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	_, err := w.Write(buf)
	return err
}

// SupportedExt reports whether path is a format the viewer can decode.
func SupportedExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return fitsExts[ext] || rawExts[ext] || stillExts[ext]
}
