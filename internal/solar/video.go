// Package solar implements the Sun-specific half of the `sun` stacking mode: capture triage
// (which files can be stacked with which), iPhone/ASI ingest, sub-pixel limb geometry, and the
// deterministic solar finish. The lucky-imaging frame engine itself is reused from
// internal/planetary — none of it is Moon-specific; only the finish is.
package solar

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// VideoInfo is what ffprobe tells us about a clip before we decode a single frame. Everything here
// feeds either grouping (dimensions, rotation) or correctness (transfer, range, bit depth).
type VideoInfo struct {
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	FPS         float64 `json:"fps,omitempty"`
	DurationSec float64 `json:"duration_sec,omitempty"`
	Frames      int     `json:"frames,omitempty"` // derived from FPS×duration; nb_frames is often absent
	Codec       string  `json:"codec,omitempty"`
	PixFmt      string  `json:"pix_fmt,omitempty"`
	Transfer    string  `json:"transfer,omitempty"`   // color_transfer: smpte2084 (PQ), arib-std-b67 (HLG), bt709…
	ColorRange  string  `json:"range,omitempty"`      // "tv" (limited) or "pc" (full); empty ⇒ assume tv
	Rotation    int     `json:"rotation,omitempty"`   // display-matrix rotation in degrees
	BitDepth    int     `json:"bit_depth,omitempty"`  // 8, 10 or 12, parsed from PixFmt
	CreatedMs   int64   `json:"created_ms,omitempty"` // container creation_time, Unix ms
	// LatDeg/LonDeg/ElevM come from the QuickTime ISO 6709 location tag a phone writes into its own
	// recordings. They are carried because an eclipse sequence needs to know WHERE the clip was
	// shot: the Moon's parallax is nearly a degree, so the phase at a given second is a different
	// number a few hundred kilometres away. HasSite distinguishes "at the prime meridian on the
	// equator" from "no tag".
	LatDeg  float64 `json:"lat_deg,omitempty"`
	LonDeg  float64 `json:"lon_deg,omitempty"`
	ElevM   float64 `json:"elev_m,omitempty"`
	HasSite bool    `json:"has_site,omitempty"`
}

// ffprobeBin resolves the ffprobe next to the configured ffmpeg, falling back to PATH.
func ffprobeBin(ffmpegBin string) string {
	if ffmpegBin == "" || filepath.Base(ffmpegBin) == ffmpegBin {
		return "ffprobe"
	}
	return filepath.Join(filepath.Dir(ffmpegBin), "ffprobe")
}

// probeVideo reads the first video stream's parameters. Every field soft-fails to its zero value:
// a clip we cannot probe is still ingestable, just with conservative defaults.
func probeVideo(ctx context.Context, ffmpegBin, path string) VideoInfo {
	args := []string{
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,pix_fmt,color_transfer,color_range,codec_name,duration",
		"-show_entries", "stream_side_data=rotation",
		"-show_entries", "format=duration",
		"-show_entries", "format_tags=creation_time,com.apple.quicktime.location.ISO6709",
		"-of", "default=nw=1",
		path,
	}
	out, err := exec.CommandContext(ctx, ffprobeBin(ffmpegBin), args...).Output()
	if err != nil {
		return VideoInfo{}
	}
	return parseProbe(string(out))
}

// parseProbe turns ffprobe's `key=value` lines into a VideoInfo (pure, for testing). Duration is
// emitted by both the stream and the format section; the last non-empty one wins, which is the
// format duration — the reliable one for a QuickTime container.
func parseProbe(s string) VideoInfo {
	var v VideoInfo
	for _, line := range strings.Split(s, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || val == "" || val == "N/A" {
			continue
		}
		switch key {
		case "width":
			v.Width, _ = strconv.Atoi(val)
		case "height":
			v.Height, _ = strconv.Atoi(val)
		case "r_frame_rate":
			v.FPS = parseRate(val)
		case "pix_fmt":
			v.PixFmt = val
		case "color_transfer":
			v.Transfer = val
		case "color_range":
			v.ColorRange = val
		case "codec_name":
			v.Codec = val
		case "duration":
			if f, err := strconv.ParseFloat(val, 64); err == nil && f > 0 {
				v.DurationSec = f
			}
		case "rotation":
			v.Rotation, _ = strconv.Atoi(val)
		case "creation_time", "TAG:creation_time":
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				v.CreatedMs = t.UnixMilli()
			}
		case "com.apple.quicktime.location.ISO6709", "TAG:com.apple.quicktime.location.ISO6709":
			if lat, lon, elev, ok := parseISO6709(val); ok {
				v.LatDeg, v.LonDeg, v.ElevM, v.HasSite = lat, lon, elev, true
			}
		}
	}
	v.BitDepth = bitDepthOf(v.PixFmt)
	if v.FPS > 0 && v.DurationSec > 0 {
		v.Frames = int(v.FPS*v.DurationSec + 0.5)
	}
	return v
}

// parseRate parses ffprobe's "num/den" frame-rate form.
func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	if !ok {
		return n
	}
	d, err := strconv.ParseFloat(den, 64)
	if err != nil || d == 0 {
		return 0
	}
	return n / d
}

// bitDepthOf reads the component depth out of an ffmpeg pixel-format name (yuv420p10le → 10).
// Unsuffixed names are 8-bit.
func bitDepthOf(pixFmt string) int {
	for _, d := range []int{16, 14, 12, 10, 9} {
		tag := strconv.Itoa(d)
		if strings.Contains(pixFmt, "p"+tag) || strings.HasSuffix(pixFmt, tag+"le") || strings.HasSuffix(pixFmt, tag+"be") {
			return d
		}
	}
	if pixFmt == "" {
		return 0
	}
	return 8
}

// IsHDR reports whether the clip's transfer function is a HDR one we must invert before stacking.
// The host ffmpeg has no zscale, so the inversion happens in Go (see transfer.go) — and it is not
// optional: every frame of an HLG clip is tone-mapped, and stacking tone-mapped pixels is wrong.
func (v VideoInfo) IsHDR() bool { return transferKind(v.Transfer) != transferSDR }

// FullRange reports whether luma spans 0..max rather than the limited 16..235-equivalent range.
// ffmpeg omits color_range on plenty of QuickTime files; limited is the safe assumption there,
// because treating limited data as full merely fails to expand it, while the reverse clips.
func (v VideoInfo) FullRange() bool { return v.ColorRange == "pc" || v.ColorRange == "jpeg" }

// parseISO6709 reads the ISO 6709 point a phone stamps on a recording, e.g. "+47.2783-002.4948/"
// or "+47.2783-002.4948+0020.000/". Signed decimal degrees only - the degrees-minutes-seconds forms
// the standard also allows have never been seen from an iPhone, and guessing at them would risk
// silently misplacing an observer by tens of kilometres, which is worse than having no site at all.
func parseISO6709(s string) (latDeg, lonDeg, elevM float64, ok bool) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "/")
	if s == "" {
		return 0, 0, 0, false
	}
	fields := splitSigned(s)
	if len(fields) < 2 {
		return 0, 0, 0, false
	}
	lat, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || lat < -90 || lat > 90 {
		return 0, 0, 0, false
	}
	lon, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || lon < -180 || lon > 180 {
		return 0, 0, 0, false
	}
	if len(fields) > 2 {
		elevM, _ = strconv.ParseFloat(fields[2], 64)
	}
	return lat, lon, elevM, true
}

// splitSigned cuts a run of signed decimals ("+47.2783-002.4948+0020") at each sign.
func splitSigned(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		if r != '+' && r != '-' {
			continue
		}
		if start >= 0 {
			out = append(out, s[start:i])
		}
		start = i
	}
	if start >= 0 && start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
