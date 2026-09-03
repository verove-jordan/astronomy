package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// The mosaic union canvas is built by GO-SIDE PADDING, not Siril framing: `seqapplyreg
// -framing=max` on Siril 1.4.4 emits a PER-FRAME bounding canvas (mixed dims in one sequence —
// live-pinned by TestSirilLive_FramingMaxPerFrameCanvases), so instead every merged frame is
// zero-padded by the SAME margins to the Go-computed union bbox, the padded sequence is registered
// afresh, and `framing=current` on the padded anchor reproduces the union exactly
// (TestSirilLive_PaddedReregisterUnionCanvas).
const (
	// mosaicMaxAreaFactor refuses a union canvas beyond this multiple of the sensor area — a huge
	// union means barely-overlapping sessions (a true offset-panel mosaic, out of scope) or a
	// registration blunder; the run then continues on the anchor canvas with a warning.
	mosaicMaxAreaFactor = 4.0
)

// CanvasInfo is the mosaic union-canvas geometry of a channel's master: dims plus the anchor
// frame's origin offset on it — the coordinate key shared by the coverage grid, the sky fill and
// the cross-channel padding.
type CanvasInfo struct {
	W    int `json:"w"`
	H    int `json:"h"`
	OffX int `json:"off_x"`
	OffY int `json:"off_y"`
	// AnchorW/AnchorH are the anchor FRAME's dims (the pre-mosaic canvas): with OffX/OffY they
	// locate the exact anchor rectangle inside the union, so a failed harmonize can crop every
	// channel back to identical anchor-frame masters instead of feeding the Siril combine mixed
	// dims (its pixel math hard-fails on any difference).
	AnchorW int `json:"anchor_w,omitempty"`
	AnchorH int `json:"anchor_h,omitempty"`
}

// regGeometry carries the registration geometry finishStackedChannel rasterizes coverage from:
// the kept frame homographies plus, under mosaic, the content rectangle and union canvas.
type regGeometry struct {
	FrameH             map[int][9]float64
	ContentW, ContentH int        // sensor dims of the real content (0 → the sequence frame dims)
	Canvas             canvasSpec // union canvas (zero → the sequence frame dims)
}

// applyMosaicConstraints forces the knobs a mosaic run must override: the coverage crop and export
// trim would discard the union the knob exists to keep, and the seam repair is what makes a union
// of rotated nights visually seamless. Called once at Process entry (re-entries share the preset).
func applyMosaicConstraints(p *mode.Preset) {
	if p == nil || !p.Mosaic {
		return
	}
	p.CoverageCrop = false
	p.EdgeCrop = false // the union's never-covered margins are sky-filled on purpose, not cut away
	p.CropFrac = 0
	p.SeamOffsetRefit = true
	p.SeamNoiseEq = true
	if p.MosaicFill != "fill" {
		p.MosaicFill = "crop"
	}
}

// shiftGrid re-places a coverage grid onto a new canvas, its cells moved by (leftPx, topPx)
// (± one cell precision — grid resolution). Negative offsets crop.
func shiftGrid(g *coverageGrid, leftPx, topPx, w, h int) *coverageGrid {
	ng := &coverageGrid{
		W: (w + g.Scale - 1) / g.Scale, H: (h + g.Scale - 1) / g.Scale,
		Scale: g.Scale, Canvas: canvasSpec{W: w, H: h}, Frames: g.Frames,
	}
	ng.Counts = make([]uint16, ng.W*ng.H)
	dx, dy := leftPx/g.Scale, topPx/g.Scale
	for y := 0; y < g.H; y++ {
		ny := y + dy
		if ny < 0 || ny >= ng.H {
			continue
		}
		for x := 0; x < g.W; x++ {
			nx := x + dx
			if nx < 0 || nx >= ng.W {
				continue
			}
			ng.Counts[ny*ng.W+nx] = g.Counts[y*g.W+x]
		}
	}
	return ng
}

// mosaicHarmonize pads every combining channel master onto the cross-channel common union canvas
// (anchor coordinates align them exactly — masterDimsMismatch and alignChannels then run
// unchanged) and applies the never-covered policy: "crop" (default) trims every channel to the
// largest rectangle where EVERY channel has real data; "fill" keeps the whole union (the padded
// margins are sky-filled). Inert when no used channel carries a mosaic canvas; soft-fails to the
// unpadded masters on any error.
func mosaicHarmonize(opts Options, res *Result, masters map[string]string, outDir string) map[string]string {
	if opts.Preset == nil || !opts.Preset.Mosaic {
		return masters
	}
	var infos []*ChannelResult
	for i := range res.Channels {
		ch := &res.Channels[i]
		if _, used := masters[ch.Filter]; !used || ch.Err != "" {
			continue
		}
		if ch.Canvas == nil || ch.coverage == nil {
			// This channel stayed on the anchor canvas (its union re-register soft-failed) while
			// others moved to their unions: mixed canvases would hard-fail the Siril combine, so
			// the mosaic is abandoned for ALL channels — every union master is cropped back to the
			// anchor frame and the combine proceeds without the mosaic.
			return mosaicRevertToAnchor(opts, res, masters, outDir,
				ch.Filter+" stayed on the anchor canvas")
		}
		infos = append(infos, ch)
	}
	if len(infos) == 0 {
		return masters
	}
	x0, y0, x1, y1 := 1<<30, 1<<30, -(1 << 30), -(1 << 30)
	for _, ch := range infos {
		cx0, cy0 := -ch.Canvas.OffX, -ch.Canvas.OffY
		x0, y0 = min(x0, cx0), min(y0, cy0)
		x1, y1 = max(x1, cx0+ch.Canvas.W), max(y1, cy0+ch.Canvas.H)
	}
	w, h := x1-x0, y1-y0
	out := make(map[string]string, len(masters))
	for f, b := range masters {
		out[f] = b
	}
	for _, ch := range infos {
		left, top := -ch.Canvas.OffX-x0, -ch.Canvas.OffY-y0
		if left == 0 && top == 0 && ch.Canvas.W == w && ch.Canvas.H == h {
			continue
		}
		// Map entries are the ABSOLUTE master paths channelMastersMap builds (job 367 died on
		// treating them as bare Siril names); repointed entries stay absolute for alignChannels.
		dst := filepath.Join(outDir, "mosaic_"+filterTag(ch.Filter)+".fits")
		if err := padFITS(masterFITSAbs(outDir, out[ch.Filter]), dst, left, top, w, h); err != nil {
			warnLive(opts, res, fmt.Sprintf("mosaic: padding %s to the common canvas failed (%v)", ch.Filter, err))
			// `out` + the mutated Canvas/coverage stay a CONSISTENT pair for already-padded
			// channels — revert works from them, never from the stale original map.
			return mosaicRevertToAnchor(opts, res, out, outDir, "common-canvas padding failed")
		}
		out[ch.Filter] = dst
		ch.coverage = shiftGrid(ch.coverage, left, top, w, h)
		ch.Canvas = &CanvasInfo{W: w, H: h, OffX: -x0, OffY: -y0,
			AnchorW: ch.Canvas.AnchorW, AnchorH: ch.Canvas.AnchorH}
	}
	if opts.Preset.MosaicFill == "fill" {
		for _, ch := range infos {
			fillHarmonizedMargins(opts, ch, masterFITSAbs(outDir, out[ch.Filter]))
		}
		return out
	}
	return mosaicCropCommon(opts, res, out, outDir, infos)
}

// fillHarmonizedMargins sky-fills the margins the common-canvas padding just added ("fill" mode).
func fillHarmonizedMargins(opts Options, ch *ChannelResult, path string) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return
	}
	covered := coveredPixelMask(ch.coverage, im.W, im.H)
	if frac, _ := fillNoCoverage(im, covered, fillSeed(ch.Filter, im.W, im.H)); frac > 0 {
		if err := im.OverwriteData(path); err == nil && ch.MosaicFill != nil {
			ch.MosaicFill.FilledFrac = frac
		}
	}
}

// mosaicCropCommon trims every harmonized channel to the largest rectangle covered by ALL of them
// ("crop" mode, the default): inside it every channel has real data, so no synthetic sky reaches
// the export while the canvas stays the multi-night union interior — far larger than the anchor
// frame.
func mosaicCropCommon(opts Options, res *Result, channels map[string]string, outDir string,
	infos []*ChannelResult) map[string]string {
	// The crop rectangle is the interior every BASE channel covers. A screen-only layer (Hα, [OIII] or
	// [SII] under the natural palette) is additive — it simply fades where its nights didn't reach — so
	// letting it constrain the interior would collapse a five-night mosaic to the screen layer's
	// footprint (job 371: Ha on two nights shrank the union back to anchor size).
	//
	// paletteResolved.screenOnly, not a hardcoded "Ha": under a narrowband palette these filters ARE
	// the base channels, and a no-B run folds OIII into the blue slot — in both cases the layer must
	// keep constraining the crop.
	pal, _ := resolvePalette(opts.Preset, channels)
	var masks [][]bool
	gw, gh, scale := 0, 0, coverageDownscale
	for _, ch := range infos {
		if pal.screenOnly(ch.Filter) {
			continue
		}
		masks = append(masks, ch.coverage.mask(0))
		gw, gh, scale = ch.coverage.W, ch.coverage.H, ch.coverage.Scale
	}
	if len(masks) == 0 { // emission-only combine: those layers ARE the base
		for _, ch := range infos {
			masks = append(masks, ch.coverage.mask(0))
			gw, gh, scale = ch.coverage.W, ch.coverage.H, ch.coverage.Scale
		}
	}
	common := intersectMasks(masks)
	if common == nil {
		return channels
	}
	gx0, gy0, gx1, gy1 := largestInteriorRect(common, gw, gh)
	rx, ry := gx0*scale, gy0*scale
	rw, rh := (gx1-gx0)*scale, (gy1-gy0)*scale
	canvasW, canvasH := infos[0].Canvas.W, infos[0].Canvas.H
	rw, rh = min(rw, canvasW-rx), min(rh, canvasH-ry)
	if rw <= 0 || rh <= 0 || float64(rw)*float64(rh) > 0.98*float64(canvasW)*float64(canvasH) {
		return channels // nothing meaningful to trim
	}
	out := make(map[string]string, len(channels))
	for f, b := range channels {
		out[f] = b
	}
	// Crop into a staging map and defer every channel-state mutation until ALL crops succeed: a
	// mid-loop failure must leave the harmonized (equal-dims) set fully intact — a half-cropped mix
	// would hard-fail the combine exactly like unharmonized masters.
	type cropPatch struct {
		ch     *ChannelResult
		cov    *coverageGrid
		canvas *CanvasInfo
	}
	var patches []cropPatch
	for _, ch := range infos {
		dst := filepath.Join(outDir, "mosaicc_"+filterTag(ch.Filter)+".fits")
		if err := cropFITS(masterFITSAbs(outDir, out[ch.Filter]), dst, rx, ry, rx+rw, ry+rh); err != nil {
			warnLive(opts, res, fmt.Sprintf("mosaic: crop of %s failed (%v) — combining uncropped", ch.Filter, err))
			return channels
		}
		out[ch.Filter] = dst
		patches = append(patches, cropPatch{ch, shiftGrid(ch.coverage, -rx, -ry, rw, rh),
			&CanvasInfo{W: rw, H: rh, OffX: ch.Canvas.OffX - rx, OffY: ch.Canvas.OffY - ry,
				AnchorW: ch.Canvas.AnchorW, AnchorH: ch.Canvas.AnchorH}})
	}
	for _, p := range patches {
		p.ch.coverage, p.ch.Canvas = p.cov, p.canvas
	}
	res.CombineCrop = &CombineCrop{X: rx, Y: ry, W: rw, H: rh,
		Frac: float64(rw) * float64(rh) / (float64(canvasW) * float64(canvasH)), Applied: true,
		Note: "mosaic: cropped to the union interior with data in every channel"}
	opts.report(Progress{Line: fmt.Sprintf("✂ mosaic: combine cropped to the all-channel field %d×%d px", rw, rh)})
	return out
}

// masterFITSAbs resolves a channel-masters map entry (an absolute path from channelMastersMap, or
// a bare base name from an earlier step) to its absolute FITS path.
func masterFITSAbs(outDir, entry string) string {
	p := entry
	if !filepath.IsAbs(p) {
		p = filepath.Join(outDir, p)
	}
	if filepath.Ext(p) == "" {
		p += ".fits"
	}
	return p
}

// mosaicRevertToAnchor abandons the mosaic: every channel that re-registered onto its union canvas
// is cropped back to the exact anchor-frame rectangle (located by CanvasInfo.OffX/OffY + AnchorW/H),
// so all channels return to identical sensor dims and the combine proceeds without the mosaic — the
// loud degradation path, since mixed canvases hard-fail Siril's pixel math. The entries map and each
// channel's Canvas/coverage must describe the SAME files (caller invariant).
func mosaicRevertToAnchor(opts Options, res *Result, entries map[string]string, outDir, reason string) map[string]string {
	out := make(map[string]string, len(entries))
	for f, b := range entries {
		out[f] = b
	}
	reverted := 0
	for i := range res.Channels {
		ch := &res.Channels[i]
		if _, used := out[ch.Filter]; !used || ch.Err != "" || ch.Canvas == nil {
			continue
		}
		aw, ah := ch.Canvas.AnchorW, ch.Canvas.AnchorH
		if aw <= 0 || ah <= 0 {
			warnLive(opts, res, fmt.Sprintf("mosaic: cannot revert %s to the anchor frame (no anchor dims) — the combine will report the dims mismatch", ch.Filter))
			return entries
		}
		dst := filepath.Join(outDir, "anchorrev_"+filterTag(ch.Filter)+".fits")
		if err := cropFITS(masterFITSAbs(outDir, out[ch.Filter]), dst,
			ch.Canvas.OffX, ch.Canvas.OffY, ch.Canvas.OffX+aw, ch.Canvas.OffY+ah); err != nil {
			warnLive(opts, res, fmt.Sprintf("mosaic: revert of %s to the anchor frame failed (%v)", ch.Filter, err))
			return entries
		}
		out[ch.Filter] = dst
		ch.coverage = shiftGrid(ch.coverage, -ch.Canvas.OffX, -ch.Canvas.OffY, aw, ah)
		ch.Canvas = nil
		reverted++
	}
	if reverted > 0 {
		warnLive(opts, res, fmt.Sprintf("mosaic abandoned (%s) — %d channel(s) cropped back to the anchor frame; re-run to retry the union canvas", reason, reverted))
	}
	return out
}

// padFITS writes dst as src placed at (left, top) on a w×h zero canvas — cropFITS's inverse.
func padFITS(src, dst string, left, top, w, h int) error {
	im, err := fits.ReadImage(src)
	if err != nil {
		return err
	}
	if left < 0 || top < 0 || left+im.W > w || top+im.H > h {
		return fmt.Errorf("pad placement %d,%d of %dx%d exceeds %dx%d", left, top, im.W, im.H, w, h)
	}
	out := fits.NewImage(w, h, len(im.Pix))
	for c := range im.Pix {
		for y := 0; y < im.H; y++ {
			copy(out.Pix[c][(y+top)*w+left:(y+top)*w+left+im.W], im.Pix[c][y*im.W:(y+1)*im.W])
		}
	}
	return out.WriteFITS(dst)
}

// composeContentH shifts a padded-frame homography to act on the original content rectangle:
// content pixel (x,y) sits at (x+offX, y+offY) in the padded frame, so Hc = H·T(offX, offY).
func composeContentH(frameH map[int][9]float64, offX, offY float64) map[int][9]float64 {
	out := make(map[int][9]float64, len(frameH))
	for idx, h := range frameH {
		hc := h
		hc[2] = h[0]*offX + h[1]*offY + h[2]
		hc[5] = h[3]*offX + h[4]*offY + h[5]
		hc[8] = h[6]*offX + h[7]*offY + h[8]
		out[idx] = hc
	}
	return out
}

// mosaicReregister builds the union-canvas registered sequence: every merged frame is zero-padded
// by the union margins (uniform — no homography needed per frame), the padded sequence is
// registered afresh (2-pass; the seam flatten/refit already live in the pixels), and the review is
// recomputed on the padded plane. ok=false soft-fails to the anchor canvas with a warning.
func mosaicReregister(ctx context.Context, opts Options, ch *ChannelResult, groups []lightGroup,
	spans []groupSpan, anchorNight, mergedDir, seqBase, filter string, review registrationReview,
	w, h int, ref stepRef) (dir, seq string, review2 registrationReview, cv canvasSpec, ok bool) {
	cv = review.unionCanvasOf(w, h)
	if cv.W <= w && cv.H <= h {
		opts.sessionLine(ref, "", fmt.Sprintf("mosaic: %s footprints already fit the anchor canvas — nothing to widen", filter))
		return "", "", review, cv, false
	}
	if float64(cv.W)*float64(cv.H) > mosaicMaxAreaFactor*float64(w)*float64(h) {
		warnChannel(opts, ch, fmt.Sprintf("%s: mosaic union %dx%d exceeds %.0fx the sensor area — staying on the anchor canvas (barely-overlapping sessions?)",
			filter, cv.W, cv.H, mosaicMaxAreaFactor))
		return "", "", review, cv, false
	}
	padDir := mergedDir + "_mosaic"
	if err := fsutil.EnsureDir(padDir); err != nil {
		warnChannel(opts, ch, filter+": mosaic scratch dir: "+err.Error())
		return "", "", review, cv, false
	}
	opts.sessionLine(ref, "", fmt.Sprintf("▶ mosaic %s: union canvas %dx%d (anchor at +%d+%d) — padding + re-registering",
		filter, cv.W, cv.H, int(cv.OffX), int(cv.OffY)))
	total := 0
	if len(spans) > 0 {
		total = spans[len(spans)-1].End
	}
	for i := 0; i < total; i++ {
		if err := ctx.Err(); err != nil {
			return "", "", review, cv, false
		}
		src := mergedFramePath(mergedDir, seqBase, i)
		dst := filepath.Join(padDir, fmt.Sprintf("pad_%05d.fits", i+1))
		if err := padFITS(src, dst, int(cv.OffX), int(cv.OffY), cv.W, cv.H); err != nil {
			warnChannel(opts, ch, fmt.Sprintf("%s: mosaic padding frame %d: %v — staying on the anchor canvas", filter, i+1, err))
			return "", "", review, cv, false
		}
	}
	if _, err := opts.Runner.Run(ctx, padDir, siril.Register2PassScript("light", "homography"), nil); err != nil {
		warnChannel(opts, ch, filter+": mosaic re-registration failed — staying on the anchor canvas: "+err.Error())
		return "", "", review, cv, false
	}
	review2 = reviewMergedRegistration(filepath.Join(padDir, "light_.seq"), spans, anchorGroupIndex(groups, anchorNight), cv.W, cv.H)
	opts.sessionLine(ref, "", fmt.Sprintf("✓ mosaic %s: %d frames on the union canvas", filter, len(review2.FrameH)))
	return padDir, "light", review2, cv, true
}
