package solar

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/tiff"
	"golang.org/x/sync/errgroup"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/imgops"
	"github.com/verove-jordan/astronomy/internal/rawconv"
)

// still.go ingests the frames that are not video: iPhone ProRAW DNG and HEIC, and any TIFF/PNG/JPEG
// or FITS a camera produced. They go through the same contract as clips — linear light, cropped to
// the disc, one FITS per frame with its limb geometry attached.

// Band is the solar band a capture was shot in, which decides which colour channel carries signal.
type Band string

const (
	BandAuto       Band = "auto"
	BandHAlpha     Band = "h_alpha"
	BandWhiteLight Band = "white_light"
)

// haRedRatio is how much brighter red must be than green for a frame to be judged Hα. Through a
// 0.6 Å etalon at 656.3 nm essentially all the light lands in red, so the real ratio is far above
// this; the threshold only has to separate that from a white-light or unfiltered capture.
const haRedRatio = 2.0

// solarDevelop is how a solar raw is developed: no white balance, linear output.
//
// Both matter and both were found the hard way on real frames. With camera white balance, an Hα
// capture that metered correctly comes out with its red channel pinned at full scale across the
// entire disc — every percentile exactly 1.0 — because the daylight red multiplier has nowhere to
// put the light. The frame looks fine as a colour thumbnail, since the untouched green and blue
// pull the average down, and is completely unusable as data. Linear output then removes the need to
// invert a transfer curve at all.
var solarDevelop = rawconv.DevelopOptions{Narrowband: true, Linear: true}

// IngestStills develops, linearises and crops a set of stills into stackable frames.
func IngestStills(ctx context.Context, paths []string, opts IngestOptions) ([]Frame, []string, error) {
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no stills to ingest")
	}
	if err := fsutil.EnsureDir(opts.WorkDir); err != nil {
		return nil, nil, err
	}
	devDir, err := os.MkdirTemp("", "solar-develop-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(devDir)

	sources, developed, warnings, err := developStills(ctx, paths, devDir)
	if err != nil {
		return nil, warnings, err
	}
	// Only dcraw honours the linear request; the sips fallback still needs its curve inverted.
	kind, _ := rawconv.Developer()
	linear := kind == "dcraw_emu"
	band, bandWarn := chooseBand(developed, opts.band(), linear)
	warnings = append(warnings, bandWarn...)
	radius := opts.TargetRadius
	if radius <= 0 {
		radius = sampleRadius(developed, band, linear, opts.TwoBody)
	}
	if radius <= 0 {
		return nil, warnings, fmt.Errorf("no still showed a measurable solar limb")
	}
	side := cropSideFor(radius, opts.cropMargin())

	frames, dropped, ferr := cropStills(ctx, sources, developed, side, band, linear, opts)
	warnings = append(warnings, dropped...)
	if ferr != nil {
		return nil, warnings, ferr
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].TimeMs < frames[j].TimeMs })
	return frames, warnings, nil
}

// developStills turns camera raws into 16-bit TIFFs, passing native formats through untouched, and
// returns the surviving source paths alongside them. A file that will not develop is reported as a
// warning rather than failing the run — one unreadable frame must not cost the session.
func developStills(ctx context.Context, paths []string, devDir string) (srcs, dev []string, warnings []string, err error) {
	out, warns, err := rawconv.PrepareTIFFWith(ctx, paths, devDir, solarDevelop, nil)
	if err != nil {
		return nil, nil, warns, err
	}
	if len(out) != len(paths) {
		// PrepareTIFF drops what it cannot develop; without a positional mapping we cannot tell
		// which source each output came from, so fall back to per-file development.
		return developOneByOne(ctx, paths, devDir, warns)
	}
	return paths, out, warns, nil
}

// developOneByOne develops each still separately so a failure is attributable to its source.
func developOneByOne(ctx context.Context, paths []string, devDir string, warnings []string) ([]string, []string, []string, error) {
	var srcs, dev []string
	for _, p := range paths {
		out, warns, err := rawconv.PrepareTIFFWith(ctx, []string{p}, devDir, solarDevelop, nil)
		warnings = append(warnings, warns...)
		if err != nil || len(out) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: could not be developed, skipped", filepath.Base(p)))
			continue
		}
		srcs, dev = append(srcs, p), append(dev, out[0])
	}
	if len(dev) == 0 {
		return nil, nil, warnings, fmt.Errorf("none of the %d still(s) could be developed", len(paths))
	}
	return srcs, dev, warnings, nil
}

// sampleRadius measures a few developed frames to establish the group's disc radius.
func sampleRadius(developed []string, band Band, linear bool, twoBody bool) float64 {
	var radii []float64
	for i := 0; i < len(developed) && len(radii) < 5; i++ {
		im, _, err := loadStillPlane(developed[i], band, linear)
		if err != nil {
			continue
		}
		if g, ok := fitGeometry(im, twoBody); ok {
			radii = append(radii, g.Sun.R)
		}
	}
	return median(radii)
}

// cropSideFor picks one square crop size for the whole group, sized to the disc plus prominence
// room. Every frame is cropped to the same size and centred on ITS OWN disc, which both bounds the
// raster everything downstream works on and leaves the frames roughly pre-aligned — registration
// then only has to correct the residual.
func cropSideFor(radius, margin float64) int {
	return (int(2*radius*(1+margin)) + 1) &^ 1
}

// cropStills loads, crops and persists each developed still.
func cropStills(ctx context.Context, srcs, developed []string, side int, band Band, linear bool, opts IngestOptions) ([]Frame, []string, error) {
	out := make([]Frame, len(srcs))
	var drops []string
	var mu sync.Mutex
	g, _ := errgroup.WithContext(ctx)
	g.SetLimit(ingestWorkers())
	for i := range srcs {
		i := i
		g.Go(func() error {
			im, clipped, err := loadStillPlane(developed[i], band, linear)
			if err != nil {
				return err
			}
			if clipped > clippedFracLimit {
				// A frame blown in the channel the group reads is unrecoverable, and averaging it in
				// would flatten the very features it was meant to contribute.
				mu.Lock()
				drops = append(drops, fmt.Sprintf("%s: %.0f%% of the frame is saturated in the %s channel, skipped",
					filepath.Base(srcs[i]), 100*clipped, band))
				mu.Unlock()
				return nil
			}
			g, ok := fitGeometry(im, opts.TwoBody)
			if !ok {
				return nil // reported as a gap; triage already explains why a frame has no limb
			}
			l := g.Sun
			crop := cropAround(im, l.CX, l.CY, side)
			dst := filepath.Join(opts.WorkDir, sanitizeName(srcs[i])+".fits")
			if err := crop.WriteFITS(dst); err != nil {
				return err
			}
			meta := readStillMeta(srcs[i])
			f := Frame{Path: dst, Source: srcs[i], Index: i, TimeMs: meta.TakenAtMs}
			// Re-fitted on the CROP, because everything downstream works in the cropped frame's
			// own coordinates and a geometry measured before the crop would be offset by it.
			if cg, ok := fitGeometry(crop, opts.TwoBody); ok {
				f.Limb, f.Moon = cg.Sun, cg.Moon
				f.Score = FrameSharpness(crop, cg.Sun)
			}
			out[i] = f
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, drops, err
	}
	kept := out[:0]
	for _, f := range out {
		if f.Path != "" {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		return nil, drops, fmt.Errorf("every still was dropped; see the warnings")
	}
	sort.Strings(drops)
	return kept, drops, nil
}

// chooseBand decides ONCE, for the whole group, which channel the signal is read from.
//
// Deciding per frame is wrong even though it looks harmless: the same subject through the same
// etalon can fall either side of the threshold depending on exposure, and the group then mixes
// frames read from the red channel with frames read from an RGB mean. Those have different noise,
// different effective transfer and different saturation points, so they neither normalise onto one
// scale nor stack cleanly — and the inconsistency is invisible in the result.
func chooseBand(developed []string, want Band, linear bool) (Band, []string) {
	if want != "" && want != BandAuto {
		return want, nil
	}
	var votes []Band
	for i := 0; i < len(developed) && len(votes) < 5; i++ {
		f, err := os.Open(developed[i])
		if err != nil {
			continue
		}
		img, derr := decodeAny(f, developed[i])
		f.Close()
		if derr != nil {
			continue
		}
		r, g, _ := splitRGB(img)
		votes = append(votes, detectBand(r, g))
	}
	ha := 0
	for _, v := range votes {
		if v == BandHAlpha {
			ha++
		}
	}
	if len(votes) == 0 {
		return BandWhiteLight, nil
	}
	if ha*2 >= len(votes) {
		return BandHAlpha, nil
	}
	return BandWhiteLight, nil
}

// cropAround extracts a side×side window centred on (cx, cy), padding with the frame's own edge
// where the window runs past it.
func cropAround(im *fits.Image, cx, cy float64, side int) *fits.Image {
	out := fits.NewImage(side, side, 1)
	x0 := int(math.Round(cx)) - side/2
	y0 := int(math.Round(cy)) - side/2
	for y := 0; y < side; y++ {
		sy := clampInt(y0+y, 0, im.H-1)
		for x := 0; x < side; x++ {
			out.Pix[0][y*side+x] = im.Pix[0][sy*im.W+clampInt(x0+x, 0, im.W-1)]
		}
	}
	return out
}

// loadStillPlane decodes a developed still into a single linear-light plane, reading the channel
// the band's signal lives in, and reports what fraction of the frame is saturated in that channel.
// linear says the file is already in linear light and needs no transfer-curve inversion.
func loadStillPlane(path string, band Band, linear bool) (*fits.Image, float64, error) {
	if fitsExts[strings.ToLower(filepath.Ext(path))] {
		im, err := fits.ReadImage(path)
		if err != nil {
			return nil, 0, err
		}
		mono := firstPlane(im) // FITS is already linear sensor data
		return mono, clippedFraction(mono.Pix[0]), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	img, err := decodeAny(f, path)
	if err != nil {
		return nil, 0, err
	}
	r, g, b := splitRGB(img)
	plane := r.Pix[0]
	if band != BandHAlpha {
		// White light carries signal in every channel, so use all of it: the mean is both less noisy
		// than any single channel and closer to what the eye judges a photospheric image on.
		plane = meanPlane(r, g, b)
	}
	if !linear {
		// Only the sips fallback reaches here: it renders through Apple's tone curve, which the sRGB
		// inverse approximates. dcraw frames are already linear by construction (see solarDevelop).
		linearizeSDRGamma(plane)
	}
	return &fits.Image{W: r.W, H: r.H, C: 1, Pix: [][]float32{plane}}, clippedFraction(plane), nil
}

// decodeAny decodes a developed still, choosing the decoder by extension.
func decodeAny(f *os.File, path string) (image.Image, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".tif" || ext == ".tiff" {
		img, err := tiff.Decode(f)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
		}
		return img, nil
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return img, nil
}

// clippedFraction is the share of a plane sitting at or above saturation.
//
// It is measured on the plane the group will actually stack, which is the only measurement that
// means anything: an Hα frame read from the red channel can be a third saturated while its RGB
// average — what a thumbnail shows, and what a colour-aware check would test — looks perfectly
// exposed, because the untouched green and blue pull the average down.
func clippedFraction(p []float32) float64 {
	if len(p) == 0 {
		return 0
	}
	n := 0
	for _, v := range p {
		if float64(v) >= satLevel {
			n++
		}
	}
	return float64(n) / float64(len(p))
}

// detectBand decides whether a frame was shot through an Hα etalon, from how far red dominates.
//
// It matters because through a 0.6 Å filter the green and blue channels hold nothing but read noise
// and demosaic bleed: averaging them into a luminance would dilute the only real signal by three.
func detectBand(r, g *fits.Image) Band {
	rm := imgops.Percentile(imgops.Subsample(r.Pix[0], 100000), 99)
	gm := imgops.Percentile(imgops.Subsample(g.Pix[0], 100000), 99)
	if gm > 1e-6 && rm/gm >= haRedRatio {
		return BandHAlpha
	}
	return BandWhiteLight
}

// splitRGB decodes an image into three 0..1 planes.
func splitRGB(img image.Image) (r, g, b *fits.Image) {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	r, g, b = fits.NewImage(w, h, 1), fits.NewImage(w, h, 1), fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cr, cg, cb, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			i := y*w + x
			r.Pix[0][i] = float32(cr) / 65535
			g.Pix[0][i] = float32(cg) / 65535
			b.Pix[0][i] = float32(cb) / 65535
		}
	}
	return r, g, b
}

// meanPlane averages three planes into one.
func meanPlane(r, g, b *fits.Image) []float32 {
	out := make([]float32, len(r.Pix[0]))
	for i := range out {
		out[i] = (r.Pix[0][i] + g.Pix[0][i] + b.Pix[0][i]) / 3
	}
	return out
}
