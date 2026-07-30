package fits

// Resize returns a bilinear resample of the image at w×h. It exists for the optional downscaled
// chroma denoise (ASTRO_DENOISE_SCALE): the AI denoiser runs on a smaller copy, and only the
// upscaled CHROMA is transferred back, so bilinear softness never touches luminance detail.
func (im *Image) Resize(w, h int) *Image {
	if w == im.W && h == im.H {
		return im
	}
	out := NewImage(w, h, im.C)
	sx := float64(im.W) / float64(w)
	sy := float64(im.H) / float64(h)
	for c := 0; c < im.C; c++ {
		src, dst := im.Pix[c], out.Pix[c]
		for y := 0; y < h; y++ {
			// Sample at the pixel centre so the grids stay aligned in both directions.
			fy := (float64(y)+0.5)*sy - 0.5
			y0 := int(fy)
			if fy < 0 {
				y0, fy = 0, 0
			}
			y1 := y0 + 1
			if y1 >= im.H {
				y1 = im.H - 1
			}
			wy := float32(fy - float64(y0))
			for x := 0; x < w; x++ {
				fx := (float64(x)+0.5)*sx - 0.5
				x0 := int(fx)
				if fx < 0 {
					x0, fx = 0, 0
				}
				x1 := x0 + 1
				if x1 >= im.W {
					x1 = im.W - 1
				}
				wx := float32(fx - float64(x0))
				top := src[y0*im.W+x0]*(1-wx) + src[y0*im.W+x1]*wx
				bot := src[y1*im.W+x0]*(1-wx) + src[y1*im.W+x1]*wx
				dst[y*w+x] = top*(1-wy) + bot*wy
			}
		}
	}
	return out
}
