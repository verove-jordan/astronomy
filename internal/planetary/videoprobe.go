package planetary

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// videoFrameCount probes the first video stream's frame count. Soft-fail: any error (or a stream
// that does not declare one) returns 0 and the caller extracts every frame as it always did.
func videoFrameCount(ctx context.Context, ffprobeBin, path string) int {
	out, err := exec.CommandContext(ctx, ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=nb_frames", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(firstCSVField(string(out)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// firstCSVField takes ffprobe's first CSV value. `-of csv=p=0` still emits the field SEPARATOR, so a
// single-value query comes back as "33147," — which parses as an error, and an unparsed frame count
// silently disables the extraction budget. That is not hypothetical: it filled a disk with 12,000
// 4K frames of a clip that should have been sampled down to a few hundred.
func firstCSVField(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// videoFrameSize probes the first video stream's coded frame size (0,0 when unknown).
func videoFrameSize(ctx context.Context, ffprobeBin, path string) (w, h int) {
	out, err := exec.CommandContext(ctx, ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, 0
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) < 2 { // ffprobe appends a trailing separator, so ≥2 rather than ==2
		return 0, 0
	}
	w, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	h, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
	return w, h
}

// Frame-extraction budget. A phone shooting 4K120 writes 33,000 frames in four minutes; extracted
// as PNG that is >100 GB of scratch for a stack that gains nothing past a few thousand samples. The
// budget is stated in PIXELS rather than frames so a small planetary ROI still keeps thousands of
// frames while a 4K clip is decimated: maxFrames = clamp(videoPixelBudget/(w·h), minVideoFrames,
// maxVideoFrames). An unknown frame size falls back to maxVideoFrames.
const (
	// Sized from what the frames cost DOWNSTREAM, not from the PNGs: Siril converts each extracted
	// frame to a 16-bit FITS, so one 3840×2160 colour frame becomes ~50 MB on disk. 5 gigapixels is
	// ~600 4K frames (~30 GB of scratch) — enough to give a swept clip's two dozen panels ~40 frames
	// each, which is a real lucky-imaging stack, without filling the disk.
	videoPixelBudget = 5e9
	minVideoFrames   = 120 // below this a lucky-imaging stack is not worth running at all
	maxVideoFrames   = 6000
	// scratchShare is the fraction of free space one video extraction may claim; fitsOverhead covers
	// the extracted PNGs and the per-panel aligned copies that live alongside the converted FITS.
	scratchShare = 0.45
	fitsOverhead = 1.4
)

// videoFrameStep returns the ffmpeg `framestep` divisor that keeps an evenly-spaced sample of at
// most budget frames, and the resulting kept count. step 1 means "keep every frame" (no filter).
// Sampling evenly across the clip — rather than truncating it — is what keeps a hand-swept capture
// covering the whole sweep instead of only its first seconds.
func videoFrameStep(total, budget int) (step, kept int) {
	if total <= 0 || budget <= 0 || total <= budget {
		return 1, total
	}
	step = (total + budget - 1) / budget
	return step, (total + step - 1) / step
}

// videoFrameBudget converts a probed frame size into the frame budget, then bounds it by the scratch
// space actually available (freeBytes; pass 0 to skip that bound).
//
// The disk bound is the one that matters. A fixed budget assumes a machine with room; the run that
// forced this had 18 GB free and would have written far more, and the failure mode of guessing wrong
// is not a bad stack but a full filesystem. Each extracted frame costs about w·h·3·2 bytes once
// Siril converts it to a 16-bit RGB FITS, and only a share of what is free may be spent.
func videoFrameBudget(w, h int, freeBytes uint64) int {
	if w <= 0 || h <= 0 {
		return minVideoFrames
	}
	b := int(videoPixelBudget / (float64(w) * float64(h)))
	if freeBytes > 0 {
		perFrame := float64(w) * float64(h) * 3 * 2 * fitsOverhead
		if fit := int(float64(freeBytes) * scratchShare / perFrame); fit < b {
			b = fit
		}
	}
	if b < minVideoFrames {
		return minVideoFrames
	}
	if b > maxVideoFrames {
		return maxVideoFrames
	}
	return b
}

// freeSpaceBytes reports the space available on the filesystem holding dir (0 when unknown).
func freeSpaceBytes(dir string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}
