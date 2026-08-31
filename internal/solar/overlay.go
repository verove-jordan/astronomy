package solar

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// overlay.go renders what triage actually saw: the probe frame with the fitted limb drawn on it.
// A circle fit is the one thing in this package that cannot be trusted from a number alone — a
// plausible radius on a mis-centred circle looks fine in a table and ruins every frame it
// registers — so both the tests and the UI panel check it by eye.

// ProbeOverlay measures one capture file and writes a preview PNG with the fitted limb drawn over
// it, returning the probe. dst may be empty to measure without rendering.
func ProbeOverlay(ctx context.Context, ffmpegBin, path, dst string, twoBody bool) (FrameProbe, error) {
	scratch, err := os.MkdirTemp("", "solar-overlay-")
	if err != nil {
		return FrameProbe{}, err
	}
	defer os.RemoveAll(scratch)

	var im *fits.Image
	var scale float64
	var p FrameProbe
	if videoExts[strings.ToLower(filepath.Ext(path))] {
		info := probeVideo(ctx, ffmpegBin, path)
		frames, s, ferr := sampleVideoFrames(ctx, ffmpegBin, path, info, 1, scratch)
		if ferr != nil {
			return FrameProbe{}, ferr
		}
		im, scale = frames[0], s
		p = FrameProbe{Path: path, Kind: KindVideo, Video: &info}
	} else {
		meta := readStillMeta(path)
		i, s, lerr := loadStillProbe(ctx, path, scratch, meta)
		if lerr != nil {
			return FrameProbe{}, lerr
		}
		im, scale = i, s
		p = FrameProbe{Path: path, Kind: KindStill}
		applyMeta(&p, meta)
	}
	measure(&p, im, scale, twoBody)
	if dst == "" {
		return p, nil
	}
	// measure reports in full-resolution units; the overlay is drawn in probe units.
	d := p.Disc
	if scale > 0 {
		d.CX, d.CY, d.R = d.CX/scale, d.CY/scale, d.R/scale
	}
	return p, writeOverlayPNG(dst, im, d, p.DiscOK)
}

// writeOverlayPNG stretches a linear plane for display and draws the fitted circle and centre.
func writeOverlayPNG(dst string, im *fits.Image, d Limb, ok bool) error {
	rgb := image.NewRGBA(image.Rect(0, 0, im.W, im.H))
	hi := imgops.Percentile(imgops.Subsample(im.Pix[0], 200000), 99.5)
	if hi <= 0 {
		hi = 1
	}
	for i, v := range im.Pix[0] {
		// A square-root stretch: the disc is bright and the sky is near black, and a linear ramp
		// would show a white blob on black with nothing to judge the limb position against.
		g := uint8(255 * clamp01(math.Sqrt(math.Max(float64(v), 0)/hi)))
		rgb.Set(i%im.W, i/im.W, color.RGBA{R: g, G: g, B: g, A: 255})
	}
	if ok && d.R > 0 {
		drawCircle(rgb, d.CX, d.CY, d.R, color.RGBA{R: 0, G: 255, B: 90, A: 255})
		drawCross(rgb, d.CX, d.CY, 12, color.RGBA{R: 0, G: 255, B: 90, A: 255})
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := png.Encode(f, rgb); err != nil {
		return fmt.Errorf("encode overlay %s: %w", filepath.Base(dst), err)
	}
	return nil
}

// drawCircle strokes a circle, sampling densely enough that no pixel gap appears at any radius.
func drawCircle(img *image.RGBA, cx, cy, r float64, c color.RGBA) {
	steps := int(2*math.Pi*r) * 2
	if steps < 360 {
		steps = 360
	}
	for i := 0; i < steps; i++ {
		a := 2 * math.Pi * float64(i) / float64(steps)
		x, y := int(math.Round(cx+r*math.Cos(a))), int(math.Round(cy+r*math.Sin(a)))
		if x >= 0 && y >= 0 && x < img.Bounds().Dx() && y < img.Bounds().Dy() {
			img.SetRGBA(x, y, c)
		}
	}
}

// drawCross marks the fitted centre.
func drawCross(img *image.RGBA, cx, cy, half float64, c color.RGBA) {
	for d := -half; d <= half; d++ {
		for _, p := range [][2]int{{int(cx + d), int(cy)}, {int(cx), int(cy + d)}} {
			if p[0] >= 0 && p[1] >= 0 && p[0] < img.Bounds().Dx() && p[1] < img.Bounds().Dy() {
				img.SetRGBA(p[0], p[1], c)
			}
		}
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
