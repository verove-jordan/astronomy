package nightscape

import "github.com/verove-jordan/astronomy/internal/fits"

// skyChromaBlur is the chroma-denoise radius (px) for the linear sky stack — gentle, just enough to melt
// the colour speckle of the thin iPhone stack without dulling star colour.
const skyChromaBlur = 3.0

// chromaBlur denoises COLOUR speckle while keeping luminance (and thus stars and the band's structure)
// sharp: it splits the image into luminance + per-channel chroma (channel − luminance), Gaussian-blurs
// only the chroma, and recombines. The thin 12-frame iPhone stack is chroma-noisy; blurring the colour
// smooths the speckle without softening star points or luminance detail — and it runs in Go, so the
// result is denoised even when GraXpert is absent. Applied before boostSaturation so the boost can't
// re-amplify the speckle. sigma ≤ 0 (or a mono image) is a no-op.
func chromaBlur(im *fits.Image, sigma float64) {
	if im.C != 3 || sigma <= 0 {
		return
	}
	lum := luminance(im)
	chroma := make([]float32, len(lum))
	for c := 0; c < 3; c++ {
		p := im.Pix[c]
		for i := range p {
			chroma[i] = p[i] - lum[i]
		}
		blurred := gaussianBlur(chroma, im.W, im.H, sigma)
		for i := range p {
			p[i] = lum[i] + blurred[i]
		}
	}
}
