package trail

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// FrameResult reports what MaskFrame changed in a frame.
type FrameResult struct {
	Segments []Segment
	MaskedPx int
}

// ApplySwathMedian replaces every swath pixel of s with the co-located value from medianPlane (e.g. a
// per-pixel median stack) and returns the number of pixels changed. It is a no-op on a size mismatch.
func ApplySwathMedian(plane, medianPlane []float32, w, h int, s Segment) int {
	if len(plane) != w*h || len(medianPlane) != w*h {
		return 0
	}
	x0, y0, x1, y1 := swathBBox(s, w, h)
	n := 0
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if s.Contains(float64(x), float64(y)) {
				i := y*w + x
				plane[i] = medianPlane[i]
				n++
			}
		}
	}
	return n
}

// ApplySwathLocalBG paints every swath pixel of s with a locally-sampled background plus matching
// noise: for each pixel it samples up to 8 off-swath pixels per side along the perpendicular at
// distances in [1.5·Width, 3·Width], sets the value to median(samples) + N(0, 1.4826·MAD). With fewer
// than 4 samples it uses the median only. seed makes the noise deterministic. Returns pixels changed.
func ApplySwathLocalBG(plane []float32, w, h int, s Segment, seed int64) int {
	if len(plane) != w*h {
		return 0
	}
	rng := rand.New(rand.NewSource(seed))
	x0, y0, x1, y1 := swathBBox(s, w, h)
	n := 0
	buf := make([]float64, 0, 16)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if !s.Contains(float64(x), float64(y)) {
				continue
			}
			samples := sampleSides(plane, w, h, s, x, y, rng, buf)
			if len(samples) == 0 {
				continue
			}
			bg, sig := medMAD(samples)
			if len(samples) >= 4 {
				bg += rng.NormFloat64() * sig
			}
			plane[y*w+x] = float32(bg)
			n++
		}
	}
	return n
}

// sampleSides draws 8 perpendicular offsets per side (so the RNG stream advances deterministically),
// collecting the in-bounds, off-swath background values into out[:0].
func sampleSides(plane []float32, w, h int, s Segment, x, y int, rng *rand.Rand, out []float64) []float64 {
	out = out[:0]
	lo, hi := 1.5*s.Width, 3.0*s.Width
	for _, side := range [2]float64{-1, 1} {
		for k := 0; k < 8; k++ {
			dist := lo + (hi-lo)*rng.Float64()
			ix := int(math.Round(float64(x) + side*dist*s.Nx))
			iy := int(math.Round(float64(y) + side*dist*s.Ny))
			if ix < 0 || ix >= w || iy < 0 || iy >= h || s.Contains(float64(ix), float64(iy)) {
				continue
			}
			out = append(out, float64(plane[iy*w+ix]))
		}
	}
	return out
}

// MaskFrame detects trails on a mono/per-pixel-max plane and paints each segment out of every channel
// with a per-(segment,channel) deterministic local-background fill. It never panics; a frame with no
// trail is returned unchanged.
func MaskFrame(im *fits.Image, p Params) FrameResult {
	if im == nil || im.W <= 0 || im.H <= 0 || im.C <= 0 || len(im.Pix) < im.C {
		return FrameResult{}
	}
	segs := DetectSegments(detectionPlane(im), im.W, im.H, p)
	res := FrameResult{Segments: segs}
	for si, s := range segs {
		for ch := 0; ch < im.C; ch++ {
			res.MaskedPx += ApplySwathLocalBG(im.Pix[ch], im.W, im.H, s, maskSeed(si, ch))
		}
	}
	return res
}

// MaskFrameFile reads a FITS frame, masks its trails and (only if anything changed) rewrites the pixel
// data in place, preserving the header.
func MaskFrameFile(path string, p Params) (FrameResult, error) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return FrameResult{}, fmt.Errorf("trail: read %s: %w", path, err)
	}
	res := MaskFrame(im, p)
	if res.MaskedPx > 0 {
		if err := im.OverwriteData(path); err != nil {
			return res, fmt.Errorf("trail: write %s: %w", path, err)
		}
	}
	return res, nil
}

// detectionPlane returns plane 0 for mono or the per-pixel max over channels for RGB (detection only).
func detectionPlane(im *fits.Image) []float32 {
	if im.C == 1 {
		return im.Pix[0]
	}
	out := make([]float32, im.W*im.H)
	for i := range out {
		m := im.Pix[0][i]
		for c := 1; c < im.C; c++ {
			if v := im.Pix[c][i]; v > m {
				m = v
			}
		}
		out[i] = m
	}
	return out
}

// maskSeed is a stable per-(segment,channel) noise seed, so channels get uncorrelated fills.
func maskSeed(segIdx, ch int) int64 {
	return int64(segIdx+1)*1000003 + int64(ch)*101
}

// swathBBox is the image-clamped axis-aligned bounding box of the masking swath (the rotated rectangle
// of half-width Width over [T0,T1]).
func swathBBox(s Segment, w, h int) (x0, y0, x1, y1 int) {
	dirx, diry := s.dirVec()
	p0x, p0y := s.Nx*s.C, s.Ny*s.C
	hw := swathDilate * s.Width / 2
	minx, miny := math.Inf(1), math.Inf(1)
	maxx, maxy := math.Inf(-1), math.Inf(-1)
	for _, t := range [2]float64{s.T0, s.T1} {
		bx, by := p0x+t*dirx, p0y+t*diry
		for _, sg := range [2]float64{-1, 1} {
			cx, cy := bx+sg*hw*s.Nx, by+sg*hw*s.Ny
			minx, maxx = math.Min(minx, cx), math.Max(maxx, cx)
			miny, maxy = math.Min(miny, cy), math.Max(maxy, cy)
		}
	}
	return clampi(int(math.Floor(minx)), 0, w-1), clampi(int(math.Floor(miny)), 0, h-1),
		clampi(int(math.Ceil(maxx)), 0, w-1), clampi(int(math.Ceil(maxy)), 0, h-1)
}

// medMAD returns the median and MAD-based sigma (1.4826·MAD) of vals (mutates a copy).
func medMAD(vals []float64) (med, sigma float64) {
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	med = cp[len(cp)/2]
	dev := make([]float64, len(cp))
	for i, v := range cp {
		dev[i] = math.Abs(v - med)
	}
	sort.Float64s(dev)
	return med, 1.4826 * dev[len(dev)/2]
}
