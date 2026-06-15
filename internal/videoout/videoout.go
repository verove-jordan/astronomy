// Package videoout renders a shareable MP4 from a finished still image — a slow Ken-Burns
// pan/zoom — for the `video` / `both` output formats, via ffmpeg.
package videoout

import (
	"context"
	"fmt"
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
