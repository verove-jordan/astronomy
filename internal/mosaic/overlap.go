package mosaic

// Overlap sampling plumbing: which panel pairs overlap on the canvas, and the paired evenly
// spaced value samples the photometric fits and the seam metric consume.

import (
	"context"
	"math"

	"golang.org/x/sync/errgroup"
)

const (
	// pairMaxSamples caps the evenly spaced canvas probes per overlap measurement.
	pairMaxSamples = 200_000
	// pairMinSamples is the floor below which an overlap is too thin to fit.
	pairMinSamples = 500
)

type pairKey struct{ a, b int }

// candidatePairs lists the panel pairs whose canvas bboxes intersect — the only ones worth
// sampling.
func candidatePairs(panels []PanelImage, canvas CanvasSpec) []pairKey {
	type box struct {
		x0, y0, x1, y1 int
		ok             bool
	}
	boxes := make([]box, len(panels))
	for i, p := range panels {
		b := &boxes[i]
		b.x0, b.y0, b.x1, b.y1, b.ok = panelCanvasBBox(p, canvas)
	}
	var out []pairKey
	for i := range panels {
		for j := i + 1; j < len(panels); j++ {
			a, b := boxes[i], boxes[j]
			if a.ok && b.ok && a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1 {
				out = append(out, pairKey{i, j})
			}
		}
	}
	return out
}

// overlapSamples returns paired values of two panels at up to maxSamples evenly spaced canvas
// points of their bbox intersection, keeping only points valid in both panels (inside the pixel
// grid, finite, both > 0). Values are bilinear samples.
func overlapSamples(a, b PanelImage, canvas CanvasSpec, maxSamples int) (va, vb []float32) {
	ax0, ay0, ax1, ay1, oka := panelCanvasBBox(a, canvas)
	bx0, by0, bx1, by1, okb := panelCanvasBBox(b, canvas)
	if !oka || !okb {
		return nil, nil
	}
	x0, y0 := max(ax0, bx0), max(ay0, by0)
	x1, y1 := min(ax1, bx1), min(ay1, by1)
	if x0 >= x1 || y0 >= y1 {
		return nil, nil
	}
	if maxSamples <= 0 {
		maxSamples = pairMaxSamples
	}
	step := 1
	if area := (x1 - x0) * (y1 - y0); area > maxSamples {
		step = int(math.Ceil(math.Sqrt(float64(area) / float64(maxSamples))))
	}
	for y := y0 + step/2; y < y1; y += step {
		for x := x0 + step/2; x < x1; x += step {
			ra, dec := canvas.WCS.PixToSky(float64(x), float64(y))
			pa, oka := samplePanel(a, ra, dec)
			if !oka {
				continue
			}
			pb, okb := samplePanel(b, ra, dec)
			if !okb {
				continue
			}
			va = append(va, pa)
			vb = append(vb, pb)
		}
	}
	return va, vb
}

// samplePanel bilinear-samples one panel at a sky position; ok=false outside the grid or for a
// non-positive/non-finite value.
func samplePanel(p PanelImage, ra, dec float64) (float32, bool) {
	sx, sy, ok := p.WCS.SkyToPix(ra, dec)
	if !ok || sx < 0 || sy < 0 || sx > float64(p.Image.W-1) || sy > float64(p.Image.H-1) {
		return 0, false
	}
	v := bilinearPlane(p.Image.Pix[0], p.Image.W, p.Image.H, sx, sy)
	if !(v > 0) || math.IsInf(float64(v), 0) {
		return 0, false
	}
	return v, true
}

// measurePairs runs the pair measurements in parallel (bounded by workers) and drops pairs whose
// overlap yields fewer than pairMinSamples valid points.
func measurePairs(ctx context.Context, panels []PanelImage, canvas CanvasSpec, cands []pairKey, workers int, fit func(a, b int, va, vb []float32) PairFit) ([]PairFit, error) {
	if workers <= 0 {
		workers = 1
	}
	out := make([]PairFit, len(cands))
	got := make([]bool, len(cands))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for k, c := range cands {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			va, vb := overlapSamples(panels[c.a], panels[c.b], canvas, pairMaxSamples)
			if len(va) < pairMinSamples {
				return nil
			}
			out[k], got[k] = fit(c.a, c.b, va, vb), true
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	pairs := make([]PairFit, 0, len(cands))
	for k, ok := range got {
		if ok {
			pairs = append(pairs, out[k])
		}
	}
	return pairs, nil
}
