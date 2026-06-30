package nightscape

import (
	"context"
	"math"
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
