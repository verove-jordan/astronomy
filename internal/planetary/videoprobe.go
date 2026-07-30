package planetary

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 16-bit video extraction. ffmpeg's PNG default is 8-bit: every >8-bit planetary capture
// (12/14-bit SharpCap/FireCapture recordings) was silently truncated before stacking. The
// probe asks ffprobe for the stream's pixel format and requests a 16-bit PNG when the source
// carries more than 8 bits; every failure path soft-falls to the historical 8-bit behavior.

// ffprobeBinFor resolves the ffprobe binary: $FFPROBE_BIN, else the sibling of the configured
// ffmpeg binary (".../ffmpeg" → ".../ffprobe"), else plain "ffprobe" from PATH.
func ffprobeBinFor(ffmpegBin string) string {
	if env := os.Getenv("FFPROBE_BIN"); env != "" {
		return env
	}
	dir := filepath.Dir(ffmpegBin)
	if ffmpegBin == "" || dir == "." {
		return "ffprobe"
	}
	return filepath.Join(dir, "ffprobe")
}

// videoPixFmt probes the first video stream's pixel format. Soft-fail: any error returns ""
// and the extraction stays at ffmpeg's 8-bit default.
func videoPixFmt(ctx context.Context, ffprobeBin, path string) string {
	out, err := exec.CommandContext(ctx, ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=pix_fmt", "-of", "csv=p=0", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// pngPixFmtFor maps a source pixel format to the 16-bit PNG format the extraction should
// request: >8-bit gray sources → gray16be, >8-bit colour → rgb48be, 8-bit or unknown → ""
// (keep ffmpeg's default).
func pngPixFmtFor(pixFmt string) string {
	switch {
	case pixFmt == "" || !bitDepthOver8(pixFmt):
		return ""
	case strings.HasPrefix(pixFmt, "gray") || strings.HasPrefix(pixFmt, "ya"):
		return "gray16be"
	default:
		return "rgb48be"
	}
}

// bitDepthOver8 recognizes ffmpeg's >8-bit pixel-format naming: an explicit depth+endianness
// suffix (yuv420p10le, gray12be, p010le, …) or a 48/64-bit packed RGB(A) tag. Plain
// subsampling digits (yuv410p is 8-bit) never match.
func bitDepthOver8(pixFmt string) bool {
	for _, suffix := range []string{"10le", "10be", "12le", "12be", "14le", "14be", "16le", "16be"} {
		if strings.HasSuffix(pixFmt, suffix) {
			return true
		}
	}
	return strings.Contains(pixFmt, "48") || strings.Contains(pixFmt, "64")
}
