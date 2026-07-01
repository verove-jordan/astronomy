// assist.go powers the AstroAgent chat: a factual, image-specific critique. It measures a browser
// upload with the same pixel statistics the finish supervisor uses (finishMetrics) plus a trail check
// and a large-scale gradient figure, and pairs it with a system prompt that forbids generic answers
// and prescribes fixes in AstroStack's tier A/B/C knob + tool vocabulary. Pure Go, soft-fail.
package pipeline

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/grade"
)

const (
	assistCropFrac    = 0.4 // central fraction sent at 100% (matches the supervisor)
	assistCropDim     = 900 // long-side px cap of the centre crop
	assistCropQuality = 88
	assistGridDim     = 256 // long side of the pooled luma grid for trail + gradient
)

// AssistSystemPrompt grounds the AstroAgent chat: critique THIS image from the supplied measurements,
// no textbook lists, fixes in AstroStack's tier A/B/C vocabulary. It embeds the shared tierKnobMenu +
// defectVocabulary (prompts.go) so it never drifts from the supervisor's prompt.
const AssistSystemPrompt = assistPromptIntro + tierKnobMenu + assistPromptMappings + defectVocabulary + assistPromptOutput

const assistPromptIntro = `You are AstroAgent, an expert astrophotography image-processing assistant built into AstroStack. The user uploads a stacked or processed astro image and asks about it. Your job is a FACTUAL, image-specific technical critique of THIS image and exactly how to fix it with AstroStack's own controls — never a generic textbook answer.

GROUND TRUTH. Each user image is followed by a line "AstroStack measurements of this image" holding objective pixel stats measured server-side from that exact image (fractions/levels in 0..1 unless noted): background (10th-percentile luma); median R/G/B; green_cast = medianG - mean(medianR,medianB) (positive = green, negative = magenta); black_clip / white_clip (fraction of pixels crushed to 0 / blown to 255, per channel); gradient (large-scale corner-vs-centre background unevenness, %); trail (a detected satellite or plane streak). Treat these numbers as ground truth: when they disagree with your visual impression, the numbers win. If no measurement line is present, say so and judge only what the pixels support.

WHAT YOU MAY JUDGE — only these, and only from the image plus its measurements: background level and neutrality, colour cast, black/white clipping, large-scale gradients, saturation, chroma and luminance noise (judge from the 100% crop), star size/bloat, star dominance, halos around stars, blown object cores, and trail / edge / stacking-border artifacts.

HARD ANTI-GENERIC RULE. Do NOT produce a textbook checklist. Name ONLY defects that are actually visible in THIS image OR supported by its measurements, and cite the confirming evidence inline (the measurement value, or a concrete location in the frame). If an axis is fine, say it is clean and move on. Never invent problems, and never speculate about what a single stacked export cannot show (star alignment, rotation, registration, frame count, guiding). Reporting FEWER, well-supported findings is correct and expected.

PRESCRIBE FIXES IN ASTROSTACK'S VOCABULARY. Every fix must name a specific AstroStack knob (with a direction or target value and its tier) and/or a specific external tool AstroStack drives (Siril, GraXpert, StarNet++). Prefer the cheapest tier that fixes the defect. The controls:

`

const assistPromptMappings = `

Typical mappings: green or magenta cast -> color_calibration (SPCC, tier B) or lower saturation (tier A); brown or too-bright sky -> lower background_level plus combined_background_ai (B); large-scale gradient -> combined_background_ai / background_degree (B) or a GraXpert background-extraction; chroma noise -> color_denoise_ai (B) or chroma_blur (A); luminance noise or thin data -> more integration or denoise_lum (C); blown core -> core_highlight_knee/ceil (A); bloated or dominant stars -> star_reduce (B, StarNet++); crushed shadows -> raise background_level (B); ragged edges -> crop_frac (A); trail residue -> a trail_mask_k re-stack (C). Diagnose with this defect vocabulary where useful: `

const assistPromptOutput = `.

OUTPUT STRUCTURE (tight, in the user's language):
1. Overall — one or two sentences: what this image is and its overall state.
2. Findings — one short bullet per real defect, worst first: what is visible plus the confirming measurement, then the concrete AstroStack fix (knob + direction/value + tier, or tool). If an axis is clean, say so in one clause instead of padding.
3. Next step — optionally one sentence naming the single highest-impact action.

Answer in the SAME LANGUAGE the user wrote in. If the user's goal or capture mode (deepsky / nebula / milkyway / planetary) is unclear and it would change your advice, ask ONE brief clarifying question, then still give a best-effort critique. Be concise and technical: no filler, no hedging disclaimers.`

// AssistMeasurement is the objective per-image readout the chat computes: fed to the model as ground
// truth and returned to the UI's stats panel.
type AssistMeasurement struct {
	Background  float64    `json:"background"`   // sky level (10th-percentile luma), 0..1
	MedianRGB   [3]float64 `json:"median_rgb"`   // per-channel median, 0..1
	GreenCast   float64    `json:"green_cast"`   // medianG - mean(medianR,medianB); >0 green, <0 magenta
	BlackClip   [3]float64 `json:"black_clip"`   // fraction of pixels at 0, per channel R,G,B
	WhiteClip   [3]float64 `json:"white_clip"`   // fraction of pixels at 255, per channel R,G,B
	GradientPct float64    `json:"gradient_pct"` // large-scale corner-vs-centre background unevenness, %
	Trail       bool       `json:"trail"`        // a satellite/plane streak was detected
	TrailSpan   float64    `json:"trail_span"`   // trail length score from DetectTrail (0 when none)
}

// AnalyzeAssistImage measures a browser-uploaded PNG/JPEG for the chat: it returns the objective
// stats, a compact one-line report to inject as ground truth, and a 100% centre-crop JPEG for
// noise/star/colour inspection. The crop is soft-fail (nil on error, report still returned); an
// undecodable image returns an error so the caller can forward the raw upload without a report.
func AnalyzeAssistImage(data []byte) (AssistMeasurement, string, []byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return AssistMeasurement{}, "", nil, fmt.Errorf("decode assist image: %w", err)
	}
	fm := metricsFromImage(img)
	maxGrid, meanGrid, w, h := lumaGrids(img, assistGridDim)
	trail, span := grade.DetectTrail(maxGrid, w, h)
	meas := AssistMeasurement{
		Background:  fm.Background,
		MedianRGB:   fm.Median,
		GreenCast:   fm.GreenCast,
		BlackClip:   fm.BlackClip,
		WhiteClip:   fm.WhiteClip,
		GradientPct: backgroundGradientPct(meanGrid, w, h),
		Trail:       trail,
		TrailSpan:   span,
	}
	crop, _ := centerCropJPEGImage(img, assistCropFrac, assistCropDim, assistCropQuality) // nil on failure
	return meas, formatAssistReport(meas), crop, nil
}

// formatAssistReport renders the ground-truth line. Pure, so the exact wording is unit-testable and
// mirrors the supervisor's "Measured metrics ... (fractions/levels in 0..1)" framing.
func formatAssistReport(m AssistMeasurement) string {
	trail := "none"
	if m.Trail {
		trail = fmt.Sprintf("detected (span %.2f)", m.TrailSpan)
	}
	return fmt.Sprintf(
		"AstroStack measurements of this image (objective pixel stats; fractions/levels in 0..1 unless noted — treat as ground truth): "+
			"background=%.3f | median R/G/B=%.3f/%.3f/%.3f | green_cast=%+.3f | black_clip R/G/B=%.3f/%.3f/%.3f | "+
			"white_clip R/G/B=%.3f/%.3f/%.3f | gradient=%.1f%% | trail=%s",
		m.Background, m.MedianRGB[0], m.MedianRGB[1], m.MedianRGB[2], m.GreenCast,
		m.BlackClip[0], m.BlackClip[1], m.BlackClip[2],
		m.WhiteClip[0], m.WhiteClip[1], m.WhiteClip[2],
		m.GradientPct, trail)
}

// lumaGrids builds two Rec.601 luma grids (long side ≤ maxDim) in one strided pass: a max-pooled grid
// (preserves thin bright streaks for DetectTrail) and a mean-pooled grid (approximates the local
// background for the gradient figure). Striding keeps it cheap (~2M samples) on large exports.
func lumaGrids(img image.Image, maxDim int) (maxGrid, meanGrid []float64, w, h int) {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return nil, nil, 0, 0
	}
	scale := 1.0
	if m := max(sw, sh); m > maxDim {
		scale = float64(maxDim) / float64(m)
	}
	w = max(1, int(float64(sw)*scale))
	h = max(1, int(float64(sh)*scale))
	maxGrid = make([]float64, w*h)
	meanGrid = make([]float64, w*h)
	count := make([]int, w*h)
	step := 1
	if px := sw * sh; px > 2_000_000 {
		step = int(math.Ceil(math.Sqrt(float64(px) / 2_000_000)))
	}
	for y := b.Min.Y; y < b.Max.Y; y += step {
		oy := min(h-1, (y-b.Min.Y)*h/sh)
		for x := b.Min.X; x < b.Max.X; x += step {
			ox := min(w-1, (x-b.Min.X)*w/sw)
			r, g, bl, _ := img.At(x, y).RGBA()
			l := (299*float64(r>>8) + 587*float64(g>>8) + 114*float64(bl>>8)) / 1000 / 255
			i := oy*w + ox
			if l > maxGrid[i] {
				maxGrid[i] = l
			}
			meanGrid[i] += l
			count[i]++
		}
	}
	for i := range meanGrid {
		if count[i] > 0 {
			meanGrid[i] /= float64(count[i])
		}
	}
	return maxGrid, meanGrid, w, h
}

// backgroundGradientPct estimates large-scale background unevenness: the 25th-percentile luma in the
// four corner thirds and the centre third of the mean-pooled grid, returned as the relative span
// (max-min)/max in percent. A flat sky → ~0%; a strong corner gradient → tens of %.
func backgroundGradientPct(meanGrid []float64, w, h int) float64 {
	if w < 3 || h < 3 || len(meanGrid) == 0 {
		return 0
	}
	tileBackground := func(cx, cy int) float64 { // 25th-pct luma of the cx,cy third (0..2 each axis)
		var vals []float64
		for y := cy * h / 3; y < (cy+1)*h/3; y++ {
			for x := cx * w / 3; x < (cx+1)*w/3; x++ {
				vals = append(vals, meanGrid[y*w+x])
			}
		}
		if len(vals) == 0 {
			return 0
		}
		sort.Float64s(vals)
		return vals[len(vals)/4]
	}
	regions := []float64{
		tileBackground(0, 0), tileBackground(2, 0), tileBackground(0, 2), tileBackground(2, 2), // corners
		tileBackground(1, 1), // centre
	}
	lo, hi := regions[0], regions[0]
	for _, v := range regions {
		lo = min(lo, v)
		hi = max(hi, v)
	}
	if hi <= 0 {
		return 0
	}
	return (hi - lo) / hi * 100
}
