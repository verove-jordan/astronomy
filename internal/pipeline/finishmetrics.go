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

	// StarWarmFrac is the fraction of the brightest cores (≥99.5th-pct luma) that read warm
	// (red-dominant) — a UNIFORMLY warm star field is a calibration/finish failure the sky-median
	// metrics can't see ("all stars orange"). StarColorSpread is the spread (stddev of the r−b
	// balance) of those cores: real fields mix blue/white/orange stars, so a near-zero spread with a
	// high warm fraction means the finish painted them one colour.
	StarWarmFrac    float64 `json:"star_warm_frac"`
	StarColorSpread float64 `json:"star_color_spread"`
	// StarSatFrac is the fraction of the brightest cores whose colour saturation (max−min)/max exceeds
	// starSatHigh — a dense field of solid, over-saturated colour DISCS (the cluster failure: the thin RGB
	// base's per-star chroma spread over the L star profile by LAYER-MODE-LUMINANCE). BgChroma is the mean
	// chroma of the darkest ~quarter of pixels — the purple-green background MOTTLE of shallow colour subs
	// a dark stretch amplifies. Neither is visible to the sky-median casts above.
	StarSatFrac float64 `json:"star_sat_frac"`
	BgChroma    float64 `json:"bg_chroma"`
	// DetailIndex is the Laplacian variance of the sampled luma — an acutance proxy scored RELATIVE
	// to the run's first pass (absolute values are scene-dependent). Drives the planetary bonus.
	DetailIndex float64 `json:"detail_index"`
	// FgLumaMean is the mean luma of the bottom 20% rows — the milkyway foreground-balance guard
	// (crushed-to-black or lifted-HDR landscapes both read unnatural).
	FgLumaMean float64 `json:"fg_luma_mean"`
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
	var chromaByLum [256]float64 // Σ per-pixel chroma, binned by luma → BgChroma without a 2nd image pass
	var n uint64
	// The strided walk also builds a decimated luma grid (for the Laplacian detail index) and the
	// bottom-rows foreground mean, so the extra metrics cost no extra image pass.
	gw := (b.Dx() + step - 1) / step
	gh := (b.Dy() + step - 1) / step
	lumGrid := make([]float64, gw*gh)
	fgY := b.Min.Y + b.Dy()*4/5 // bottom 20% rows
	var fgSum float64
	var fgN uint64
	gy := 0
	for y := b.Min.Y; y < b.Max.Y; y += step {
		gx := 0
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl, _ := img.At(x, y).RGBA() // 16-bit per channel
			r8, g8, b8 := r>>8, g>>8, bl>>8
			hist[0][r8]++
			hist[1][g8]++
			hist[2][b8]++
			lum := (299*r8 + 587*g8 + 114*b8) / 1000 // Rec.601 luma
			lumHist[uint8(lum)]++
			chromaByLum[uint8(lum)] += (absU(r8, g8) + absU(g8, b8) + absU(b8, r8)) / (3 * 255) // 0..~0.67
			if gy < gh && gx < gw {
				lumGrid[gy*gw+gx] = float64(lum) / 255
			}
			if y >= fgY {
				fgSum += float64(lum) / 255
				fgN++
			}
			n++
			gx++
		}
		gy++
	}
	var m finishMetrics
	if n == 0 {
		return m
	}
	m.DetailIndex = laplacianVar(lumGrid, gw, gh)
	if fgN > 0 {
		m.FgLumaMean = fgSum / float64(fgN)
	}
	m.StarWarmFrac, m.StarColorSpread, m.StarSatFrac = starCoreColor(img, b, step, lumHist[:], n)
	// Background chroma mottle: mean per-pixel chroma over the darkest quarter of the frame (luma at/below
	// the 25th percentile) — coloured noise a dark stretch amplifies where the sky should be neutral grey.
	thr25 := percentile(lumHist[:], n, 0.25)
	var chSum float64
	var chN uint64
	for v := 0; v <= thr25; v++ {
		chSum += chromaByLum[v]
		chN += lumHist[v]
	}
	if chN > 0 {
		m.BgChroma = chSum / float64(chN)
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

// laplacianVar is the 4-neighbour Laplacian variance of a luma grid — the acutance proxy behind
// DetailIndex. Deterministic and cheap (the grid is the strided sample, ≤ ~2M cells).
func laplacianVar(grid []float64, w, h int) float64 {
	if w < 3 || h < 3 {
		return 0
	}
	var sum, sum2 float64
	n := 0
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			lap := 4*grid[row+x] - grid[row+x-1] - grid[row+x+1] - grid[row-w+x] - grid[row+w+x]
			sum += lap
			sum2 += lap * lap
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / float64(n)
	return sum2/float64(n) - mean*mean
}

// starSatHigh is the colour-saturation (max−min)/max above which a bright star core reads as a solid
// colour DISC rather than a naturally tinted star — the threshold behind StarSatFrac.
const starSatHigh = 0.45

// starCoreColor samples the brightest cores (≥99.5th-pct luma) and reports how many read warm
// (red-dominant), the spread of their red−blue balance, and the fraction that are over-saturated colour
// discs (saturation above starSatHigh). A capped second strided pass.
func starCoreColor(img image.Image, b image.Rectangle, step int, lumHist []uint64, n uint64) (warmFrac, spread, satFrac float64) {
	thr := uint32(percentile(lumHist, n, 0.995))
	if thr < 32 {
		return 0, 0, 0 // a near-black frame has no meaningful "bright cores"
	}
	const maxSamples = 50_000
	var balances []float64
	warm, satHigh := 0, 0
	for y := b.Min.Y; y < b.Max.Y && len(balances) < maxSamples; y += step {
		for x := b.Min.X; x < b.Max.X && len(balances) < maxSamples; x += step {
			r, g, bl, _ := img.At(x, y).RGBA()
			r8, g8, b8 := r>>8, g>>8, bl>>8
			lum := (299*r8 + 587*g8 + 114*b8) / 1000
			if lum < thr {
				continue
			}
			bal := (float64(r8) - float64(b8)) / 255
			balances = append(balances, bal)
			if bal > 0.08 && r8 > g8 {
				warm++
			}
			hi := max(r8, max(g8, b8))
			lo := min(r8, min(g8, b8))
			if hi > 0 && (float64(hi)-float64(lo))/float64(hi) > starSatHigh {
				satHigh++
			}
		}
	}
	if len(balances) < 20 {
		return 0, 0, 0
	}
	warmFrac = float64(warm) / float64(len(balances))
	satFrac = float64(satHigh) / float64(len(balances))
	var mean float64
	for _, v := range balances {
		mean += v
	}
	mean /= float64(len(balances))
	var varr float64
	for _, v := range balances {
		varr += (v - mean) * (v - mean)
	}
	spread = math.Sqrt(varr / float64(len(balances)))
	return warmFrac, spread, satFrac
}

// absU is the absolute difference of two uint32 channel values as a float (chroma accumulation).
func absU(a, b uint32) float64 {
	if a > b {
		return float64(a - b)
	}
	return float64(b - a)
}
