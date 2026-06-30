// Package planetary processes lunar/planetary videos with lucky imaging: extract frames
// (ffmpeg for MP4/MOV/MKV/AVI; SER read by Siril), convert to a FITS sequence, rank frames by
// sharpness in Go, keep the best, stack them (no registration — surfaces have no stars), then
// sharpen, stretch and export. High-precision multi-point planetary alignment is out of scope
// (a known Siril-CLI limitation); use the Siril GUI or AutoStakkert! for that.
package planetary

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Options tunes the lucky-imaging run.
type Options struct {
	BestPercent int      // keep this percent of the sharpest frames (default 50)
	Sharpen     bool     // apply an unsharp mask to the stack
	Formats     []string // output formats: png, tif, fits
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{BestPercent: 50, Sharpen: true, Formats: []string{"png", "tif"}}
}

// Result summarizes a planetary run.
type Result struct {
	Source        string   `json:"source"`
	FrameCount    int      `json:"frame_count"`
	StackedFrames int      `json:"stacked_frames"`
	Outputs       []string `json:"outputs"`
	Notes         []string `json:"notes,omitempty"`
}

var videoExts = map[string]bool{".mp4": true, ".mov": true, ".mkv": true, ".m4v": true, ".avi": true}

// imageExts are the still-frame formats a Moon/planetary capture uses when shot as an image series
// instead of a video — FITS plus the processed/raw stills Siril converts into a sequence.
var imageExts = map[string]bool{
	".fits": true, ".fit": true, ".fts": true,
	".tif": true, ".tiff": true, ".png": true, ".jpg": true, ".jpeg": true,
	".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true, ".dng": true,
}

// Process runs the lucky-imaging pipeline on a single video file.
func Process(ctx context.Context, runner *siril.Runner, ffmpegBin, videoPath, workDir, outDir string,
	opts Options, onProgress func(siril.Progress)) (*Result, error) {
	if opts.BestPercent <= 0 || opts.BestPercent > 100 {
		opts.BestPercent = 50
	}
	if len(opts.Formats) == 0 {
		opts.Formats = []string{"png", "tif"}
	}
	base := sanitize(strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath)))
	seqDir, err := filepath.Abs(filepath.Join(workDir, "planetary_"+base))
	if err != nil {
		return nil, err
	}
	if err := fsutil.EnsureDir(seqDir); err != nil {
		return nil, err
	}
	outAbs, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	if err := fsutil.EnsureDir(outAbs); err != nil {
		return nil, err
	}
	res := &Result{Source: videoPath}

	// 1. Get the frames into seqDir: extract a video (ffmpeg), hand a SER straight to Siril, or — when
	//    the input is a folder of pre-captured stills (the Moon/planets shot as a FITS/TIFF image series
	//    rather than a video) — stage those frames directly. All three converge on a FITS sequence next.
	report(onProgress, "extracting frames")
	ext := strings.ToLower(filepath.Ext(videoPath))
	switch info, statErr := os.Stat(videoPath); {
	case statErr == nil && info.IsDir():
		n, err := stageImageFrames(videoPath, seqDir)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("no image frames found in %s (expected FITS/TIFF/PNG/raw stills)", videoPath)
		}
	case videoExts[ext]:
		if err := extractFrames(ctx, ffmpegBin, videoPath, seqDir); err != nil {
			return nil, err
		}
	case ext == ".ser":
		abs, _ := filepath.Abs(videoPath)
		_ = os.Remove(filepath.Join(seqDir, "vid.ser"))
		if err := os.Symlink(abs, filepath.Join(seqDir, "vid.ser")); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported planetary input %q — use a video (mp4/mov/mkv/avi), a SER, or a folder of frames", videoPath)
	}

	// 2. Convert to a FITS sequence.
	report(onProgress, "converting to FITS sequence")
	if _, err := runner.Run(ctx, seqDir, siril.ConvertScript("vid"), onProgress); err != nil {
		return nil, err
	}
	frames, err := filepath.Glob(filepath.Join(seqDir, "vid_*.fits"))
	if err != nil || len(frames) == 0 {
		return nil, fmt.Errorf("no frames after conversion")
	}
	sort.Strings(frames)
	res.FrameCount = len(frames)

	// 3. Rank by sharpness, keep the best N%.
	report(onProgress, "ranking frames by sharpness")
	rejected := selectBest(frames, opts.BestPercent)
	res.StackedFrames = res.FrameCount - len(rejected)

	// 4. Stack the survivors + sharpen + export.
	report(onProgress, "stacking best frames")
	outBase := filepath.Join(outAbs, base+"_stack")
	script := siril.PlanetaryStackScript("vid", res.FrameCount, rejected, outBase, opts.Sharpen, opts.Formats)
	if _, err := runner.Run(ctx, seqDir, script, onProgress); err != nil {
		return nil, err
	}
	for _, f := range opts.Formats {
		res.Outputs = append(res.Outputs, outBase+"."+f)
	}
	res.Notes = append(res.Notes, "frames stacked without surface alignment — for high-resolution planets use the Siril GUI / AutoStakkert!")
	return res, nil
}

// stageImageFrames symlinks every still frame under dir (recursively — capture programs nest frames in
// per-run/timestamp subfolders) into seqDir as a flat, numbered set, so Siril's convert builds one
// sequence from a folder of pre-captured images (no video to extract). Hidden dirs are skipped. Returns
// the number staged; a cross-device target falls back to a copy.
func stageImageFrames(dir, seqDir string) (int, error) {
	var srcs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if imageExts[strings.ToLower(filepath.Ext(p))] {
			srcs = append(srcs, p)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Strings(srcs) // stable acquisition order (timestamped names sort chronologically)
	staged := 0
	for _, src := range srcs {
		abs, err := filepath.Abs(src)
		if err != nil {
			return staged, err
		}
		link := filepath.Join(seqDir, fmt.Sprintf("frame_%05d%s", staged+1, strings.ToLower(filepath.Ext(src))))
		_ = os.Remove(link)
		if err := os.Symlink(abs, link); err != nil {
			if cerr := fsutil.CopyFile(abs, link); cerr != nil { // cross-device fallback
				return staged, fmt.Errorf("stage %s: %w", src, cerr)
			}
		}
		staged++
	}
	return staged, nil
}

// selectBest measures each frame's sharpness and returns the 1-based indices to reject.
func selectBest(frames []string, bestPercent int) []int {
	scores := make([]float64, len(frames))
	for i, f := range frames {
		scores[i] = frameSharpness(f)
	}
	return rejectLeastSharp(scores, bestPercent)
}

// rejectLeastSharp keeps the top bestPercent of frames by score and returns the sorted 1-based
// indices of the rest (always keeping at least one).
func rejectLeastSharp(scores []float64, bestPercent int) []int {
	type scored struct {
		index int
		score float64
	}
	ranked := make([]scored, len(scores))
	for i, s := range scores {
		ranked[i] = scored{index: i + 1, score: s}
	}
	sort.Slice(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })

	keep := len(scores) * bestPercent / 100
	if keep < 1 {
		keep = 1
	}
	var rejected []int
	for i := keep; i < len(ranked); i++ {
		rejected = append(rejected, ranked[i].index)
	}
	sort.Ints(rejected)
	return rejected
}

func frameSharpness(path string) float64 {
	f, err := fits.Open(path)
	if err != nil {
		return 0
	}
	grid, w, h, err := f.ReadDownsampled(512, fits.Mean)
	if err != nil {
		return 0
	}
	return laplacianVariance(grid, w, h)
}

// laplacianVariance is a standard focus/sharpness metric: higher means sharper.
func laplacianVariance(grid []float64, w, h int) float64 {
	if w < 3 || h < 3 {
		return 0
	}
	var sum, sum2 float64
	n := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			c := grid[y*w+x]
			lap := 4*c - grid[y*w+x-1] - grid[y*w+x+1] - grid[(y-1)*w+x] - grid[(y+1)*w+x]
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

func extractFrames(ctx context.Context, ffmpegBin, video, destDir string) error {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpegBin, "-y", "-i", video,
		filepath.Join(destDir, "f_%05d.png"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg extract: %w\n%s", err, lastLines(string(out), 5))
	}
	return nil
}

func report(onProgress func(siril.Progress), step string) {
	if onProgress != nil {
		onProgress(siril.Progress{Line: "", Percent: -1})
		onProgress(siril.Progress{Line: step})
	}
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		case r == ' ' || r == '.':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "video"
	}
	return string(out)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
