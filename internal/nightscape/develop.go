package nightscape

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/rawconv"
)

// ReadFocal35mm returns the EXIF 35 mm-equivalent focal length (mm) of the first frame that carries
// it, via macOS Spotlight metadata (`mdls`). It is used to derive a plate scale for solving a phone
// field (see deriveSolve). Returns 0 when unreadable (no mdls, non-macOS, or the tag is absent), in
// which case solving falls back to the header/blind scale — both soft-fail paths.
func ReadFocal35mm(frames []string) float64 {
	for _, f := range frames {
		if v := mdlsFloat(f, "kMDItemFocalLength35mm"); v > 0 {
			return v
		}
	}
	return 0
}

// mdlsFloat reads one numeric Spotlight attribute from a file, returning 0 on any failure.
func mdlsFloat(path, attr string) float64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "mdls", "-name", attr, "-raw", path).Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return v
}

// linearizeSRGB converts a display-referred (sRGB/Display-P3 transfer) image to linear light, in
// place. The frames reach us through `sips` (which writes a gamma-encoded TIFF) then Siril, so the
// whole linear-domain recipe needs them linearised first — mirroring the reference pipeline, which
// linearised its JPG inputs the same way (main.py _srgb_to_linear_u16).
func linearizeSRGB(im *fits.Image) {
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		for i := range p {
			x := float64(p[i])
			if x <= 0.04045 {
				p[i] = float32(x / 12.92)
			} else {
				p[i] = float32(math.Pow((x+0.055)/1.055, 2.4))
			}
		}
	}
}

// encodeSRGB applies the sRGB transfer (linear → display) to a single value in [0,1].
func encodeSRGB(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1.0/2.4) - 0.055
}

// orient applies a final display orientation to an upright result. mode is "auto" (ensure portrait
// with the bright sky on top — robust for nightscapes), "none", or an explicit transform built from
// a rotation token (cw|ccw|180) optionally suffixed with "-flip" (horizontal mirror), e.g. "cw-flip".
func orient(im *fits.Image, mode string) *fits.Image {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "auto" {
		return orientAuto(im)
	}
	flip := strings.Contains(mode, "flip")
	rot := strings.TrimSuffix(strings.TrimSuffix(mode, "flip"), "-")
	switch rot {
	case "cw", "rotate90", "90":
		im = rotate90(im, true)
	case "ccw", "rotate270", "270":
		im = rotate90(im, false)
	case "180", "rotate180":
		im = rotate180(im)
	}
	if flip {
		im = flipH(im)
	}
	return im
}

// orientAuto rotates a landscape frame to portrait, then flips it 180° if the bright half (the sky)
// is at the bottom — content-based and so insensitive to the camera's dropped EXIF orientation.
func orientAuto(im *fits.Image) *fits.Image {
	if im.W > im.H {
		im = rotate90(im, true)
	}
	lum := luminance(im)
	var top, bot float64
	half := im.H / 2
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			if y < half {
				top += float64(lum[y*im.W+x])
			} else {
				bot += float64(lum[y*im.W+x])
			}
		}
	}
	if top < bot {
		im = rotate180(im)
	}
	return im
}

func rotate90(in *fits.Image, clockwise bool) *fits.Image {
	out := fits.NewImage(in.H, in.W, in.C)
	for c := 0; c < in.C; c++ {
		src, dst := in.Pix[c], out.Pix[c]
		for y := 0; y < in.H; y++ {
			for x := 0; x < in.W; x++ {
				var nx, ny int
				if clockwise {
					nx, ny = in.H-1-y, x
				} else {
					nx, ny = y, in.W-1-x
				}
				dst[ny*out.W+nx] = src[y*in.W+x]
			}
		}
	}
	return out
}

func rotate180(in *fits.Image) *fits.Image {
	out := fits.NewImage(in.W, in.H, in.C)
	n := in.W * in.H
	for c := 0; c < in.C; c++ {
		src, dst := in.Pix[c], out.Pix[c]
		for i := 0; i < n; i++ {
			dst[n-1-i] = src[i]
		}
	}
	return out
}

func flipH(in *fits.Image) *fits.Image {
	out := fits.NewImage(in.W, in.H, in.C)
	for c := 0; c < in.C; c++ {
		src, dst := in.Pix[c], out.Pix[c]
		for y := 0; y < in.H; y++ {
			base := y * in.W
			for x := 0; x < in.W; x++ {
				dst[base+in.W-1-x] = src[base+x]
			}
		}
	}
	return out
}

// resolveOrientation picks the final display transform for the composite — applied EXACTLY ONCE, at
// the end of the grade. An explicit user Orientation (cw|ccw|180|…, optionally +"-flip") wins;
// "auto" forces the legacy content heuristic; the default ("", "exif") reconciles the source
// frame's REAL EXIF orientation with how the raw developer treated it (orientDecision) — the
// double-apply between a developer that bakes the rotation and this final pass is what made the
// same capture come out landscape on one run and portrait on the next. devW/devH are the DEVELOPED
// reference frame's pixel dimensions (for the baked-rotation detection).
func resolveOrientation(o Options, devW, devH int) string {
	mode := strings.ToLower(strings.TrimSpace(o.Orientation))
	if mode != "" && mode != "auto" && mode != "exif" {
		return mode
	}
	if mode == "auto" {
		return "auto" // explicit opt-in to the content heuristic (bright-half = sky)
	}
	src := orientationSource(o)
	if src == "" {
		return "none"
	}
	code, srcW, srcH := tiffOrientationDims(src)
	if code == 0 {
		code = sipsOrientation(src)
	}
	return orientDecision(developerFor(src), isDNG(src), code, srcW, srcH, devW, devH)
}

// developerFor names the raw developer that produced the working frames for src: "" for stills the
// pipeline symlinks through untouched (their pixels are exactly as stored), else the platform
// developer (dcraw_emu preferred, sips fallback — see rawconv).
func developerFor(src string) string {
	switch strings.ToLower(filepath.Ext(src)) {
	case ".tif", ".tiff", ".png", ".jpg", ".jpeg":
		return "" // Siril-native: never developed, pixels as stored
	}
	kind, err := rawconv.Developer()
	if err != nil {
		return ""
	}
	return kind
}

func isDNG(src string) bool { return strings.EqualFold(filepath.Ext(src), ".dng") }

// orientDecision reconciles the source EXIF orientation with the developer's baking behaviour and
// returns the single token to apply at the end of the composite. Pure — table-tested.
//
//   - dcraw_emu runs with -t 0 and NEVER bakes the rotation → apply the EXIF token here.
//   - sips leaves DNG pixels in sensor orientation and drops the tag (verified on real iPhone
//     ProRAW) → apply the token. For other formats ImageIO may have baked the rotation: the
//     rotated codes (5..8) transpose the image aspect, so a developed aspect that no longer
//     matches the source means "already baked" → nothing left to apply. The non-transposing
//     codes (2/3/4) are undetectable from dims — assume baked (ImageIO applies the full
//     orientation for non-raw formats).
//   - no developer (native stills) → pixels are exactly as stored → apply the token.
//   - unreadable tag → "none": a guessed rotation risks an upside-down result, and the explicit
//     user override exists for exactly that case.
func orientDecision(devKind string, srcIsDNG bool, srcCode, srcW, srcH, devW, devH int) string {
	tok, ok := exifTokenFromCode(srcCode)
	if !ok {
		return "none"
	}
	if devKind != "sips" || srcIsDNG {
		return tok // dcraw (-t 0), native stills, and sips-on-DNG all leave the pixels unrotated
	}
	if srcCode >= 5 && srcCode <= 8 { // rotated codes transpose the aspect
		srcLandscape := true // phone/camera sensors are landscape-native; override when dims are known
		if srcW > 0 && srcH > 0 && srcW != srcH {
			srcLandscape = srcW > srcH
		}
		if devW <= 0 || devH <= 0 || devW == devH {
			return tok // aspect unknown — assume unbaked rather than silently dropping the rotation
		}
		if (devW > devH) != srcLandscape {
			return "none" // developed aspect already transposed → sips baked the rotation
		}
		return tok
	}
	return "none" // 2/3/4: undetectable; sips applies full orientation for non-raw formats
}

// orientationSource is the frame whose EXIF orientation stands for the session (all frames share the
// camera pose): the explicit foreground override if set, else the first captured frame.
func orientationSource(o Options) string {
	if o.ForegroundFrame != "" {
		return o.ForegroundFrame
	}
	if len(o.Frames) > 0 {
		return o.Frames[0]
	}
	return ""
}

// exifOrientationToken reads path's EXIF orientation and maps it to an orient() token. Phone captures are
// stored in the sensor's native landscape with an orientation tag that the `sips` TIFF transcode drops
// (so the recipe would otherwise have to guess); reading it here restores the intended orientation
// exactly. It parses the TIFF Orientation tag directly — the reliable path for DNG/TIFF raws, where
// `sips -g orientation` reports <nil> — and falls back to sips for non-TIFF containers. ok=false when
// unreadable (no tag, non-macOS + non-TIFF, etc.).
func exifOrientationToken(path string) (string, bool) {
	code := tiffOrientation(path)
	if code == 0 {
		code = sipsOrientation(path)
	}
	return exifTokenFromCode(code)
}

// tiffOrientation reads the EXIF Orientation tag (0x0112) from IFD0 of a TIFF-based file (DNG, TIFF, and
// most camera raws), returning 1..8 or 0 if absent/unreadable. `sips -g orientation` returns <nil> for
// DNG, so this direct parse is the reliable path for phone ProRAW.
func tiffOrientation(path string) int {
	code, _, _ := tiffOrientationDims(path)
	return code
}

// tiffOrientationDims reads the EXIF Orientation tag (0x0112) plus the IFD0 image dimensions
// (0x0100 ImageWidth / 0x0101 ImageLength) from a TIFF-based file. It reads only the 8-byte header
// and the first IFD (a bounded ReadAt), so it stays cheap even on a multi-MB raw. Any unreadable
// value returns as 0. NOTE: on some DNGs IFD0 describes a preview, not the main image — callers use
// the dimensions only for ASPECT comparison, which a preview preserves.
func tiffOrientationDims(path string) (code, w, h int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()
	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0, 0, 0
	}
	var bo binary.ByteOrder
	switch {
	case head[0] == 'I' && head[1] == 'I':
		bo = binary.LittleEndian
	case head[0] == 'M' && head[1] == 'M':
		bo = binary.BigEndian
	default:
		return 0, 0, 0 // not a TIFF container
	}
	ifd := int64(bo.Uint32(head[4:8]))
	cnt := make([]byte, 2)
	if _, err := f.ReadAt(cnt, ifd); err != nil {
		return 0, 0, 0
	}
	n := int(bo.Uint16(cnt))
	if n <= 0 || n > 4096 {
		return 0, 0, 0
	}
	entries := make([]byte, n*12)
	if _, err := f.ReadAt(entries, ifd+2); err != nil {
		return 0, 0, 0
	}
	// IFD entry values here are SHORT (type 3) or LONG (type 4), inlined in the 4-byte value field.
	entryValue := func(e []byte) int {
		switch bo.Uint16(e[2:4]) {
		case 3:
			return int(bo.Uint16(e[8:10]))
		case 4:
			return int(bo.Uint32(e[8:12]))
		}
		return 0
	}
	for i := 0; i < n; i++ {
		e := entries[i*12 : i*12+12]
		switch bo.Uint16(e[0:2]) {
		case 0x0100:
			w = entryValue(e)
		case 0x0101:
			h = entryValue(e)
		case 0x0112:
			code = entryValue(e)
		}
	}
	return code, w, h
}

// sipsOrientation returns path's EXIF orientation code (1..8), or 0 on any failure.
func sipsOrientation(path string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sips", "-g", "orientation", path).Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "orientation:"); ok {
			if code, cerr := strconv.Atoi(strings.TrimSpace(v)); cerr == nil {
				return code
			}
		}
	}
	return 0
}

// exifTokenFromCode maps an EXIF orientation code (1..8) to the orient() token (a rotation, then an
// optional horizontal mirror) that displays the stored pixels upright. Codes 5 and 7 are the mirrored
// diagonals (transpose/transverse); 2 and 4 are pure mirrors. Anything outside 1..8 → not ok.
func exifTokenFromCode(code int) (string, bool) {
	switch code {
	case 1:
		return "none", true
	case 2:
		return "flip", true
	case 3:
		return "180", true
	case 4:
		return "180-flip", true
	case 5:
		return "cw-flip", true
	case 6:
		return "cw", true
	case 7:
		return "ccw-flip", true
	case 8:
		return "ccw", true
	}
	return "", false
}
