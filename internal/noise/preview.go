package noise

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

const heatmapScale = 8 // nearest-neighbour upscale factor for the heatmap PNG

// WriteHeatmapPNG renders the per-tile sigma grid of rep to a grayscale PNG at path, nearest-upscaled
// ×8 and normalized to the grid's 99th percentile. It errors on an inconsistent/empty grid.
func WriteHeatmapPNG(rep Report, path string) error {
	gw, gh := rep.GridW, rep.GridH
	if gw <= 0 || gh <= 0 || len(rep.Tiles) < gw*gh {
		return fmt.Errorf("noise: heatmap needs a %dx%d grid but have %d tiles", gw, gh, len(rep.Tiles))
	}
	norm := percentile64(floats64(rep.Tiles), 99)
	if !(norm > 0) {
		norm = 1
	}
	img := image.NewGray(image.Rect(0, 0, gw*heatmapScale, gh*heatmapScale))
	for ty := 0; ty < gh; ty++ {
		for tx := 0; tx < gw; tx++ {
			v := clamp(float64(rep.Tiles[ty*gw+tx])/norm, 0, 1)
			fillBlock(img, tx*heatmapScale, ty*heatmapScale, heatmapScale, uint8(v*255))
		}
	}
	return writePNG(img, path)
}

// floats64 converts a float32 slice to a fresh float64 slice.
func floats64(s []float32) []float64 {
	out := make([]float64, len(s))
	for i, v := range s {
		out[i] = float64(v)
	}
	return out
}

// fillBlock paints a size×size block of constant gray at (x0,y0).
func fillBlock(img *image.Gray, x0, y0, size int, val uint8) {
	for dy := 0; dy < size; dy++ {
		for dx := 0; dx < size; dx++ {
			img.SetGray(x0+dx, y0+dy, color.Gray{Y: val})
		}
	}
}

// writePNG encodes img to path, wrapping every failure with context.
func writePNG(img image.Image, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("noise: create heatmap %s: %w", path, err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return fmt.Errorf("noise: encode heatmap %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("noise: close heatmap %s: %w", path, err)
	}
	return nil
}
