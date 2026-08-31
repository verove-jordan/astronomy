package solar

import (
	"context"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// scan.go is pass one of video ingest: stream the whole clip once at reduced resolution, score
// every frame, and write nothing.
//
// The alternative — the way the planetary path does it — is `ffmpeg -i clip f_%05d.png`, which
// materialises every frame before anything has decided which are worth keeping. That is fine for a
// few hundred planetary frames and impossible here: a single 25 s 4K120 solar clip is 3,000 frames,
// and one real session's four clips come to roughly 60–100 GB of PNG. Streaming raw frames over a
// pipe costs one decode and no disk at all, so selection can happen before materialisation.

const (
	// scanMaxEdge is the long edge frames are scored at.
	//
	// It has to stay close to native. Selection is a comparison of FINE detail between frames, and
	// reducing a 4K frame to 960 px averages away the very structure that separates a sharp frame
	// from a soft one — rank them there and the order is barely better than random, which shows up
	// downstream as a stack flatter than its own inputs.
	scanMaxEdge = 1600
	// limbEveryNFrames is how often the limb is re-fitted while scanning. The Sun drifts across the
	// sensor over a clip, so the crop and the scoring window have to follow it, but it does not move
	// far in a tenth of a second.
	limbEveryNFrames = 10
)

// frameScan is one frame's verdict from the scanning pass.
type frameScan struct {
	index int
	score float64
	// level is the frame's on-disc median: how much light actually reached the sensor. It is what
	// makes a passing cloud visible as a number rather than as a vague loss of quality, and it costs
	// nothing to take — the frame is already decoded and its limb already fitted.
	level float64
	limb  Limb
	ok    bool
}

// scanResult is what pass one learns about a clip.
type scanResult struct {
	frames []frameScan
	limb   Limb // median geometry over the clip, in SCAN pixels
	scale  float64
	crop   cropRect // in full-resolution display pixels
}

// cropRect is an integer crop in full-resolution display coordinates.
type cropRect struct{ x, y, w, h int }

// scanVideo streams a clip at reduced resolution and scores every frame.
func scanVideo(ctx context.Context, ffmpegBin, path string, info VideoInfo, cropMargin float64, twoBody bool) (scanResult, error) {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	dw, dh := displayDims(info)
	sw, sh, scale := scaleTo(dw, dh, scanMaxEdge)
	if sw <= 0 || sh <= 0 {
		return scanResult{}, fmt.Errorf("scan %s: unusable dimensions %dx%d", filepath.Base(path), dw, dh)
	}

	cmd := exec.CommandContext(ctx, ffmpegBin, "-v", "error", "-i", path,
		"-vf", fmt.Sprintf("scale=%d:%d", sw, sh),
		"-f", "rawvideo", "-pix_fmt", "gray16be", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return scanResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return scanResult{}, fmt.Errorf("scan %s: %w", filepath.Base(path), err)
	}
	defer func() {
		_ = cmd.Process.Kill() // a caller cancelling mid-clip must not leave ffmpeg decoding
		_ = cmd.Wait()
	}()

	res, err := scanFrames(stdout, sw, sh, info, twoBody)
	if err != nil {
		return scanResult{}, fmt.Errorf("scan %s: %w", filepath.Base(path), err)
	}
	res.scale = scale
	res.crop = cropFor(res, scale, dw, dh, cropMargin)
	return res, nil
}

// scanFrames reads raw gray16 frames off the pipe and scores each one.
func scanFrames(r io.Reader, w, h int, info VideoInfo, twoBody bool) (scanResult, error) {
	buf := make([]byte, w*h*2)
	im := fits.NewImage(w, h, 1)
	var out scanResult
	var last Pair
	var haveLast bool

	for n := 0; ; n++ {
		if _, err := io.ReadFull(r, buf); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return out, err
		}
		decodeGray16BE(buf, im.Pix[0])
		Linearize(im.Pix[0], info)

		if n%limbEveryNFrames == 0 || !haveLast {
			if g, ok := fitGeometry(im, twoBody); ok {
				last, haveLast = g, true
			}
		}
		fs := frameScan{index: n}
		if haveLast {
			fs.limb, fs.ok = last.Sun, true
			fs.score = FrameSharpnessPair(im, last)
			fs.level = discLevelPair(im, last)
		}
		out.frames = append(out.frames, fs)
	}
	if len(out.frames) == 0 {
		return out, fmt.Errorf("no frames decoded")
	}
	out.limb = medianLimb(out.frames)
	return out, nil
}

// decodeGray16BE converts big-endian 16-bit samples into a normalised float plane.
func decodeGray16BE(src []byte, dst []float32) {
	for i := range dst {
		dst[i] = float32(uint16(src[2*i])<<8|uint16(src[2*i+1])) / 65535
	}
}

// discLevel is a frame's on-disc median brightness — its transparency.
//
// The MEDIAN over the disc interior, on a stride, and nothing more elaborate. It has to be robust to
// what is ON the Sun (plage runs 50–100% bright, filaments 40% dark, and both come and go across a
// clip) while responding to what is IN FRONT of it, and a median over tens of thousands of pixels
// does exactly that at a cost that can be paid on every frame of a 3000-frame clip. The interior
// bound keeps limb darkening out of it, so the figure does not move when the disc drifts on the
// sensor.
func discLevel(im *fits.Image, l Limb) float64 {
	return discLevelPair(im, Pair{Sun: l})
}

// discLevelPair is discLevel over the un-occluded Sun only, and on an eclipse it is the difference
// between a transparency gate and a frame shredder.
//
// The gate drops frames whose level fell below 95% of the clip's clearest, on the reasoning that the
// Sun's surface brightness does not change so a drop means something got in the way. Something DID
// get in the way — but the Moon is not cloud, and it does not dim the Sun, it covers it. Measured
// across the whole disc the level falls with obscuration, so on the seventeen-minute clip that spans
// maximum the gate would read the eclipse itself as thickening cloud and throw away the deepest and
// most interesting frames. Measured on the crescent alone the level is what it always was.
func discLevelPair(im *fits.Image, g Pair) float64 {
	l := g.Sun
	if l.R <= 0 {
		return 0
	}
	p := im.Pix[0]
	inMask := g.visibleSunAt(medianRadius)
	vals := make([]float32, 0, 40000)
	step := 1 + int(l.R)/100 // ~30k samples whatever the disc's size
	for y := 0; y < im.H; y += step {
		for x := 0; x < im.W; x += step {
			if inMask(x, y) {
				vals = append(vals, p[y*im.W+x])
			}
		}
	}
	if len(vals) == 0 {
		return 0
	}
	return imgops.Percentile(vals, 50)
}

// medianLimb is the clip's representative geometry: robust to the odd frame where a cloud or a
// wobble threw the fit off.
func medianLimb(frames []frameScan) Limb {
	var cx, cy, r []float64
	for _, f := range frames {
		if f.ok {
			cx, cy, r = append(cx, f.limb.CX), append(cy, f.limb.CY), append(r, f.limb.R)
		}
	}
	if len(r) == 0 {
		return Limb{}
	}
	return Limb{CX: median(cx), CY: median(cy), R: median(r)}
}

// cropFor is the crop every kept frame is materialised through: the union of the disc across the
// clip, plus room for prominences.
//
// Cropping before materialising is the difference between a workable run and an unworkable one. The
// disc is a fraction of a 4K frame, and everything downstream — the warp, the stack's float64
// accumulators, the drizzled raster — scales with the raster it is handed, not with the Sun in it.
func cropFor(res scanResult, scale float64, dw, dh int, margin float64) cropRect {
	if res.limb.R <= 0 {
		return cropRect{0, 0, dw, dh}
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	loY, hiY := math.Inf(1), math.Inf(-1)
	span := res.limb.R * (1 + margin)
	for _, f := range res.frames {
		if !f.ok {
			continue
		}
		lo, hi = math.Min(lo, f.limb.CX-span), math.Max(hi, f.limb.CX+span)
		loY, hiY = math.Min(loY, f.limb.CY-span), math.Max(hiY, f.limb.CY+span)
	}
	if math.IsInf(lo, 1) {
		return cropRect{0, 0, dw, dh}
	}
	x0 := clampInt(int(lo*scale), 0, dw-2)
	y0 := clampInt(int(loY*scale), 0, dh-2)
	x1 := clampInt(int(hi*scale)+1, x0+2, dw)
	y1 := clampInt(int(hiY*scale)+1, y0+2, dh)
	// Even offsets and sizes: ffmpeg's crop on subsampled chroma requires them, and an odd offset
	// would silently shift the frame by half a chroma sample.
	return cropRect{x: x0 &^ 1, y: y0 &^ 1, w: (x1 - x0) &^ 1, h: (y1 - y0) &^ 1}
}

// covers reports whether the crop is the whole frame, in which case ffmpeg can skip the filter.
func (c cropRect) covers(w, h int) bool { return c.x == 0 && c.y == 0 && c.w >= w && c.h >= h }

func (c cropRect) String() string {
	return strconv.Itoa(c.w) + "x" + strconv.Itoa(c.h) + "+" + strconv.Itoa(c.x) + "+" + strconv.Itoa(c.y)
}

// scaleTo caps the long edge at maxEdge, keeping both dimensions even, and returns the factor back
// to the original coordinates.
func scaleTo(w, h, maxEdge int) (ow, oh int, scale float64) {
	long := w
	if h > long {
		long = h
	}
	if long <= 0 {
		return 0, 0, 1
	}
	if long <= maxEdge {
		return w &^ 1, h &^ 1, 1
	}
	f := float64(long) / float64(maxEdge)
	ow, oh = int(float64(w)/f)&^1, int(float64(h)/f)&^1
	if ow < 8 || oh < 8 {
		return w &^ 1, h &^ 1, 1
	}
	return ow, oh, float64(w) / float64(ow)
}
