package solar

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// native.go measures the colour the recording actually had, so the finish can render in it instead
// of in a chosen palette.
//
// The rest of this package is deliberately monochrome: the etalon passes 0.6 Å of Hα and nothing
// else, so there is no spectral colour to recover, and the ingest takes the luma plane because on a
// 4:2:2 clip the chroma planes are half-resolution and would dilute the only signal-bearing channel.
// All of that is right for DETAIL and says nothing about hue. What reaches the sensor still lands on
// a colour filter array and comes back through the phone's own rendering as a particular orange, and
// that orange is what the capture looked like.
//
// So colour is measured separately from detail and at a different resolution: subsampled chroma is
// perfectly adequate for "what hue was this", and irrelevant to sharpness because the ramp only ever
// recolours a mono plane the stack produced.
//
// THE MEASUREMENT IS INDEXED BY QUANTILE, not by brightness, and that is what makes it survive the
// finish. Between the recording and the finished image sit a deconvolution, a starlet pass, a
// limb-darkening correction and a strongly non-linear tone curve; a ramp anchored on the source's
// own levels would land in the wrong place after all of that. Rank is the one thing the whole chain
// preserves — it is monotone from end to end — so the darkest tenth of the recording is still the
// darkest tenth of the render, and the colour measured there belongs there.

const (
	// nativeQuantiles is how many points the colour is measured at. Enough to follow a hue that
	// shifts from the sky through the disc to the highlights, few enough that each still holds tens of
	// thousands of pixels to take a median over.
	nativeQuantiles = 9
	// nativeFrames is how many frames across the clip the measurement averages. Colour is a property
	// of the capture rather than of any one frame, and a handful spread across the clip costs almost
	// nothing while covering any auto-white-balance drift.
	nativeFrames = 8
	// nativeMaxEdge is the long edge frames are measured at. Colour has no fine structure worth
	// resolving and the chroma planes are subsampled anyway.
	nativeMaxEdge = 720
	// nativeRegionR is how far past the limb the measurement reaches, as a fraction of the radius.
	// Far enough to include the sky the darkest stops need, close enough that the disc still makes up
	// about half the samples.
	nativeRegionR = 1.4
)

// NativeChroma is the recording's own colour, as the median chromaticity at a set of luminance
// quantiles of the source.
//
// The values are normalised to unit luminance, so this carries hue and saturation and nothing about
// brightness — brightness stays entirely the finish's business, which is what lets the colour be
// taken from the recording while the exposure and the stretch stay as they were tuned.
type NativeChroma struct {
	Q []float64 `json:"q"` // quantiles, ascending
	R []float64 `json:"r"`
	G []float64 `json:"g"`
	B []float64 `json:"b"`
}

// OK reports whether a measurement is usable.
func (n NativeChroma) OK() bool {
	return len(n.Q) > 1 && len(n.R) == len(n.Q) && len(n.G) == len(n.Q) && len(n.B) == len(n.Q)
}

// MeasureNativeChroma samples a clip and reports the colour it was recorded in.
func MeasureNativeChroma(ctx context.Context, ffmpegBin, path string, info VideoInfo) (NativeChroma, error) {
	if info.DurationSec <= 0 {
		return NativeChroma{}, fmt.Errorf("native colour: no duration for %s", filepath.Base(path))
	}
	scratch, err := os.MkdirTemp("", "solar-native-")
	if err != nil {
		return NativeChroma{}, err
	}
	defer os.RemoveAll(scratch)

	type sample struct{ lum, r, g, b float64 }
	var all []sample
	for i := 0; i < nativeFrames; i++ {
		t := info.DurationSec * (float64(i) + 0.5) / float64(nativeFrames)
		im, err := extractColourFrame(ctx, ffmpegBin, path, t, scratch, i)
		if err != nil {
			continue // one unreadable sample must not cost the measurement
		}
		// DELIBERATELY NOT LINEARISED, which is the opposite of what every other measurement here does.
		//
		// The stack works in linear light because averaging, deconvolving and flat-fielding are only
		// meaningful there. Colour is not being averaged — it is being carried from one rendering to
		// another. "The colour of the recording" means what the clip looks like when it is PLAYED,
		// which is the display-referred RGB as decoded, and the ramp is applied to the finish's output,
		// which is display-referred too. Linearising would put a transfer curve between the two for
		// nothing.
		//
		// It is also the only safe choice here: measured on the real clips, the gray16be and rgb48be
		// paths do not agree on scale, because the YUV-to-RGB conversion handles range differently
		// from the luma extraction. A linearisation calibrated for one is not valid for the other.
		// Chromaticity normalised to unit luminance is invariant to that scale, and it is what this
		// stores.
		disc, ok := FitLimb(firstPlaneImage(lumaPlane(im), im.W, im.H))
		if !ok {
			continue
		}
		// Sampled around the DISC, not over the frame. The Sun covers a few percent of a phone's frame,
		// so quantiles taken over everything would be sky nine times out of ten and the disc's own
		// colour would land entirely in the top one.
		r2 := (nativeRegionR * disc.R) * (nativeRegionR * disc.R)
		for y := 0; y < im.H; y++ {
			dy := float64(y) - disc.CY
			for x := 0; x < im.W; x++ {
				dx := float64(x) - disc.CX
				if dx*dx+dy*dy > r2 {
					continue
				}
				j := y*im.W + x
				r, g, b := float64(im.Pix[0][j]), float64(im.Pix[1][j]), float64(im.Pix[2][j])
				if l := lumaOf(r, g, b); l > 0 {
					all = append(all, sample{l, r, g, b})
				}
			}
		}
	}
	if len(all) < nativeQuantiles*64 {
		return NativeChroma{}, fmt.Errorf("native colour: too few usable pixels in %s", filepath.Base(path))
	}
	sort.Slice(all, func(i, j int) bool { return all[i].lum < all[j].lum })

	out := NativeChroma{}
	for i := 0; i < nativeQuantiles; i++ {
		q := (float64(i) + 0.5) / nativeQuantiles
		lo := len(all) * i / nativeQuantiles
		hi := len(all) * (i + 1) / nativeQuantiles
		if hi <= lo {
			continue
		}
		bin := all[lo:hi]
		// A median per channel, not a mean: the bins near the limb straddle an edge, and a mean there
		// would report a colour that no pixel in the frame actually has.
		r := medianOfField(bin, func(s sample) float64 { return s.r })
		g := medianOfField(bin, func(s sample) float64 { return s.g })
		b := medianOfField(bin, func(s sample) float64 { return s.b })
		lum := lumaOf(r, g, b)
		if lum <= 1e-9 {
			continue
		}
		out.Q = append(out.Q, q)
		out.R = append(out.R, r/lum)
		out.G = append(out.G, g/lum)
		out.B = append(out.B, b/lum)
	}
	if !out.OK() {
		return NativeChroma{}, fmt.Errorf("native colour: no usable quantiles in %s", filepath.Base(path))
	}
	return out, nil
}

// medianOfField is the median of one field over a slice of samples.
func medianOfField[T any](vals []T, get func(T) float64) float64 {
	v := make([]float64, len(vals))
	for i, s := range vals {
		v[i] = get(s)
	}
	sort.Float64s(v)
	return v[len(v)/2]
}

// lumaOf is the luminance the ramp's stops are scaled against. It matches the weights applyPalette
// already uses for saturation, so a colour that round-trips through both keeps its brightness.
func lumaOf(r, g, b float64) float64 { return 0.299*r + 0.587*g + 0.114*b }

// nativeRamp turns a measurement into a palette ramp against a particular rendered plane.
//
// Each measured quantile is placed at the value the RENDERED image reaches at that same quantile, so
// the colour lands on the tones it was measured on however the tone curve moved them. The stop's
// brightness is then the render's, and only its hue is the recording's.
func nativeRamp(n NativeChroma, rendered []float32, mask []float32) []colourStop {
	if !n.OK() {
		return nil
	}
	vals := make([]float64, 0, len(rendered))
	for i, v := range rendered {
		if mask != nil && mask[i] < 0.5 {
			continue
		}
		vals = append(vals, float64(v))
	}
	if len(vals) < len(n.Q) {
		return nil
	}
	sort.Float64s(vals)

	out := make([]colourStop, 0, len(n.Q)+2)
	last := -1.0
	for i, q := range n.Q {
		t := vals[clampInt(int(q*float64(len(vals))), 0, len(vals)-1)]
		// The ramp must be strictly increasing in t or sampleRamp's search misbehaves; a render that
		// flattens a whole quantile onto one value is not a reason to drop the colour there.
		if t <= last {
			t = last + 1e-4
		}
		last = t
		r, g, b := fitToLuma(n.R[i], n.G[i], n.B[i], t)
		out = append(out, colourStop{t, r, g, b})
	}
	// Anchor both ends so nothing is extrapolated: sampleRamp holds the end stops flat past them, and
	// a ramp that starts at t=0.3 would render everything below that in the first stop's colour.
	if out[0].t > 0 {
		r, g, b := fitToLuma(n.R[0], n.G[0], n.B[0], 0)
		out = append([]colourStop{{0, r, g, b}}, out...)
	}
	if e := out[len(out)-1]; e.t < 1 {
		k := len(n.Q) - 1
		r, g, b := fitToLuma(n.R[k], n.G[k], n.B[k], 1)
		out = append(out, colourStop{1, r, g, b})
	}
	return out
}

// fitToLuma scales a unit-luminance chromaticity to the given luminance, desaturating only as far as
// it must to keep every channel in range.
//
// A saturated hue cannot be made bright without clipping — deep red at unit luminance would need a
// red channel of 3.3 — and clipping each channel independently would swing the hue as it brightened.
// Pulling towards neutral instead is what a highlight roll-off does, and it is what the recording
// itself did: a phone's own rendering desaturates as it approaches white, which is why the measured
// chromaticity at the top quantiles is already closer to neutral than at the middle ones.
func fitToLuma(r, g, b, lum float64) (float64, float64, float64) {
	lum = clampF(lum, 0, 1)
	r, g, b = r*lum, g*lum, b*lum
	if m := math.Max(r, math.Max(g, b)); m > 1 {
		// Blend towards the achromatic colour of the same luminance until the peak just fits.
		t := (m - 1) / (m - lum)
		if lum >= m {
			t = 0
		}
		t = clampF(t, 0, 1)
		r = r + (lum-r)*t
		g = g + (lum-g)*t
		b = b + (lum-b)*t
	}
	return clampF(r, 0, 1), clampF(g, 0, 1), clampF(b, 0, 1)
}

// extractColourFrame pulls one frame as a 16-bit RGB PNG.
//
// It is the colour twin of extractProbeFrame and differs from it in exactly one way that matters: it
// asks for rgb48be rather than gray16be, so swscale performs the YUV-to-RGB conversion including the
// chroma upsampling. That is the right trade here and the wrong one there — chroma is half-resolution
// on a 4:2:2 clip, which costs nothing when the question is "what hue" and would cost real detail if
// this fed the stack.
func extractColourFrame(ctx context.Context, ffmpegBin, path string, t float64, scratch string, idx int) (*fits.Image, error) {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	dst := filepath.Join(scratch, fmt.Sprintf("%s_c%02d.png", sanitizeName(path), idx))
	defer os.Remove(dst)
	args := []string{
		"-y", "-ss", strconv.FormatFloat(t, 'f', 3, 64), "-i", path, "-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2", nativeMaxEdge),
		"-pix_fmt", "rgb48be", dst,
	}
	if out, err := exec.CommandContext(ctx, ffmpegBin, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg colour frame: %w\n%s", err, tailLines(string(out), 4))
	}
	return decodeRGB(dst)
}

// decodeRGB reads an image into three linear-indexed planes.
func decodeRGB(path string) (*fits.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	b := img.Bounds()
	out := fits.NewImage(b.Dx(), b.Dy(), 3)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := y*b.Dx() + x
			out.Pix[0][i] = float32(r) / 65535
			out.Pix[1][i] = float32(g) / 65535
			out.Pix[2][i] = float32(bl) / 65535
		}
	}
	return out, nil
}

// lumaPlane is the luminance of a three-plane image, for fitting the limb on a colour frame.
func lumaPlane(im *fits.Image) []float32 {
	out := make([]float32, len(im.Pix[0]))
	for i := range out {
		out[i] = float32(lumaOf(float64(im.Pix[0][i]), float64(im.Pix[1][i]), float64(im.Pix[2][i])))
	}
	return out
}
