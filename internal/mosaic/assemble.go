// Package mosaic assembles an offset-panel mosaic: N overlapping panels — each already stacked
// into per-channel master FITS by the deepsky pipeline, each carrying a plate-solved TAN WCS —
// are placed onto ONE north-up/east-left canvas by WCS reprojection (Catmull-Rom), cross-panel
// photometric matching (gain+offset over the overlap graph, loop-closure least squares) and
// center-weighted feathered blending. The package is pure image/geometry math: no Siril, no DB,
// no HTTP — plate-solving and file orchestration live in the pipeline.
package mosaic

import (
	"context"
	"errors"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// AssembleChannel reprojects and blends every panel of one channel onto the canvas:
// out = Σ w·(v·gain+offset) / Σ w, zero where nothing covers. sol may be nil (identity); its
// indices follow the panel slice order. Returns the canvas image (C==1), the per-channel report,
// and the per-cell coverage depth (Σ of each covering panel's weight, for the seam-noise
// equalizer + coverage preview).
func AssembleChannel(ctx context.Context, panels []PanelImage, canvas CanvasSpec, sol *PhotomSolution, opts Options) (*fits.Image, *ChannelAssembly, []float32, error) {
	if canvas.W <= 0 || canvas.H <= 0 {
		return nil, nil, nil, errors.New("mosaic: empty canvas")
	}
	if len(panels) == 0 {
		return nil, nil, nil, errors.New("mosaic: no panels to assemble")
	}
	if err := checkPanels(panels); err != nil {
		return nil, nil, nil, err
	}
	gains, offsets, err := solTerms(sol, len(panels))
	if err != nil {
		return nil, nil, nil, err
	}
	opts = opts.withDefaults()

	sum := make([]float32, canvas.W*canvas.H)
	wsum := make([]float32, canvas.W*canvas.H)
	placements, err := accumulateAll(ctx, panels, canvas, gains, offsets, opts, sum, wsum)
	if err != nil {
		return nil, nil, nil, err
	}

	out := fits.NewImage(canvas.W, canvas.H, 1)
	dst := out.Pix[0]
	covered := 0
	for i, w := range wsum {
		if w > 0 {
			dst[i] = sum[i] / w
			covered++
		}
	}
	rep := &ChannelAssembly{
		Panels:      placements,
		SeamRMS:     seamRMS(panels, canvas, gains, offsets),
		CoveredFrac: float64(covered) / float64(canvas.W*canvas.H),
	}
	return out, rep, wsum, nil
}

// accumulateAll blends every panel into the shared accumulators (weights built per panel, rows
// parallel inside accumulatePanel) and reports each panel's written placement.
func accumulateAll(ctx context.Context, panels []PanelImage, canvas CanvasSpec, gains, offsets []float64, opts Options, sum, wsum []float32) ([]PanelPlacement, error) {
	placements := make([]PanelPlacement, 0, len(panels))
	for i, p := range panels {
		x0, y0, x1, y1, ok := panelCanvasBBox(p, canvas)
		if !ok {
			continue
		}
		wm := BuildWeights(p.Image, opts.FeatherFrac, opts.OverlapFrac, opts.EdgeErodePx)
		if err := accumulatePanel(ctx, p, wm, gains[i], offsets[i], canvas, sum, wsum, opts.Workers); err != nil {
			return nil, fmt.Errorf("mosaic: accumulate panel %s: %w", p.Label, err)
		}
		placements = append(placements, PanelPlacement{
			Label: p.Label, X0: x0, Y0: y0, X1: x1, Y1: y1, Gain: gains[i], Offset: offsets[i],
		})
	}
	return placements, nil
}

// solTerms flattens an optional photometric solution into per-panel gain/offset slices (identity
// when sol is nil).
func solTerms(sol *PhotomSolution, n int) (gains, offsets []float64, err error) {
	if sol == nil {
		id := identitySolution(n)
		return id.Gain, id.Offset, nil
	}
	if len(sol.Gain) != n || len(sol.Offset) != n {
		return nil, nil, fmt.Errorf("mosaic: photometric solution covers %d panel(s), assembly has %d", len(sol.Gain), n)
	}
	return sol.Gain, sol.Offset, nil
}
