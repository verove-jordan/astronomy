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
	BlackClip  [3]float64 `json:"black_clip"`  // fraction of pixels at 0, per channel R,G,B
	WhiteClip  [3]float64 `json:"white_clip"`  // fraction of pixels at 255, per channel R,G,B
	Median     [3]float64 `json:"median"`      // per-channel median, 0..1
	Background float64    `json:"background"`  // sky level estimate (10th-percentile luma), 0..1
	GreenCast  float64    `json:"green_cast"`  // medianG - mean(medianR, medianB); >0 → green cast
	WarmCast   float64    `json:"warm_cast"`   // sky red-excess: bgR - mean(bgG,bgB) on the per-channel 10th-pct background; >0 → warm/orange cast
	SignalCast float64    `json:"signal_cast"` // bright-signal (galaxy/star) green balance: 90th-pct G - mean(90th-pct R,B); <0 → magenta/pink signal, >0 → green
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
	// Warm/orange cast is measured on the SKY (per-channel 10th-percentile background), not the whole-frame
	// median — so legitimate red nebulosity (bright, not in the background) never reads as a cast.
	bgR := float64(percentile(hist[0][:], n, 0.10)) / 255
	bgG := float64(percentile(hist[1][:], n, 0.10)) / 255
	bgB := float64(percentile(hist[2][:], n, 0.10)) / 255
	m.WarmCast = bgR - (bgG+bgB)/2
	// Signal (galaxy/star) colour cast on the BRIGHT 90th-percentile per channel — not the sky median — so a
	// magenta/pink galaxy + discoloured stars register even when the sky median is neutral (the M31 failure).
	// <0 → magenta/pink signal, >0 → green signal.
	sigR := float64(percentile(hist[0][:], n, 0.90)) / 255
	sigG := float64(percentile(hist[1][:], n, 0.90)) / 255
	sigB := float64(percentile(hist[2][:], n, 0.90)) / 255
	m.SignalCast = sigG - (sigR+sigB)/2
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
// vision model — a small payload keeps the request light and inference fast.
func thumbnailJPEG(path string, maxDim, quality int) ([]byte, error) {
	src, err := decodeImage(path)
	if err != nil {
		return nil, err
	}
	return thumbnailJPEGImage(src, maxDim, quality)
}

// thumbnailJPEGImage is the in-memory core: downscale an already-decoded image (long side ≤ maxDim,
// nearest-neighbour — enough for an aesthetic check) and JPEG-encode it. Reused by the chat assistant.
func thumbnailJPEGImage(src image.Image, maxDim, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, downscale(src, maxDim), &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// centerCropJPEG returns a JPEG of the central frac×frac region of the finish PNG at native
// resolution (down to maxDim on the long side). The supervisor sends this alongside the full-frame
// thumbnail so the model can judge noise, star tightness and colour at 100% — detail the downscaled
// whole-frame view loses. frac in (0,1]; frac ≥ 1 crops nothing.
func centerCropJPEG(path string, frac float64, maxDim, quality int) ([]byte, error) {
	src, err := decodeImage(path)
	if err != nil {
		return nil, err
	}
	return centerCropJPEGImage(src, frac, maxDim, quality)
}

// centerCropJPEGImage is the in-memory core of centerCropJPEG (no file I/O), reused by the chat
// assistant to crop an already-decoded upload without re-reading disk.
func centerCropJPEGImage(src image.Image, frac float64, maxDim, quality int) ([]byte, error) {
	b := src.Bounds()
	if frac <= 0 || frac >= 1 {
		frac = 1
	}
	cw, ch := int(float64(b.Dx())*frac), int(float64(b.Dy())*frac)
	if cw < 1 || ch < 1 {
		return thumbnailJPEGImage(src, maxDim, quality) // degenerate crop → fall back to the full frame
	}
	x0 := b.Min.X + (b.Dx()-cw)/2
	y0 := b.Min.Y + (b.Dy()-ch)/2
	crop := image.NewRGBA(image.Rect(0, 0, cw, ch))
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			crop.Set(x, y, src.At(x0+x, y0+y))
		}
	}
	return thumbnailJPEGImage(crop, maxDim, quality)
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
