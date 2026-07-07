package optics

import (
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// detectionCap is the maximum long-axis length (px) of the downsampled detection plane. Keeping it
// bounded makes the Gaussian smooth model and connected-component labeling cheap regardless of the
// sensor resolution.
const detectionCap = 1024

// LoadFlatPlane reads a master flat and returns a downsampled, mono "detection plane" together with
// the linear `scale` factor that maps a detection-plane pixel back to full-resolution sensor pixels.
//
// Steps: (1) read the FITS; (2) if the data is un-normalized 16-bit (max pixel > 1.5) divide by
// 65535 to bring it to [0,1]; (3) collapse to mono — a Bayer master (BAYERPAT present) becomes a
// (W/2)x(H/2) 2x2-superpixel MEAN, which exactly cancels the RGGB checkerboard; an RGB master becomes
// the per-pixel channel mean; a mono master is used as-is; (4) mean-pool by an integer factor so the
// long axis is <=detectionCap. `scale` = superpixelFactor (2 for Bayer, else 1) * poolFactor.
//
// It soft-fails: on any read error it returns a wrapped error and zero values.
func LoadFlatPlane(path string) (plane []float32, w, h int, scale float64, bayer bool, err error) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return nil, 0, 0, 0, false, fmt.Errorf("optics: read flat %s: %w", path, err)
	}
	bayer = flatIsBayer(path)

	normalizeTo01(im)
	mono, mw, mh, superFactor := monoPlane(im, bayer)
	pooled, pw, ph, poolFactor := meanPoolToCap(mono, mw, mh, detectionCap)
	return pooled, pw, ph, float64(superFactor * poolFactor), bayer, nil
}

// flatIsBayer reports whether the FITS header carries a non-empty BAYERPAT card. A spurious BAYERPAT
// on a mono master merely triggers a harmless 2x2 mean downsample (no CFA artifact is introduced).
func flatIsBayer(path string) bool {
	f, err := fits.Open(path)
	if err != nil {
		return false
	}
	pat, ok := f.Header.String("BAYERPAT")
	return ok && strings.TrimSpace(pat) != ""
}

// normalizeTo01 divides every channel by 65535 when the brightest pixel exceeds 1.5, i.e. the master
// is stored as un-normalized 16-bit ADU rather than Siril's [0,1] float. It is a no-op for already
// normalized masters.
func normalizeTo01(im *fits.Image) {
	max := float32(0)
	for _, ch := range im.Pix {
		for _, v := range ch {
			if v > max {
				max = v
			}
		}
	}
	if max <= 1.5 {
		return
	}
	const inv = float32(1.0 / 65535.0)
	for _, ch := range im.Pix {
		for i := range ch {
			ch[i] *= inv
		}
	}
}

// monoPlane collapses an image to a single mono plane and returns it with its dimensions and the
// linear superpixel factor (2 for the Bayer path, else 1).
func monoPlane(im *fits.Image, bayer bool) (plane []float32, w, h, superFactor int) {
	switch {
	case bayer:
		p, pw, ph := superpixelMean(im.Pix[0], im.W, im.H)
		return p, pw, ph, 2
	case im.C >= 3:
		return channelMean(im), im.W, im.H, 1
	default:
		p := make([]float32, len(im.Pix[0]))
		copy(p, im.Pix[0])
		return p, im.W, im.H, 1
	}
}

// superpixelMean averages each 2x2 block of a CFA plane into one output pixel, yielding a
// (w/2)x(h/2) mono image in which the RGGB pattern is exactly cancelled.
func superpixelMean(src []float32, w, h int) (out []float32, ow, oh int) {
	ow, oh = w/2, h/2
	out = make([]float32, ow*oh)
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			sx, sy := 2*x, 2*y
			s := src[sy*w+sx] + src[sy*w+sx+1] + src[(sy+1)*w+sx] + src[(sy+1)*w+sx+1]
			out[y*ow+x] = s * 0.25
		}
	}
	return out, ow, oh
}

// channelMean returns the per-pixel mean across all channels.
func channelMean(im *fits.Image) []float32 {
	n := im.W * im.H
	out := make([]float32, n)
	inv := float32(1.0 / float64(im.C))
	for i := 0; i < n; i++ {
		var s float32
		for c := 0; c < im.C; c++ {
			s += im.Pix[c][i]
		}
		out[i] = s * inv
	}
	return out
}

// meanPoolToCap mean-pools src by the smallest integer factor that brings max(w,h) to <=cap. When the
// image already fits it is returned unchanged (factor 1). Trailing pixels that don't fill a whole
// block are dropped.
func meanPoolToCap(src []float32, w, h, cap int) (out []float32, ow, oh, factor int) {
	long := w
	if h > long {
		long = h
	}
	factor = 1
	for long/factor > cap {
		factor++
	}
	if factor == 1 {
		return src, w, h, 1
	}
	ow, oh = w/factor, h/factor
	out = make([]float32, ow*oh)
	inv := float32(1.0 / float64(factor*factor))
	for oy := 0; oy < oh; oy++ {
		for ox := 0; ox < ow; ox++ {
			var s float32
			for dy := 0; dy < factor; dy++ {
				row := (oy*factor + dy) * w
				for dx := 0; dx < factor; dx++ {
					s += src[row+ox*factor+dx]
				}
			}
			out[oy*ow+ox] = s * inv
		}
	}
	return out, ow, oh, factor
}
