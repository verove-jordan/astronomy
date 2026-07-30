// Cost-aware staged re-entry for the supervised finish. Each iteration re-enters the pipeline at the
// cheapest stage that reflects the model's change: Tier A re-renders only the GIMP composite from the
// cached linear prep; Tier B re-runs the linear prep (stretch/SPCC/background/denoise) from the
// on-disk channel masters; Tier C re-stacks from the raw frames (via Options.Reprocess) into fresh
// masters, then falls through B and A. Intermediates are cached so a later cheaper-tier iteration
// skips the expensive stages.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// reentry holds the cached intermediates for one supervised finish: the current aligned-channel
// masters (Tier C may replace them) and the prepped GIMP inputs (Tier B rebuilds them).
type reentry struct {
	opts       Options
	channels   map[string]string
	outDir     string
	stretchDir string
	base       *gimp.Inputs // cached linear prep (Tier B output); nil → must rebuild
	notes      []string     // colour-calibration/background notes from the last prep
}

func newReentry(opts Options, channels map[string]string, workRun, outDir string) (*reentry, error) {
	stretchDir := filepath.Join(workRun, "05_stretched")
	if err := fsutil.EnsureDir(stretchDir); err != nil {
		return nil, err
	}
	opts.steps = nil // nested re-renders (supervised loop, star fix) must never advance the main bar
	return &reentry{opts: opts, channels: channels, outDir: outDir, stretchDir: stretchDir}, nil
}

// render produces one candidate finish at tier t from the working preset p, reusing cached
// intermediates for the stages cheaper than t. Tier C re-stacks and invalidates the cached prep;
// Tier B rebuilds the prep; Tier A composites from the cached prep (building it first if none exists).
func (r *reentry) render(ctx context.Context, t tier, p mode.Preset, outBase string) (*gimp.Result, error) {
	o := r.opts
	o.Preset = &p
	if t >= tierC {
		if err := r.restack(ctx, o, p); err != nil {
			return nil, err
		}
	}
	if t >= tierB || r.base == nil {
		if err := r.buildBase(ctx, o, p); err != nil {
			return nil, err
		}
	}
	return buildComposite(o.Gimp, *r.base, presetComposeParams(&p), outBase)
}

// restack re-runs the stack stages from the raw frames with the working preset and swaps in the fresh
// masters, invalidating the cached prep. Requires Options.Reprocess (nil for a pure refine with no raws).
func (r *reentry) restack(ctx context.Context, o Options, p mode.Preset) error {
	if o.Reprocess == nil {
		return fmt.Errorf("re-stack requested but no raw frames are available for this run")
	}
	channels, err := o.Reprocess(ctx, &p)
	if err != nil {
		return fmt.Errorf("re-stack: %w", err)
	}
	if len(channels) == 0 {
		return fmt.Errorf("re-stack produced no channel masters")
	}
	r.channels = channels
	r.base = nil // masters changed → the linear prep must rebuild
	return nil
}

// buildBase runs the linear finish prep (rgbcomp → background extraction → colour calibration →
// stretch) for the working preset and caches the resulting GIMP inputs.
func (r *reentry) buildBase(ctx context.Context, o Options, p mode.Preset) error {
	deg := backgroundDegree(ctx, o)
	cc := postprocess.ColorCalOptions{Enabled: p.ColorCalibration, RemoveGreen: true, StarField: true, Solve: o.Solve, Spcc: o.Spcc}
	base, notes, _, err := prepGimpInputs(ctx, o, o.Runner, r.channels, r.outDir, r.stretchDir, deg, cc, p.BackgroundLevel, p.LinkedStretch)
	if err != nil {
		return err
	}
	r.base, r.notes = &base, notes
	return nil
}

// buildComposite renders one layered composite from the cached linear prep with the given Tier-A
// composite params (all other GIMP inputs — Base/Lum/Ha/Color — are carried in base). Shared so the
// loop renders exactly what the standard finish would for the same params.
func buildComposite(c *gimp.Client, base gimp.Inputs, p composeParams, outBase string) (*gimp.Result, error) {
	in := base
	in.HaBlack = p.HaBlackPoint
	in.ChromaBlur = p.ChromaBlur
	in.CropFrac = p.CropFrac
	in.LumCurve = p.LumCurve
	in.LumOpacity = p.LumOpacity
	in.CoreHighlightKnee = p.CoreHighlightKnee
	in.CoreHighlightCeil = p.CoreHighlightCeil
	in.HighlightKnee = p.HighlightKnee // star-safe highlight cap: keep the supervised renders identical to the standard finish
	in.HighlightCeil = p.HighlightCeil
	in.StarDesat = p.StarDesat // star-core desaturation (kills colour discs on dense star fields)
	in.HaExcludeStars = p.HaExcludeStars
	in.OIIIScreen = in.OIIIOpacity(p.OIIIScreen) // teal emission screen: retuned opacity × the persisted wash-gate factor
	in.OIIIBlack = p.OIIIBlackPoint
	in.SIIScreen = in.SIIOpacity(p.SIIScreen) // [SII] emission screen, same rule
	in.SIIBlack = p.SIIBlackPoint
	in.SIITint = p.SIITint
	return gimp.BuildImage(c, in, p.Curve, in.HaOpacity(p.HaScreen), p.Saturation, outBase)
}
