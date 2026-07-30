package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mosaic"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Assembly of the stacked, solved panels onto the one mosaic canvas, per channel: canvas planning,
// photometric matching over the overlap graph (gains solved once on the broadband reference and
// shared; offsets refit per channel — pedestals are filter-dependent), reprojection + blending
// (internal/mosaic), the crop policy, and the aligned_<tag>.fits handoff to the standard finish.

// MosaicResult records a Mode "mosaic" run's assembly for run.json.
type MosaicResult struct {
	PlanID  int64               `json:"plan_id,omitempty"`
	Grid    string              `json:"grid"`
	CanvasW int                 `json:"canvas_w,omitempty"`
	CanvasH int                 `json:"canvas_h,omitempty"`
	Panels  []MosaicPanelResult `json:"panels"`
	Pairs   []mosaic.PairFit    `json:"pairs,omitempty"`
	SeamRMS map[string]float64  `json:"seam_rms,omitempty"`
}

// MosaicPanelResult is one panel's fate: solved + assembled, or dropped with its reason.
type MosaicPanelResult struct {
	Label         string  `json:"label"`
	Frames        int     `json:"frames"`
	SolveRA       float64 `json:"solve_ra,omitempty"`
	SolveDec      float64 `json:"solve_dec,omitempty"`
	ScaleArcsecPx float64 `json:"scale_arcsec_px,omitempty"`
	Gain          float64 `json:"gain,omitempty"`
	Offset        float64 `json:"offset,omitempty"`
	Dropped       bool    `json:"dropped,omitempty"`
	DropReason    string  `json:"drop_reason,omitempty"`
}

// mosaicAssembleSteps sizes the assembly's share of the progress bar: one photometry step + one per
// distinct channel.
func mosaicAssembleSteps(work []panelWork) int {
	return 1 + len(mosaicUnionFilters(work))
}

func mosaicUnionFilters(work []panelWork) []string {
	set := map[string]string{}
	for _, w := range work {
		for _, f := range w.filters {
			set[f] = ""
		}
	}
	return orderedFilters(set)
}

// assembleMosaic builds the per-channel canvases from the solved panels and returns the channels
// map finishAligned consumes (filter → aligned base name in outDir).
func assembleMosaic(ctx context.Context, opts Options, res *Result, solved []solvedPanel,
	workRun, outDir string, progress func(string) func(siril.Progress)) (map[string]string, error) {
	assembly := res.MosaicAssembly
	preset := opts.Preset
	workers := mosaicWorkers()

	// Canvas + gains from the broadband reference channel.
	refFilter := mosaicAssemblyReference(solved)
	refImages, err := loadPanelChannel(solved, refFilter)
	if err != nil {
		return nil, fmt.Errorf("mosaic: load %s panels: %w", refFilter, err)
	}
	canvas, err := planMosaicCanvas(opts, refImages)
	if err != nil {
		return nil, fmt.Errorf("mosaic: %w", err)
	}
	assembly.CanvasW, assembly.CanvasH = canvas.W, canvas.H
	opts.report(Progress{Line: fmt.Sprintf("mosaic canvas %dx%d px at %.2f″/px (north-up)",
		canvas.W, canvas.H, canvas.WCS.ScaleArcsecPerPix())})

	progress("matching panel photometry")
	sol, err := mosaic.FitPhotometry(ctx, refImages, canvas, preset.MosaicPhotomMatch, workers)
	if err != nil {
		warnLive(opts, res, "mosaic: photometric matching failed — assembling uncorrected: "+err.Error())
		sol = nil
	}
	recordPhotometry(assembly, sol, solved)

	channels := map[string]string{}
	seamRMS := map[string]float64{}
	crop := newMosaicCropTracker(canvas)
	for _, filter := range mosaicSolvedFilters(solved) {
		progress("assembling mosaic " + filter)
		images := refImages
		chSol := sol
		if filter != refFilter {
			if images, err = loadPanelChannel(solved, filter); err != nil {
				warnLive(opts, res, fmt.Sprintf("mosaic: %s panels unreadable — channel skipped: %v", filter, err))
				continue
			}
			if sol != nil {
				if chSol, err = sol.RefitOffsets(ctx, images, canvas, workers); err != nil {
					warnLive(opts, res, fmt.Sprintf("mosaic: %s offset refit failed — using the reference solution: %v", filter, err))
					chSol = sol
				}
			}
		}
		img, chAsm, coverage, aerr := mosaic.AssembleChannel(ctx, images, canvas, chSol, mosaic.Options{
			FeatherFrac: preset.MosaicFeatherFrac,
			OverlapFrac: preset.MosaicOverlapExpected,
			EdgeErodePx: 8,
			Workers:     workers,
		})
		if aerr != nil {
			warnLive(opts, res, fmt.Sprintf("mosaic: assembling %s failed — channel skipped: %v", filter, aerr))
			continue
		}
		seamRMS[filter] = chAsm.SeamRMS
		crop.observe(coverage)
		tmp := filepath.Join(workRun, "canvas_"+filterTag(filter)+".fits")
		if err := img.WriteFITSWith(tmp, canvas.WCS.Cards()); err != nil {
			return nil, fmt.Errorf("mosaic: write %s canvas: %w", filter, err)
		}
		channels[filter] = tmp // full canvas for now; cropped + moved into outDir below
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("mosaic: no channel could be assembled — see the run warnings")
	}
	assembly.SeamRMS = seamRMS

	rect := crop.rect(preset.MosaicCanvasCrop, opts.MosaicPlan)
	if err := finalizeMosaicChannels(ctx, opts, res, channels, canvas, rect, outDir); err != nil {
		return nil, err
	}
	return channels, nil
}

// finalizeMosaicChannels crops every canvas to the chosen rectangle, adjusts the WCS reference
// pixel, writes outDir/aligned_<tag>.fits (the refine-compatible handoff name) and records the
// per-channel results + previews.
func finalizeMosaicChannels(ctx context.Context, opts Options, res *Result,
	channels map[string]string, canvas mosaic.CanvasSpec, rect cropRect, outDir string) error {
	for slot, filter := range orderedFilters(channels) {
		img, err := fits.ReadImage(channels[filter])
		if err != nil {
			return fmt.Errorf("mosaic: reload %s canvas: %w", filter, err)
		}
		img = cropCanvasImage(img, rect)
		w, ok := fits.NewTanWCS(canvas.WCS.RA0, canvas.WCS.Dec0,
			canvas.WCS.CRPix1-float64(rect.x0), canvas.WCS.CRPix2-float64(rect.y0), canvas.WCS.CD)
		if !ok {
			return fmt.Errorf("mosaic: cropped canvas WCS is degenerate")
		}
		base := "aligned_" + filterTag(filter)
		path := filepath.Join(outDir, base+".fits")
		if err := img.WriteFITSWith(path, w.Cards()); err != nil {
			return fmt.Errorf("mosaic: write %s: %w", filter, err)
		}
		channels[filter] = base // finishAligned/refine take base names relative to outDir

		frames := 0
		for _, p := range res.MosaicAssembly.Panels {
			if !p.Dropped {
				frames += p.Frames
			}
		}
		preview := captureSessionPreview(ctx, opts, outDir, ordAligned+slot, stageAligned, filter, "", path, true)
		res.Channels = append(res.Channels, ChannelResult{
			Object: res.Object, Filter: filter,
			InputFrames: frames, StackedFrames: frames,
			OutputPath: path, PreviewPath: preview,
		})
	}
	return nil
}

// mosaicAssemblyReference picks the channel whose panels drive the canvas + gain fit: L when every
// solved panel has it, else the channel the most panels share.
func mosaicAssemblyReference(solved []solvedPanel) string {
	counts := map[string]int{}
	set := map[string]string{}
	for _, sp := range solved {
		for f := range sp.aligned {
			counts[f]++
			set[f] = ""
		}
	}
	if counts["L"] == len(solved) && len(solved) > 0 {
		return "L"
	}
	best, bestN := "", -1
	for _, f := range orderedFilters(set) {
		if counts[f] > bestN {
			best, bestN = f, counts[f]
		}
	}
	return best
}

func mosaicSolvedFilters(solved []solvedPanel) []string {
	set := map[string]string{}
	for _, sp := range solved {
		for f := range sp.aligned {
			set[f] = ""
		}
	}
	return orderedFilters(set)
}

// loadPanelChannel reads one channel's aligned master from every solved panel that has it.
func loadPanelChannel(solved []solvedPanel, filter string) ([]mosaic.PanelImage, error) {
	var out []mosaic.PanelImage
	for _, sp := range solved {
		base, ok := sp.aligned[filter]
		if !ok {
			continue
		}
		img, err := fits.ReadImage(filepath.Join(sp.outDir, base+".fits"))
		if err != nil {
			return nil, fmt.Errorf("panel %s: %w", sp.work.panel.Label, err)
		}
		out = append(out, mosaic.PanelImage{Label: sp.work.panel.Label, Image: img, WCS: sp.wcs})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no panel carries channel %s", filter)
	}
	return out, nil
}

// recordPhotometry copies the fitted per-panel corrections + pair fits into the run record.
func recordPhotometry(assembly *MosaicResult, sol *mosaic.PhotomSolution, solved []solvedPanel) {
	if sol == nil {
		return
	}
	assembly.Pairs = sol.Pairs
	for i, sp := range solved {
		if i >= len(sol.Gain) {
			break
		}
		for j := range assembly.Panels {
			if assembly.Panels[j].Label == sp.work.panel.Label && !assembly.Panels[j].Dropped {
				assembly.Panels[j].Gain = sol.Gain[i]
				assembly.Panels[j].Offset = sol.Offset[i]
			}
		}
	}
}

// planMosaicCanvas sizes the canvas: the plan center anchors the tangent point when a plan is
// referenced, else the solve centroid.
func planMosaicCanvas(opts Options, refImages []mosaic.PanelImage) (mosaic.CanvasSpec, error) {
	if plan := opts.MosaicPlan; plan != nil {
		return mosaic.PlanCanvas(refImages, plan.CenterRA, plan.CenterDec, true)
	}
	return mosaic.PlanCanvas(refImages, 0, 0, false)
}

// cropRect is the assembled-canvas crop window (x0,y0 inclusive → x1,y1 exclusive).
type cropRect struct{ x0, y0, x1, y1 int }

// mosaicCropTracker intersects every channel's covered bounding box — the "common" crop policy.
type mosaicCropTracker struct {
	canvas mosaic.CanvasSpec
	common cropRect
	seen   bool
}

func newMosaicCropTracker(canvas mosaic.CanvasSpec) *mosaicCropTracker {
	return &mosaicCropTracker{canvas: canvas}
}

// observe folds one channel's coverage (per-pixel blend weight sums, len W·H) into the common box.
func (t *mosaicCropTracker) observe(coverage []float32) {
	w, h := t.canvas.W, t.canvas.H
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			if coverage[row+x] > 0 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		return
	}
	box := cropRect{minX, minY, maxX + 1, maxY + 1}
	if !t.seen {
		t.common, t.seen = box, true
		return
	}
	t.common = cropRect{
		maxInt(t.common.x0, box.x0), maxInt(t.common.y0, box.y0),
		minIntM(t.common.x1, box.x1), minIntM(t.common.y1, box.y1),
	}
}

// rect resolves the crop policy: common (default) = the tracked intersection box, union = the full
// canvas, plan = the plan's tile-grid bounding box projected onto the canvas.
func (t *mosaicCropTracker) rect(policy string, plan *mosaic.Plan) cropRect {
	full := cropRect{0, 0, t.canvas.W, t.canvas.H}
	switch policy {
	case "union":
		return full
	case "plan":
		if plan == nil {
			return t.commonOrFull(full)
		}
		return t.planBox(plan, full)
	default:
		return t.commonOrFull(full)
	}
}

func (t *mosaicCropTracker) commonOrFull(full cropRect) cropRect {
	if !t.seen || t.common.x1 <= t.common.x0 || t.common.y1 <= t.common.y0 {
		return full
	}
	return t.common
}

func (t *mosaicCropTracker) planBox(plan *mosaic.Plan, full cropRect) cropRect {
	// The plan gives tile centers; half a tile step beyond the outermost centers approximates the
	// grid bbox — clamped to the canvas either way.
	minX, minY, maxX, maxY := full.x1, full.y1, 0, 0
	for _, tile := range plan.Tiles {
		x, y, ok := t.canvas.WCS.SkyToPix(tile.RA, tile.Dec)
		if !ok {
			continue
		}
		minX, minY = minIntM(minX, int(x)), minIntM(minY, int(y))
		maxX, maxY = maxInt(maxX, int(x)), maxInt(maxY, int(y))
	}
	if maxX <= minX || maxY <= minY {
		return t.commonOrFull(full)
	}
	// Grow by the mean tile half-extent estimated from neighboring centers.
	grow := (maxX - minX + maxY - minY) / (2 * maxInt(plan.Cols+plan.Rows-2, 1))
	return cropRect{
		maxInt(0, minX-grow), maxInt(0, minY-grow),
		minIntM(full.x1, maxX+grow), minIntM(full.y1, maxY+grow),
	}
}

func cropCanvasImage(img *fits.Image, r cropRect) *fits.Image {
	if r.x0 == 0 && r.y0 == 0 && r.x1 == img.W && r.y1 == img.H {
		return img
	}
	out := fits.NewImage(r.x1-r.x0, r.y1-r.y0, img.C)
	for c := 0; c < img.C; c++ {
		for y := r.y0; y < r.y1; y++ {
			src := img.Pix[c][y*img.W+r.x0 : y*img.W+r.x1]
			copy(out.Pix[c][(y-r.y0)*out.W:], src)
		}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minIntM(a, b int) int {
	if a < b {
		return a
	}
	return b
}
