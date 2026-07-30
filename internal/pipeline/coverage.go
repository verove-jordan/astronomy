// Coverage-aware colour combine (task #354): every channel master is stacked on the anchor-night
// canvas, but each night covers its own rotated footprint — and each channel's surviving nights
// differ again. Combining geometrically different channels produces REGIONAL colour casts no
// global calibration can fix, plus black wedges the fixed export crop never removes. The coverage
// grid counts, per downsampled canvas cell, how many STACKED frames cover it; the combine then
// crops every channel to the largest rectangle covered by ALL of them, with an honest union
// fallback (plus warning) when the intersection collapses.
package pipeline

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	// coverageDownscale is the grid downsampling vs the anchor canvas: ±8 px rect precision.
	coverageDownscale = 8
	// coverageMinRectFrac is the honest-fallback floor: a common rectangle below this fraction of
	// the canvas area means the nights barely intersect — keep the union and say so rather than
	// deliver a sliver (the task #312 lesson, applied to cropping).
	coverageMinRectFrac = 0.35
)

// canvasSpec is the target canvas geometry for coverage rasterization. Zero offsets with the frame
// dims = today's anchor canvas; the mosaic union canvas passes its own dims plus the anchor-frame
// origin shift (a frame corner lands at its homography output + Off).
type canvasSpec struct {
	W, H       int
	OffX, OffY float64
}

// coverageGrid is one channel's footprint-coverage histogram on a downsampled canvas.
type coverageGrid struct {
	W, H   int // grid dims (canvas dims / Scale, rounded up)
	Counts []uint16
	Frames int        // stacked frames accumulated
	Scale  int        // cell size in canvas pixels
	Canvas canvasSpec // the canvas this grid rasterizes
}

// rasterizeCoverage counts, per grid cell, how many kept frames cover the cell's canvas centre on
// the legacy anchor canvas (canvas = frame rectangle, cells of coverageDownscale px).
// frameH maps 0-based merged-order indices to frame→canvas homographies (captured at registration
// review — the merged .seq is deleted after stacking); rejected reports the final grading outcome
// for an index; w/h are the canvas (= frame) dimensions.
func rasterizeCoverage(frameH map[int][9]float64, rejected func(int) bool, w, h int) *coverageGrid {
	return rasterizeCoverageOn(frameH, rejected, w, h, canvasSpec{W: w, H: h}, coverageDownscale)
}

// rasterizeCoverageOn is the general form: frames of fw×fh are rasterized onto the cv canvas
// (dims + anchor-origin offset) with cells of scale px.
func rasterizeCoverageOn(frameH map[int][9]float64, rejected func(int) bool,
	fw, fh int, cv canvasSpec, scale int) *coverageGrid {
	gw := (cv.W + scale - 1) / scale
	gh := (cv.H + scale - 1) / scale
	g := &coverageGrid{W: gw, H: gh, Counts: make([]uint16, gw*gh), Scale: scale, Canvas: cv}
	for idx, hm := range frameH {
		if rejected(idx) {
			continue
		}
		g.Frames++
		quad, ok := frameQuad(hm, fw, fh, cv)
		if !ok {
			continue
		}
		g.accumulate(quad)
	}
	return g
}

// groupFootprintMask rasterizes ONE group span's kept-frame union footprint (true = any kept frame
// of the [span.Start, span.End) merged-order range covers the cell) — the overlap-region input of
// the seam offset refit.
func groupFootprintMask(frameH map[int][9]float64, rejected func(int) bool, span groupSpan,
	fw, fh int, cv canvasSpec, scale int) []bool {
	sub := make(map[int][9]float64)
	for idx, hm := range frameH {
		if idx >= span.Start && idx < span.End {
			sub[idx] = hm
		}
	}
	g := rasterizeCoverageOn(sub, rejected, fw, fh, cv, scale)
	out := make([]bool, len(g.Counts))
	for i, c := range g.Counts {
		out[i] = c > 0
	}
	return out
}

// frameQuad maps the frame rectangle's corners onto the canvas (homography + canvas origin
// offset). ok is false for a degenerate homography (a kept frame's H is sane by the
// absurd-transform gate, so this is belt and braces).
func frameQuad(hm [9]float64, w, h int, cv canvasSpec) ([4][2]float64, bool) {
	corners := [4][2]float64{{0, 0}, {float64(w), 0}, {float64(w), float64(h)}, {0, float64(h)}}
	var quad [4][2]float64
	for i, c := range corners {
		x, y, ok := applyH3(hm, c[0], c[1])
		if !ok {
			return quad, false
		}
		quad[i] = [2]float64{x + cv.OffX, y + cv.OffY}
	}
	return quad, true
}

// accumulate bumps every grid cell whose canvas centre lies inside the quad.
func (g *coverageGrid) accumulate(quad [4][2]float64) {
	minX, minY := quad[0][0], quad[0][1]
	maxX, maxY := minX, minY
	for _, q := range quad[1:] {
		minX, maxX = math.Min(minX, q[0]), math.Max(maxX, q[0])
		minY, maxY = math.Min(minY, q[1]), math.Max(maxY, q[1])
	}
	x0 := max(0, int(minX)/g.Scale)
	y0 := max(0, int(minY)/g.Scale)
	x1 := min(g.W-1, int(maxX)/g.Scale)
	y1 := min(g.H-1, int(maxY)/g.Scale)
	for gy := y0; gy <= y1; gy++ {
		cy := (float64(gy) + 0.5) * float64(g.Scale)
		for gx := x0; gx <= x1; gx++ {
			cx := (float64(gx) + 0.5) * float64(g.Scale)
			if insideQuad(quad, cx, cy) {
				g.Counts[gy*g.W+gx]++
			}
		}
	}
}

// applyH3 maps (x, y) through a row-major 3×3 homography.
func applyH3(hm [9]float64, x, y float64) (float64, float64, bool) {
	wq := hm[6]*x + hm[7]*y + hm[8]
	if wq == 0 || math.IsNaN(wq) {
		return 0, 0, false
	}
	return (hm[0]*x + hm[1]*y + hm[2]) / wq, (hm[3]*x + hm[4]*y + hm[5]) / wq, true
}

// insideQuad reports whether (x, y) lies inside the convex quad (either winding).
func insideQuad(q [4][2]float64, x, y float64) bool {
	sign := 0.0
	for i := 0; i < 4; i++ {
		j := (i + 1) % 4
		cross := (q[j][0]-q[i][0])*(y-q[i][1]) - (q[j][1]-q[i][1])*(x-q[i][0])
		if cross == 0 {
			continue
		}
		if sign == 0 {
			sign = cross
			continue
		}
		if (cross > 0) != (sign > 0) {
			return false
		}
	}
	return sign != 0
}

// mask thresholds the grid: a cell is covered when at least max(1, minFrac·Frames) stacked frames
// reach it — the same minimum-coverage philosophy as the nightscape drift-edge fill.
func (g *coverageGrid) mask(minFrac float64) []bool {
	need := uint16(max(1, int(math.Round(minFrac*float64(g.Frames)))))
	out := make([]bool, len(g.Counts))
	for i, c := range g.Counts {
		out[i] = c >= need
	}
	return out
}

// coveredFrac is the fraction of canvas cells the mask covers.
func coveredFrac(mask []bool) float64 {
	if len(mask) == 0 {
		return 0
	}
	n := 0
	for _, b := range mask {
		if b {
			n++
		}
	}
	return float64(n) / float64(len(mask))
}

// intersectMasks ANDs same-dimension masks; nil when the inputs are empty or disagree on length.
func intersectMasks(masks [][]bool) []bool {
	if len(masks) == 0 {
		return nil
	}
	out := append([]bool(nil), masks[0]...)
	for _, m := range masks[1:] {
		if len(m) != len(out) {
			return nil
		}
		for i, b := range m {
			out[i] = out[i] && b
		}
	}
	return out
}

// largestInteriorRect returns the largest axis-aligned all-true rectangle of the gw×gh mask (grid
// coords, [x0,y0]..(x1,y1) exclusive) — the classic maximal-rectangle-in-a-binary-matrix histogram
// method, O(cells).
func largestInteriorRect(mask []bool, gw, gh int) (x0, y0, x1, y1 int) {
	heights := make([]int, gw)
	bestArea := 0
	for row := 0; row < gh; row++ {
		for x := 0; x < gw; x++ {
			if mask[row*gw+x] {
				heights[x]++
			} else {
				heights[x] = 0
			}
		}
		// Largest rectangle in this row's histogram (indices stack, sentinel pass at x == gw).
		type bar struct{ start, height int }
		var stack []bar
		for x := 0; x <= gw; x++ {
			hgt := 0
			if x < gw {
				hgt = heights[x]
			}
			start := x
			for len(stack) > 0 && stack[len(stack)-1].height >= hgt {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if area := top.height * (x - top.start); area > bestArea {
					bestArea = area
					x0, y0, x1, y1 = top.start, row-top.height+1, x, row+1
				}
				start = top.start
			}
			if hgt > 0 {
				stack = append(stack, bar{start, hgt})
			}
		}
	}
	return x0, y0, x1, y1
}

// writeCoverageMaskPNG renders the grid as a small grayscale PNG (white = deepest coverage) next
// to the channel master — the UI's "where does this channel actually have data" thumbnail.
func writeCoverageMaskPNG(g *coverageGrid, path string) error {
	var peak uint16
	for _, c := range g.Counts {
		if c > peak {
			peak = c
		}
	}
	img := image.NewGray(image.Rect(0, 0, g.W, g.H))
	if peak > 0 {
		for i, c := range g.Counts {
			img.Pix[i] = uint8(uint32(c) * 255 / uint32(peak))
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// CombineCrop records the coverage-derived crop of the colour-combine inputs (canvas pixels).
type CombineCrop struct {
	X       int     `json:"x"`
	Y       int     `json:"y"`
	W       int     `json:"w"`
	H       int     `json:"h"`
	Frac    float64 `json:"frac"` // rectangle area / canvas area
	Applied bool    `json:"applied"`
	Note    string  `json:"note,omitempty"`
}

// applyCoverageCrop crops every aligned channel to the cross-channel common-coverage rectangle,
// returning a channels map (filter → extension-less basename in outDir, the alignChannels
// contract) pointing at the cropped copies combine_<tag>.fits — the aligned originals stay
// untouched for reuse/refine. Inert (returns channels unchanged) when the preset disables it, no
// channel carries a coverage grid (single-session runs), or a crop is not needed (the rect IS the
// canvas). Falls back to the union with a warning when the intersection drops below
// coverageMinRectFrac of the canvas.
func applyCoverageCrop(opts Options, res *Result, channels map[string]string, outDir string) map[string]string {
	if opts.Preset == nil || !opts.Preset.CoverageCrop {
		return channels
	}
	var masks [][]bool
	gw, gh, scale := 0, 0, coverageDownscale
	minFrac := opts.Preset.CoverageMinFrac
	for i := range res.Channels {
		ch := &res.Channels[i]
		if ch.coverage == nil || ch.Err != "" {
			continue
		}
		if _, used := channels[ch.Filter]; !used {
			continue
		}
		masks = append(masks, ch.coverage.mask(minFrac))
		gw, gh, scale = ch.coverage.W, ch.coverage.H, ch.coverage.Scale
	}
	if len(masks) == 0 {
		return channels
	}
	common := intersectMasks(masks)
	if common == nil {
		return channels
	}
	x0, y0, x1, y1 := largestInteriorRect(common, gw, gh)
	rectFrac := float64((x1-x0)*(y1-y0)) / float64(gw*gh)
	crop := &CombineCrop{
		X: x0 * scale, Y: y0 * scale,
		W: (x1 - x0) * scale, H: (y1 - y0) * scale,
		Frac: rectFrac,
	}
	res.CombineCrop = crop
	if rectFrac < coverageMinRectFrac {
		crop.Note = fmt.Sprintf("common coverage rectangle is only %.0f%% of the canvas — keeping the full field", rectFrac*100)
		warnLive(opts, res, "coverage crop skipped: "+crop.Note)
		return channels
	}
	if crop.W <= 0 || crop.H <= 0 || rectFrac > 0.98 {
		return channels // nothing meaningful to cut
	}
	out := make(map[string]string, len(channels))
	for f, base := range channels {
		tag := filterTag(f)
		dst := "combine_" + tag
		if err := cropFITS(filepath.Join(outDir, base+".fits"), filepath.Join(outDir, dst+".fits"),
			crop.X, crop.Y, crop.X+crop.W, crop.Y+crop.H); err != nil {
			warnLive(opts, res, fmt.Sprintf("coverage crop failed on %s (%v) — combining uncropped", f, err))
			return channels
		}
		out[f] = dst
	}
	crop.Applied = true
	opts.report(Progress{Line: fmt.Sprintf("✂ combine cropped to the common covered field %d×%d px (%.0f%% of canvas)", crop.W, crop.H, rectFrac*100)})
	return out
}

// cropFITS writes dst as the [x0,y0)..(x1,y1) sub-rectangle of src (clamped to its bounds).
func cropFITS(src, dst string, x0, y0, x1, y1 int) error {
	im, err := fits.ReadImage(src)
	if err != nil {
		return err
	}
	x0, y0 = max(0, x0), max(0, y0)
	x1, y1 = min(im.W, x1), min(im.H, y1)
	cw, chh := x1-x0, y1-y0
	if cw <= 0 || chh <= 0 {
		return fmt.Errorf("empty crop %dx%d", cw, chh)
	}
	out := fits.NewImage(cw, chh, len(im.Pix))
	for c := range im.Pix {
		for y := 0; y < chh; y++ {
			copy(out.Pix[c][y*cw:(y+1)*cw], im.Pix[c][(y0+y)*im.W+x0:(y0+y)*im.W+x1])
		}
	}
	return out.WriteFITS(dst)
}
