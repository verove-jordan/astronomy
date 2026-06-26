// Package rawconv prepares one-shot-color stills for Siril ingestion.
//
// Apple's iPhone DNG/HEIC (and other lossy/linear camera raws) are frequently undecodable by the
// libraw build bundled with Siril — `convert` writes its plan file but produces no FITS. We sidestep
// libraw entirely by transcoding those frames to 16-bit RGB TIFF with the macOS `sips` tool (always
// present on the host, alongside the host-installed Siril/GIMP), which Siril imports natively.
// Stills already in a Siril-native format (TIFF/PNG/JPEG) are symlinked through unchanged.
package rawconv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fsutil"
)

// sirilNative lists still formats Siril imports directly, so they need no transcode.
var sirilNative = map[string]bool{".tif": true, ".tiff": true, ".png": true, ".jpg": true, ".jpeg": true}

// Progress is called once per successfully prepared frame (1-based index of total).
type Progress func(index, total int, name string)

// PrepareTIFF lays each still in srcs into dstDir as a numbered frame Siril can convert, preserving
// the order of srcs (acquisition order). Camera raws are transcoded to 16-bit RGB TIFF via `sips`;
// native stills are symlinked. It returns the prepared paths plus a per-frame warning for any frame
// that could not be prepared, and errors only if dstDir is unusable or every frame failed.
func PrepareTIFF(ctx context.Context, srcs []string, dstDir string, onProgress Progress) (out []string, warnings []string, err error) {
	if err := fsutil.EnsureDir(dstDir); err != nil {
		return nil, nil, fmt.Errorf("create seq dir %s: %w", dstDir, err)
	}
	total := len(srcs)
	for i, src := range srcs {
		if cerr := ctx.Err(); cerr != nil {
			return out, warnings, cerr
		}
		name := fmt.Sprintf("frame_%05d", i+1)
		ext := strings.ToLower(filepath.Ext(src))

		var dst string
		var perr error
		if sirilNative[ext] {
			dst = filepath.Join(dstDir, name+ext)
			_ = os.Remove(dst) // idempotent re-runs
			perr = os.Symlink(src, dst)
		} else {
			dst = filepath.Join(dstDir, name+".tif")
			perr = transcodeSips(ctx, src, dst)
		}
		if perr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", filepath.Base(src), perr))
			continue
		}
		out = append(out, dst)
		if onProgress != nil {
			onProgress(i+1, total, filepath.Base(src))
		}
	}
	if len(out) == 0 {
		return nil, warnings, fmt.Errorf("no frames could be prepared from %d source(s)", total)
	}
	return out, warnings, nil
}

// transcodeSips converts one image to a 16-bit RGB TIFF with the macOS `sips` tool.
func transcodeSips(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "sips", "-s", "format", "tiff", src, "--out", dst)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		return fmt.Errorf("sips transcode: %w (%s)", cerr, lastLine(string(out)))
	}
	return nil
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return strings.TrimSpace(s)
}
