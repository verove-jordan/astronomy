package pipeline

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register the PNG decoder for the finish previews
	"math"
	"os"
)

// finishMetrics are objective, no-reference quality signals measured directly from the rendered
// finish PNG — the exact pixels the vision model and the user see. They drive the supervisor's
// deterministic score, so a crushed or colour-cast render can never win on the model's word alone.
type finishMetrics struct {
	BlackClip  [3]float64 `json:"black_clip"` // fraction of pixels at 0, per channel R,G,B
	WhiteClip  [3]float64 `json:"white_clip"` // fraction of pixels at 255, per channel R,G,B
	Median     [3]float64 `json:"median"`     // per-channel median, 0..1
	Background float64    `json:"background"` // sky level estimate (10th-percentile luma), 0..1
	GreenCast  float64    `json:"green_cast"` // medianG - mean(medianR, medianB); >0 → green cast
}

// measureFinish decodes a rendered PNG and computes per-channel clipping, medians, a background
// estimate and a green-cast figure. Pure and deterministic, so it is unit-testable without Siril.
func measureFinish(path string) (finishMetrics, error) {
	img, err := decodeImage(path)
	if err != nil {
		return finishMetrics{}, err
	}
	return metricsFromImage(img), nil
}

func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return img, nil
}

func metricsFromImage(img image.Image) finishMetrics {
	b := img.Bounds()
	// Stride so the histogram stays cheap on 20MP exports (~2M samples max) while staying deterministic.
	step := 1
	if px := b.Dx() * b.Dy(); px > 2_000_000 {
		step = int(math.Ceil(math.Sqrt(float64(px) / 2_000_000)))
	}
	var hist [3][256]uint64
	var lumHist [256]uint64
	var n uint64
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl, _ := img.At(x, y).RGBA() // 16-bit per channel
			r8, g8, b8 := r>>8, g>>8, bl>>8
			hist[0][r8]++
			hist[1][g8]++
			hist[2][b8]++
			lum := (299*r8 + 587*g8 + 114*b8) / 1000 // Rec.601 luma
			lumHist[uint8(lum)]++
			n++
		}
	}
	var m finishMetrics
	if n == 0 {
		return m
	}
	for c := 0; c < 3; c++ {
		m.BlackClip[c] = float64(hist[c][0]) / float64(n)
		m.WhiteClip[c] = float64(hist[c][255]) / float64(n)
		m.Median[c] = float64(percentile(hist[c][:], n, 0.5)) / 255
	}
	m.Background = float64(percentile(lumHist[:], n, 0.10)) / 255
	m.GreenCast = m.Median[1] - (m.Median[0]+m.Median[2])/2
	return m
}

// percentile returns the 0..255 bin value at fraction p of a cumulative 256-bin histogram.
func percentile(hist []uint64, n uint64, p float64) int {
	target := uint64(p * float64(n))
	var cum uint64
	for v := 0; v < len(hist); v++ {
		cum += hist[v]
		if cum >= target {
			return v
		}
	}
	return len(hist) - 1
}

// thumbnailJPEG decodes the finish PNG and returns a downscaled JPEG (long side ≤ maxDim) for the
// vision model — a small payload keeps the request light and inference fast. Nearest-neighbour
// downscale (pure stdlib) is sufficient for an aesthetic check.
func thumbnailJPEG(path string, maxDim, quality int) ([]byte, error) {
	src, err := decodeImage(path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, downscale(src, maxDim), &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func downscale(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return src
	}
	scale := float64(maxDim) / float64(max(w, h))
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + int(float64(y)/scale)
		for x := 0; x < nw; x++ {
			dst.Set(x, y, src.At(b.Min.X+int(float64(x)/scale), sy))
		}
	}
	return dst
}
