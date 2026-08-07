package scene3d

import (
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // registered so a run whose final output is a JPEG still gets a backdrop
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/image/tiff"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

const (
	backdropFileName = "scene3d_bg.png"
	// backdropMaxEdge caps the texture's long edge. The billboard is only ever seen at about screen
	// size, so beyond this the extra pixels cost download and VRAM and buy nothing.
	backdropMaxEdge = 2048
	// starPaintScale turns a star's measured half-max radius into the radius painted over. 2.5× covers
	// the visible disc plus its first halo without eating the nebulosity around it.
	starPaintScale = 2.5
	starPaintMinPx = 3
	starPaintMaxPx = 40
)

// backdropSources lists the images the billboard texture is cut from, best first. The starless
// output is preferred when the run produced one: it is a real star removal rather than this
// package's local-median patch, so nothing has to be reconstructed at all.
var backdropSources = []string{"final_starless.tif", "final.png"}

// writeBackdrop renders the billboard texture — the run's final image with its stars taken out —
// and returns the run-relative file name it wrote.
//
// The stars have to go. Every one of them is already drawn as a point in three dimensions, so
// leaving them on the billboard too would draw the field twice: correct-looking while the depth
// slider is at zero, and visibly wrong the moment it opens and one copy stays pinned to the object
// while the other pulls away.
func writeBackdrop(res *annotate.Result, o Options) (string, error) {
	locate := o.Locate
	if locate == nil {
		locate = func(rel string) (string, bool) {
			abs := filepath.Join(o.RunDir, rel)
			_, err := os.Stat(abs)
			return abs, err == nil
		}
	}

	var src image.Image
	var starless bool
	for _, rel := range backdropSources {
		abs, ok := locate(rel)
		if !ok {
			continue
		}
		im, err := decodeImage(abs)
		if err != nil {
			continue
		}
		src, starless = im, rel != "final.png"
		break
	}
	if src == nil {
		return "", fmt.Errorf("scene3d: no final image to cut the backdrop from")
	}

	rgba := toRGBA(src)
	if !starless {
		paintOutStars(rgba, res)
	}
	out := downsample(rgba, backdropMaxEdge)

	path := filepath.Join(o.RunDir, backdropFileName)
	if err := writeAtomic(path, func(f *os.File) error { return png.Encode(f, out) }); err != nil {
		return "", err
	}
	return backdropFileName, nil
}

// decodeImage reads a TIFF/PNG/JPEG into memory, matching internal/preview's dispatch.
func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tif", ".tiff":
		return tiff.Decode(f)
	default:
		im, _, err := image.Decode(f)
		return im, err
	}
}

// toRGBA normalises any decoded image into an 8-bit RGBA buffer we can write into.
func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			dst.SetRGBA(x, y, color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), uint8(a >> 8)})
		}
	}
	return dst
}

// paintOutStars replaces each detected star with the median of the ring around it. A median rather
// than a mean because the ring often clips a neighbouring star, and one bright intruder drags a mean
// into a visible bright blob — which is precisely the artefact this is here to avoid.
func paintOutStars(im *image.RGBA, res *annotate.Result) {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	// Scale the annotation's coordinates if the final image on disk is not the size it measured, so a
	// re-rendered output can never smear patches across the frame.
	sx, sy := 1.0, 1.0
	if res.Image.Width > 0 && res.Image.Height > 0 {
		sx, sy = float64(w)/float64(res.Image.Width), float64(h)/float64(res.Image.Height)
	}

	var ring [3][]uint8
	for _, p := range res.Stars {
		cx, cy := float64(p.X)*sx, float64(p.Y)*sy
		r := math.Max(1, p.Rpx) * starPaintScale * math.Max(sx, sy)
		r = math.Min(starPaintMaxPx, math.Max(starPaintMinPx, r))

		for c := range ring {
			ring[c] = ring[c][:0]
		}
		outer := r * 2
		for y := int(cy - outer); y <= int(cy+outer); y++ {
			for x := int(cx - outer); x <= int(cx+outer); x++ {
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				if d := math.Hypot(float64(x)-cx, float64(y)-cy); d <= r || d > outer {
					continue
				}
				i := im.PixOffset(x, y)
				ring[0] = append(ring[0], im.Pix[i])
				ring[1] = append(ring[1], im.Pix[i+1])
				ring[2] = append(ring[2], im.Pix[i+2])
			}
		}
		if len(ring[0]) == 0 {
			continue // fully off-frame: nothing to sample and nothing visible to patch
		}
		var med [3]uint8
		for c := range ring {
			sort.Slice(ring[c], func(a, b int) bool { return ring[c][a] < ring[c][b] })
			med[c] = ring[c][len(ring[c])/2]
		}
		for y := int(cy - r); y <= int(cy+r); y++ {
			for x := int(cx - r); x <= int(cx+r); x++ {
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				if math.Hypot(float64(x)-cx, float64(y)-cy) > r {
					continue
				}
				i := im.PixOffset(x, y)
				im.Pix[i], im.Pix[i+1], im.Pix[i+2] = med[0], med[1], med[2]
			}
		}
	}
}

// downsample box-filters the image so its long edge is at most maxEdge. Box rather than
// nearest-neighbour because the patched frame still holds single-pixel noise, and point-sampling it
// would alias that into the texture as a sparkle that looks exactly like the stars just removed.
func downsample(src *image.RGBA, maxEdge int) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	long := w
	if h > long {
		long = h
	}
	if long <= maxEdge || long == 0 {
		return src
	}
	scale := float64(maxEdge) / float64(long)
	dw, dh := int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale))
	if dw < 1 || dh < 1 {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*h/dh, (y+1)*h/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0, x1 := x*w/dw, (x+1)*w/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sum [4]int
			n := 0
			for yy := y0; yy < y1 && yy < h; yy++ {
				for xx := x0; xx < x1 && xx < w; xx++ {
					i := src.PixOffset(xx, yy)
					sum[0] += int(src.Pix[i])
					sum[1] += int(src.Pix[i+1])
					sum[2] += int(src.Pix[i+2])
					sum[3] += int(src.Pix[i+3])
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i] = uint8(sum[0] / n)
			dst.Pix[i+1] = uint8(sum[1] / n)
			dst.Pix[i+2] = uint8(sum[2] / n)
			dst.Pix[i+3] = uint8(sum[3] / n)
		}
	}
	return dst
}
