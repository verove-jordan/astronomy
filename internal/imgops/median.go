package imgops

import "sort"

// MedianFilter applies a size×size median filter (reflect boundary), like
// scipy.ndimage.median_filter. It is used to knock out isolated hot pixels while preserving edges.
func MedianFilter(src []float32, w, h, size int) []float32 {
	out := make([]float32, len(src))
	rad := size / 2
	window := make([]float32, 0, size*size)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			window = window[:0]
			for dy := -rad; dy <= rad; dy++ {
				yy := ReflectIndex(y+dy, h)
				for dx := -rad; dx <= rad; dx++ {
					xx := ReflectIndex(x+dx, w)
					window = append(window, src[yy*w+xx])
				}
			}
			sort.Slice(window, func(i, j int) bool { return window[i] < window[j] })
			out[y*w+x] = window[len(window)/2]
		}
	}
	return out
}
