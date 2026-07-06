// Package rawconv prepares one-shot-color stills for Siril ingestion.
//
// Apple's iPhone DNG/HEIC (and other lossy/linear camera raws) are frequently undecodable by the
// libraw build bundled with Siril — `convert` writes its plan file but produces no FITS. We sidestep
// libraw-in-Siril by developing those frames to a 16-bit RGB TIFF Siril imports natively. LibRaw's
// `dcraw_emu` is PREFERRED on every platform (`brew install libraw` on macOS, `libraw-bin` in the
// container): it develops with no per-frame auto-brightening, no baked orientation and an exact sRGB
// transfer curve — the photometric consistency the nightscape stack requires. macOS `sips` remains a
// soft-fail fallback (Apple's ProRAW rendering applies an opaque tone curve). Stills already in a
// Siril-native format (TIFF/PNG/JPEG) are symlinked through unchanged.
package rawconv

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/tiff"

	"github.com/verove-jordan/astronomy/internal/fsutil"
)

// sirilNative lists still formats Siril imports directly, so they need no transcode.
var sirilNative = map[string]bool{".tif": true, ".tiff": true, ".png": true, ".jpg": true, ".jpeg": true}

// Progress is called once per successfully prepared frame (1-based index of total).
type Progress func(index, total int, name string)

// PrepareTIFF lays each still in srcs into dstDir as a numbered frame Siril can convert, preserving
// the order of srcs (acquisition order). Camera raws are developed to a 16-bit RGB TIFF (sips or
// dcraw_emu); native stills are symlinked. It returns the prepared paths plus a per-frame warning for any
// frame that could not be prepared, and errors only if dstDir is unusable or every frame failed.
func PrepareTIFF(ctx context.Context, srcs []string, dstDir string, onProgress Progress) (out []string, warnings []string, err error) {
	if err := fsutil.EnsureDir(dstDir); err != nil {
		return nil, nil, fmt.Errorf("create seq dir %s: %w", dstDir, err)
	}
	transcode, terr := rawTranscoder() // resolved once; a raw frame surfaces terr as its own warning
	total := len(srcs)
	for i, src := range srcs {
		if cerr := ctx.Err(); cerr != nil {
			return out, warnings, cerr
		}
		name := fmt.Sprintf("frame_%05d", i+1)
		ext := strings.ToLower(filepath.Ext(src))

		var dst string
		var perr error
		switch {
		case sirilNative[ext]:
			dst = filepath.Join(dstDir, name+ext)
			_ = os.Remove(dst) // idempotent re-runs
			perr = os.Symlink(src, dst)
		case terr != nil:
			perr = terr // no raw developer on this platform
		default:
			dst = filepath.Join(dstDir, name+".tif")
			perr = transcode(ctx, src, dst)
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

// Developer reports which raw→TIFF developer PrepareTIFF will use: "dcraw_emu" (preferred — exact,
// photometrically consistent output, identical on host and in the container) or "sips" (macOS
// fallback; Apple's ProRAW rendering applies a tone curve we cannot exactly invert). Exposed for
// environment health reporting and for the nightscape orientation decision, which must know how the
// developer treats the EXIF orientation tag.
func Developer() (string, error) {
	if _, err := exec.LookPath(dcrawBin()); err == nil {
		return "dcraw_emu", nil
	}
	if _, err := exec.LookPath(sipsBin()); err == nil {
		return "sips", nil
	}
	return "", fmt.Errorf("no raw developer found: install LibRaw (`brew install libraw` on macOS, `libraw-bin` on Linux) or set DCRAW_BIN; macOS `sips` works as a fallback")
}

// rawTranscoder returns the platform's raw→TIFF developer, preferring LibRaw's `dcraw_emu` over
// macOS `sips`: dcraw develops with NO per-frame auto-brightening and an exactly-known transfer
// curve (see transcodeDcraw), which the nightscape stack depends on for photometric consistency —
// sips renders ProRAW through Apple's opaque tone curve and is kept only as a fallback. It errors
// when neither is available so a raw frame reports a clear cause instead of a silent "no frames".
func rawTranscoder() (func(context.Context, string, string) error, error) {
	kind, err := Developer()
	if err != nil {
		return nil, err
	}
	if kind == "dcraw_emu" {
		return transcodeDcraw, nil
	}
	return transcodeSips, nil
}

func sipsBin() string {
	if b := os.Getenv("SIPS_BIN"); b != "" {
		return b
	}
	return "sips"
}

func dcrawBin() string {
	if b := os.Getenv("DCRAW_BIN"); b != "" {
		return b
	}
	return "dcraw_emu"
}

// transcodeSips develops one raw to a 16-bit RGB TIFF with the macOS `sips` tool.
func transcodeSips(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, sipsBin(), "-s", "format", "tiff", src, "--out", dst)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		return fmt.Errorf("sips transcode: %w (%s)", cerr, lastLine(string(out)))
	}
	return nil
}

// transcodeDcraw develops one raw to a 16-bit RGB TIFF with LibRaw's `dcraw_emu`, tuned for the
// stacking pipeline rather than a pretty single frame:
//
//	-6            16-bit output
//	-W            NO per-frame auto-brightening — every frame of a stack must share one exposure
//	              scale or per-frame gains break calibration and the sigma-clipped stack
//	-w            camera white balance (shared by lights and calibration frames, so it cancels)
//	-t 0          NEVER bake the EXIF orientation into the pixels — orientation is applied exactly
//	              once, at the end of the nightscape composite, from the raw's own EXIF tag
//	-g 2.4 12.92  exact sRGB transfer curve, so the pipeline's linearizeSRGB is its exact inverse
//	              (dcraw's default is a BT.709-ish curve that linearizeSRGB would mis-invert)
func transcodeDcraw(ctx context.Context, src, dst string) error {
	return dcrawDevelop(ctx, src, dst, "-6", "-W", "-w", "-t", "0", "-g", "2.4", "12.92")
}

// dcrawDevelop runs dcraw_emu with the given options. dcraw_emu writes `<input>.tiff` beside its input and
// has no output-path flag — and the capture volume is read-only — so we point it at a symlink in the
// (writable) destination dir and rename the result to dst. `-T` forces TIFF output.
func dcrawDevelop(ctx context.Context, src, dst string, opts ...string) error {
	// The symlink must point at an ABSOLUTE source: a relative src (e.g. the CLI's "input/…") would be
	// resolved relative to the symlink's own directory (dstDir), not the caller's CWD, so dcraw_emu
	// opens the wrong path and fails with "Input/output error". (sips took the path directly, so this
	// only bites the dcraw path.)
	if abs, err := filepath.Abs(src); err == nil {
		src = abs
	}
	link := dst + strings.ToLower(filepath.Ext(src)) // e.g. frame_00001.tif.dng, in the writable dstDir
	_ = os.Remove(link)
	if err := os.Symlink(src, link); err != nil {
		if cerr := fsutil.CopyFile(src, link); cerr != nil { // symlinks disabled / cross-device → copy
			return fmt.Errorf("stage raw %s: %w", filepath.Base(src), cerr)
		}
	}
	defer os.Remove(link)
	produced := link + ".tiff"
	defer os.Remove(produced)

	args := append([]string{"-T"}, opts...)
	args = append(args, link)
	cmd := exec.CommandContext(ctx, dcrawBin(), args...)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		return fmt.Errorf("dcraw_emu transcode: %w (%s)", cerr, lastLine(string(out)))
	}
	if err := os.Rename(produced, dst); err != nil {
		return fmt.Errorf("rename transcoded tiff %s: %w", filepath.Base(dst), err)
	}
	return nil
}

// Thumbnail develops one camera raw (or any raw-developer-readable still) to a DOWNSCALED 8-bit PNG at dst,
// bounding the longer edge to maxEdge (maxEdge <= 0 = full size). It is the fast path for previews and for
// pixel-stat classification. sips downsamples during the decode (`-Z`); dcraw_emu develops half-size
// (`-h`) then Go downscales. PNG (not TIFF) because sips emits a JPEG-COMPRESSED TIFF when downscaling
// which the Go image stack can't decode — PNG stays decodable by image.Decode.
func Thumbnail(ctx context.Context, src, dst string, maxEdge int) error {
	if _, err := exec.LookPath(sipsBin()); err == nil {
		return thumbnailSips(ctx, src, dst, maxEdge)
	}
	if _, err := exec.LookPath(dcrawBin()); err == nil {
		return thumbnailDcraw(ctx, src, dst, maxEdge)
	}
	return fmt.Errorf("no raw developer found: install `sips` (macOS) or `dcraw_emu` from libraw-bin (Linux), or set DCRAW_BIN")
}

func thumbnailSips(ctx context.Context, src, dst string, maxEdge int) error {
	args := []string{"-s", "format", "png"}
	if maxEdge > 0 {
		args = append([]string{"-Z", strconv.Itoa(maxEdge)}, args...)
	}
	args = append(args, src, "--out", dst)
	cmd := exec.CommandContext(ctx, sipsBin(), args...)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		return fmt.Errorf("sips thumbnail: %w (%s)", cerr, lastLine(string(out)))
	}
	return nil
}

// thumbnailDcraw develops the raw to a temporary TIFF (half-size when downscaling, for speed) then
// re-encodes it as a PNG at dst so the caller's image.Decode (PNG/JPEG only) can read it.
func thumbnailDcraw(ctx context.Context, src, dst string, maxEdge int) error {
	tmpTif := dst + ".dcraw.tif"
	opts := []string{"-6", "-w"}
	if maxEdge > 0 {
		opts = append(opts, "-h") // half-size decode: fast; Go bounds it to maxEdge below
	}
	if err := dcrawDevelop(ctx, src, tmpTif, opts...); err != nil {
		return err
	}
	defer os.Remove(tmpTif)
	return tiffToPNG(tmpTif, dst, maxEdge)
}

// tiffToPNG decodes a TIFF and writes it as a PNG at pngPath, bounding the longer edge to maxEdge (<=0 =
// no downscale) with a Catmull-Rom resize.
func tiffToPNG(tifPath, pngPath string, maxEdge int) error {
	f, err := os.Open(tifPath)
	if err != nil {
		return err
	}
	defer f.Close()
	img, err := tiff.Decode(f)
	if err != nil {
		return fmt.Errorf("decode dcraw tiff: %w", err)
	}
	img = boundLongEdge(img, maxEdge)
	out, err := os.Create(pngPath)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		return fmt.Errorf("encode png %s: %w", filepath.Base(pngPath), err)
	}
	return nil
}

// boundLongEdge returns img scaled so its longer edge is at most maxEdge (maxEdge <= 0 or already-smaller
// returns img unchanged), using a Catmull-Rom filter.
func boundLongEdge(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxEdge <= 0 || (w <= maxEdge && h <= maxEdge) {
		return img
	}
	long := w
	if h > long {
		long = h
	}
	nw, nh := w*maxEdge/long, h*maxEdge/long
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return strings.TrimSpace(s)
}
