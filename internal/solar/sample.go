package solar

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// sample.go pulls a handful of representative frames out of a clip for triage. It never decodes the
// whole video: a 25 s 4K120 capture is ~3000 frames, and measuring all of them to decide which
// group the clip belongs to would cost more than the stack that follows.

// probeFramesPerClip is how many evenly spaced frames triage measures per clip — enough to get a
// stable disc radius and to notice the exposure or focus drifting mid-clip.
const probeFramesPerClip = 5

// displayDims returns the frame size after the container's display-matrix rotation is applied,
// which is what ffmpeg emits by default and therefore what every measurement is in.
func displayDims(v VideoInfo) (w, h int) {
	if r := ((v.Rotation % 360) + 360) % 360; r == 90 || r == 270 {
		return v.Height, v.Width
	}
	return v.Width, v.Height
}

// probeScaleFor picks the output size for a probe frame: the long edge capped at probeMaxEdge, both
// dimensions even (yuv-safe), plus the factor converting probe pixels back to full resolution.
func probeScaleFor(w, h int) (ow, oh int, scale float64) {
	long := w
	if h > long {
		long = h
	}
	if long <= 0 {
		return 0, 0, 1
	}
	if long <= probeMaxEdge {
		return w, h, 1
	}
	f := float64(long) / float64(probeMaxEdge)
	ow, oh = int(float64(w)/f)&^1, int(float64(h)/f)&^1
	if ow < 8 || oh < 8 {
		return w, h, 1
	}
	return ow, oh, float64(w) / float64(ow)
}

// sampleVideoFrames extracts n evenly spaced frames as linear-light luminance planes, along with
// the factor converting their coordinates to full resolution.
func sampleVideoFrames(ctx context.Context, ffmpegBin, path string, info VideoInfo, n int, scratch string) ([]*fits.Image, float64, error) {
	if info.DurationSec <= 0 || n <= 0 {
		return nil, 1, fmt.Errorf("no duration for %s", filepath.Base(path))
	}
	dw, dh := displayDims(info)
	ow, oh, scale := probeScaleFor(dw, dh)
	out := make([]*fits.Image, 0, n)
	for i := 0; i < n; i++ {
		// Sample inside the clip, never at the very edges: the first and last frames of a handheld
		// phone capture are the ones with the finger still on the shutter.
		t := info.DurationSec * (float64(i) + 0.5) / float64(n)
		im, err := extractProbeFrame(ctx, ffmpegBin, path, t, ow, oh, scratch, i)
		if err != nil {
			continue // a single unreadable sample must not disqualify the clip
		}
		Linearize(im.Pix[0], info)
		out = append(out, im)
	}
	if len(out) == 0 {
		return nil, scale, fmt.Errorf("no readable frames in %s", filepath.Base(path))
	}
	return out, scale, nil
}

// extractProbeFrame pulls one frame at timestamp t as a scaled 16-bit luma PNG and decodes it.
//
// It takes the LUMA PLANE rather than an RGB conversion: an iPhone clip is 4:2:0 or 4:2:2, so
// chroma is half or quarter resolution, and through a deep-red Hα etalon an RGB average would
// dilute the only signal-bearing channel with interpolated chroma. Y is the full-resolution,
// least-processed plane available.
//
// Two ffmpeg behaviours are load-bearing here, both measured rather than assumed (see
// TestExtract_LimitedRangePreserved):
//
//   - `-pix_fmt gray16be` is used instead of the more obvious `extractplanes=y` filter, because
//     extractplanes rejects ProRes' yuv422p10le in ffmpeg 8.x — half a real session's clips — while
//     the pixel-format route handles 4:2:0, 4:2:2 and 8-bit alike.
//   - The 16-bit gray conversion does NOT expand limited range (black stays at ~0.0636, the 16/255
//     floor), so expansion happens in Go where it is explicit and testable. The 8-bit `gray`
//     conversion *does* expand — relying on swscale to guess would silently change the black level
//     with the pixel format, and a wrong pedestal quietly damps every deconvolution downstream.
func extractProbeFrame(ctx context.Context, ffmpegBin, path string, t float64, ow, oh int, scratch string, idx int) (*fits.Image, error) {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	dst := filepath.Join(scratch, fmt.Sprintf("%s_p%02d.png", sanitizeName(path), idx))
	defer os.Remove(dst)
	args := []string{
		"-y", "-ss", strconv.FormatFloat(t, 'f', 3, 64), "-i", path, "-frames:v", "1",
	}
	if ow > 0 && oh > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", ow, oh))
	}
	args = append(args, "-pix_fmt", "gray16be", dst)
	if out, err := exec.CommandContext(ctx, ffmpegBin, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg probe frame: %w\n%s", err, tailLines(string(out), 4))
	}
	return decodeLuminance(dst)
}

// probeVideoFile measures a clip: probe its parameters, sample a few frames, and report the median
// of their measurements so one bad sample cannot define the clip.
func probeVideoFile(ctx context.Context, ffmpegBin, path, scratch string, twoBody bool) FrameProbe {
	p := FrameProbe{Path: path, Kind: KindVideo}
	info := probeVideo(ctx, ffmpegBin, path)
	p.Video = &info
	dw, dh := displayDims(info)
	p.Width, p.Height, p.TakenAtMs = dw, dh, info.CreatedMs
	if info.Frames == 0 {
		p.Err = "unreadable video stream"
		return p
	}
	frames, scale, err := sampleVideoFrames(ctx, ffmpegBin, path, info, probeFramesPerClip, scratch)
	if err != nil {
		p.Err = err.Error()
		return p
	}
	measureSamples(&p, frames, scale, twoBody)
	return p
}

// measureSamples measures every sampled frame and keeps the median of each figure, so a single
// cloud-crossed or mis-seeked sample cannot skew the clip's group assignment.
func measureSamples(p *FrameProbe, frames []*fits.Image, scale float64, twoBody bool) {
	probes := make([]FrameProbe, 0, len(frames))
	for _, im := range frames {
		var q FrameProbe
		measure(&q, im, scale, twoBody)
		if q.DiscOK {
			probes = append(probes, q)
		}
	}
	if len(probes) == 0 {
		return
	}
	p.DiscOK = true
	p.Disc.CX = medianOf(probes, func(q FrameProbe) float64 { return q.Disc.CX })
	p.Disc.CY = medianOf(probes, func(q FrameProbe) float64 { return q.Disc.CY })
	p.Disc.R = medianOf(probes, func(q FrameProbe) float64 { return q.Disc.R })
	p.Disc.ArcDeg = medianOf(probes, func(q FrameProbe) float64 { return q.Disc.ArcDeg })
	p.Disc.ResidRMS = medianOf(probes, func(q FrameProbe) float64 { return q.Disc.ResidRMS })
	p.ArcsecPerPx = medianOf(probes, func(q FrameProbe) float64 { return q.ArcsecPerPx })
	p.ClippedFrac = medianOf(probes, func(q FrameProbe) float64 { return q.ClippedFrac })
	p.OnDiscMedian = medianOf(probes, func(q FrameProbe) float64 { return q.OnDiscMedian })
	p.Detail = medianOf(probes, func(q FrameProbe) float64 { return q.Detail })
	p.LimbRatio = medianOf(probes, func(q FrameProbe) float64 { return q.LimbRatio })
	p.DiscFill = medianOf(probes, func(q FrameProbe) float64 { return q.DiscFill })
	for _, q := range probes {
		p.Disc.Partial = p.Disc.Partial || q.Disc.Partial
	}
}
