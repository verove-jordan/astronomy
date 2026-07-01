package nightscape

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
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

// resolveOrientation picks the final display transform for the composite. An explicit user Orientation
// (cw|ccw|180|…, optionally +"-flip") wins; "auto"/"" first tries the source frame's REAL EXIF orientation
// — robust and, unlike the content heuristic, able to undo front-camera/portrait mirroring — and only
// falls back to the content heuristic (orientAuto, via the "auto" token) when that tag is unreadable.
func resolveOrientation(o Options) string {
	mode := strings.ToLower(strings.TrimSpace(o.Orientation))
	if mode != "" && mode != "auto" {
		return mode
	}
	if src := orientationSource(o); src != "" {
		if tok, ok := exifOrientationToken(src); ok {
			return tok
		}
	}
	return "auto"
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
// most camera raws), returning 1..8 or 0 if absent/unreadable. It reads only the 8-byte header and the
// first IFD (a bounded ReadAt), so it stays cheap even on a multi-MB raw. `sips -g orientation` returns
// <nil> for DNG, so this direct parse is the reliable path for phone ProRAW.
func tiffOrientation(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0
	}
	var bo binary.ByteOrder
	switch {
	case head[0] == 'I' && head[1] == 'I':
		bo = binary.LittleEndian
	case head[0] == 'M' && head[1] == 'M':
		bo = binary.BigEndian
	default:
		return 0 // not a TIFF container
	}
	ifd := int64(bo.Uint32(head[4:8]))
	cnt := make([]byte, 2)
	if _, err := f.ReadAt(cnt, ifd); err != nil {
		return 0
	}
	n := int(bo.Uint16(cnt))
	if n <= 0 || n > 4096 {
		return 0
	}
	entries := make([]byte, n*12)
	if _, err := f.ReadAt(entries, ifd+2); err != nil {
		return 0
	}
	for i := 0; i < n; i++ {
		e := entries[i*12 : i*12+12]
		if bo.Uint16(e[0:2]) == 0x0112 { // Orientation tag; value is a SHORT in the entry's value field
			return int(bo.Uint16(e[8:10]))
		}
	}
	return 0
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
