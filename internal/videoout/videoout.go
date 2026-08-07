// Package videoout renders a shareable MP4 from a finished still image — a slow Ken-Burns
// pan/zoom — for the `video` / `both` output formats, via ffmpeg.
package videoout

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // header probe for RenderAuto
	_ "image/png"  // header probe for RenderAuto
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Options tunes the rendered clip.
type Options struct {
	Seconds int
	Width   int
	Height  int
}

// DefaultOptions returns a 12s 720p clip.
func DefaultOptions() Options {
	return Options{Seconds: 12, Width: 1280, Height: 720}
}

// defaultArea is the 720p-equivalent pixel budget OptionsFor sizes any aspect against.
const defaultArea = 1280 * 720

// OptionsFor sizes the clip to the SOURCE aspect ratio with the same pixel budget as the 720p
// default (both dims even for yuv420p): a 4:3 sensor or a mosaic union canvas is no longer
// stretched into 16:9. Non-positive dims fall back to DefaultOptions.
func OptionsFor(srcW, srcH int) Options {
	if srcW <= 0 || srcH <= 0 {
		return DefaultOptions()
	}
	aspect := float64(srcW) / float64(srcH)
	w := int(math.Sqrt(defaultArea * aspect))
	w -= w % 2
	h := int(float64(w) / aspect)
	h -= h % 2
	if w < 2 || h < 2 {
		return DefaultOptions()
	}
	return Options{Seconds: 12, Width: w, Height: h}
}

// RenderAuto probes the still's dimensions (PNG/JPEG header) and renders with the matching aspect.
func RenderAuto(ctx context.Context, ffmpegBin, image, out string) error {
	w, h := imageDims(image)
	return Render(ctx, ffmpegBin, image, out, OptionsFor(w, h))
}

// imageDims decodes just the image header ((0,0) on any error — OptionsFor then falls back).
func imageDims(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// Render produces a Ken-Burns MP4 from a still image.
func Render(ctx context.Context, ffmpegBin, image, out string, o Options) error {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpegBin, Args(image, out, o)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg render: %w\n%s", err, tail(string(b), 5))
	}
	return nil
}

// Args builds the ffmpeg argument list (pure, for testing). A single still is looped and slowly
// zoomed; upscaled first so the pan stays smooth on small astro frames.
func Args(image, out string, o Options) []string {
	if o.Seconds <= 0 {
		o = DefaultOptions()
	}
	frames := o.Seconds * 25
	vf := fmt.Sprintf(
		"scale=%d:-2,zoompan=z='min(zoom+0.0012,1.4)':d=%d:x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':s=%dx%d:fps=25,format=yuv420p",
		o.Width, frames, o.Width, o.Height)
	return []string{
		"-y", "-loop", "1", "-framerate", "25", "-t", strconv.Itoa(o.Seconds),
		"-i", image, "-vf", vf, "-c:v", "libx264", "-preset", "medium", "-crf", "23",
		"-pix_fmt", "yuv420p", out,
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// RenderSequence encodes an image sequence into an MP4, for outputs that are genuinely a time
// series rather than one still — a solar session's windows, for instance, where the motion in the
// prominences is the subject rather than a pan across a fixed frame.
//
// fps is derived from the frame count so a short session still yields a watchable clip instead of a
// half-second flicker.
func RenderSequence(ctx context.Context, ffmpegBin, pattern, out string, frames int) error {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpegBin, SequenceArgs(pattern, out, frames)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg sequence: %w\n%s", err, tail(string(b), 5))
	}
	return nil
}

// SequenceArgs builds the ffmpeg argument list for an image sequence (pure, for testing).
func SequenceArgs(pattern, out string, frames int) []string {
	fps := 8
	if frames > 0 && frames < 24 {
		// Below a couple of dozen frames a normal frame rate is over before the eye settles; slow it
		// down so every window is actually seen.
		fps = 4
	}
	return []string{
		"-y", "-framerate", strconv.Itoa(fps), "-i", pattern,
		// Even dimensions: yuv420p requires them and a solar crop is rarely even by luck.
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p",
		"-c:v", "libx264", "-preset", "medium", "-crf", "18", out,
	}
}
