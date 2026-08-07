package pipeline

// Optional monochrome side-outputs saved next to the colour final (deepsky/nebula):
//   - the processed Luminance-only image (EmitLuminanceMono, on by default), and
//   - the combined all-channel integration (EmitAllChannelMono, opt-in).
// Both reuse the co-registered channel masters and the exact stretch/finish helpers the colour
// composite uses, so a mono output matches the luminance in the colour image. Everything is soft-fail:
// a run never fails because of a side output.

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// monoScriptHeader mirrors the Siril prelude every finish script uses (modern Siril, FITS output,
// 32-bit) — see siril.scriptHeader / the local hdr in prepGimpInputs.
const monoScriptHeader = "requires 1.2.0\nsetext fits\nset32bits\n"

// emitMonoOutputs saves the optional monochrome deliverables from the co-registered channel masters.
// It is registered as a defer in finishAligned so it runs once, on every finish path, after res.Final
// is set. The produced files are APPENDED to res.Final.Outputs (after the colour final, so final.png
// stays the gallery hero / video source) and recorded in res.Final.MonoOutputs for the results UI.
func emitMonoOutputs(ctx context.Context, opts Options, channels map[string]string, res *Result, workRun, outDir string) {
	if res == nil || res.Final == nil || opts.Preset == nil || ctx.Err() != nil {
		return
	}
	if !opts.Preset.EmitLuminanceMono && !opts.Preset.EmitAllChannelMono {
		return
	}
	stretchDir := filepath.Join(workRun, "05_stretched")
	if err := fsutil.EnsureDir(stretchDir); err != nil {
		warnLive(opts, res, "mono outputs skipped: "+err.Error())
		return
	}
	deg := backgroundDegree(ctx, opts)
	bgLevel := opts.Preset.BackgroundLevel
	headroom := opts.Preset.StretchHeadroom

	if opts.Preset.EmitLuminanceMono {
		if _, ok := channels["L"]; ok {
			src := filepath.Join(outDir, channels["L"]+".fits")
			if mo, err := renderMono(ctx, opts, src, "lum_mono", stretchDir, outDir, "final_luminance", deg, bgLevel, headroom); err != nil {
				warnLive(opts, res, "luminance mono output skipped: "+err.Error())
			} else {
				appendMono(res, mo, "luminance", "saved luminance-only mono (final_luminance)")
			}
		} else {
			res.Warnings = append(res.Warnings, "luminance mono output skipped: this run has no L channel")
		}
	}

	if opts.Preset.EmitAllChannelMono {
		if mo, err := renderAllChannelMono(ctx, opts, channels, workRun, stretchDir, outDir, "final_monostack", deg, bgLevel, headroom); err != nil {
			warnLive(opts, res, "all-channel mono output skipped: "+err.Error())
		} else if mo != nil {
			appendMono(res, mo, "all_channels", "saved combined all-channel mono (final_monostack)")
		}
	}
}

// renderMono stretches one linear master (src, absolute .fits) into a display mono the same way the
// colour composite builds its luminance layer — highlight headroom → subsky → dark autostretch (see
// prepGimpInputs' lum block) — then flattens it to a mono PNG+TIF at outDir/<outBase>.{png,tif}.
func renderMono(ctx context.Context, opts Options, src, tag, stretchDir, outDir, outBase string,
	deg int, bgLevel, headroom float64) (*postprocess.MonoOutput, error) {
	var notes []string // headroom soft-notes; the stretch still runs on a skip
	lin := headroomSource(src, tag, stretchDir, headroom, &notes)
	stretched := filepath.Join(stretchDir, tag)
	lumPx := 0
	if opts.Preset != nil {
		lumPx = opts.Preset.SkyLumFlattenPx
	}
	// Same post-stretch sky-level flatten detour as the composite's luminance layer, so the mono
	// deliverables get the same uniform sky (knob 0 → the single legacy script, byte-identical).
	if lumPx > 0 {
		script := monoScriptHeader + fmt.Sprintf("load %s\n%s%s\nsave %s\n",
			lin, siril.SubskyCmd(deg), siril.AutostretchCmd(false, bgLevel), stretched)
		if _, err := opts.Runner.Run(ctx, outDir, script, opts.sirilLines("mono finish ("+tag+")")); err != nil {
			return nil, err
		}
		if _, err := flattenSkyLuminance(stretched+".fits", lumPx); err != nil {
			opts.report(Progress{Line: "⚠ mono sky luminance flatten skipped: " + err.Error()})
		}
		save := monoScriptHeader + fmt.Sprintf("load %s\nsavetif %s\n", stretched, stretched)
		if _, err := opts.Runner.Run(ctx, outDir, save, opts.sirilLines("mono finish ("+tag+")")); err != nil {
			return nil, err
		}
	} else {
		script := monoScriptHeader + fmt.Sprintf("load %s\n%s%s\nsavetif %s\n",
			lin, siril.SubskyCmd(deg), siril.AutostretchCmd(false, bgLevel), stretched)
		if _, err := opts.Runner.Run(ctx, outDir, script, opts.sirilLines("mono finish ("+tag+")")); err != nil {
			return nil, err
		}
	}
	return exportMono(ctx, opts, stretched+".tif", outDir, outBase)
}

// renderAllChannelMono integrates every co-registered channel master into one synthetic-luminance
// master (sub-count-weighted, additive-normalized), gently denoises it (the integration pools the
// un-denoised R/G/B chroma noise), then renders it as a mono deliverable. Returns nil (no error) when
// there are too few channels to combine — a single channel is just the luminance mono.
func renderAllChannelMono(ctx context.Context, opts Options, channels map[string]string, workRun, stretchDir, outDir, outBase string,
	deg int, bgLevel, headroom float64) (*postprocess.MonoOutput, error) {
	ordered := orderedFilters(channels)
	if len(ordered) < 2 {
		return nil, nil
	}
	masters := make(map[string]string, len(ordered))
	for _, f := range ordered {
		masters[f] = filepath.Join(outDir, channels[f]+".fits")
	}
	seqDir := filepath.Join(workRun, "06_synthlum")
	if err := symlinkOrdered(seqDir, ordered, masters); err != nil {
		return nil, err
	}
	synth := filepath.Join(outDir, "synthlum")
	if _, err := opts.Runner.Run(ctx, seqDir, siril.IntegrateChannelsScript("synth", synth, stackalg.WeightNbStack),
		opts.sirilLines("integrating all channels")); err != nil {
		return nil, err
	}
	if dn := denoiseFor("L", opts.Preset); dn.Modulation > 0 {
		// Best-effort: a denoise skip leaves the integration as-is (the mean already cut the noise).
		_ = applyDenoise(ctx, opts, "synthlum", outDir, dn, dn.Modulation, opts.sirilLines("denoise all-channel mono"))
	}
	return renderMono(ctx, opts, synth+".fits", "synthlum", stretchDir, outDir, outBase, deg, bgLevel, headroom)
}

// exportMono flattens a stretched mono TIFF into the final PNG+TIF deliverable at outDir/<outBase>.*.
// Prefers a GIMP mono render (value curve + edge crop, matching the colour finish); falls back to a
// straight Siril re-save when GIMP is unavailable (the Siril-only finish path).
func exportMono(ctx context.Context, opts Options, monoTif, outDir, outBase string) (*postprocess.MonoOutput, error) {
	base := filepath.Join(outDir, outBase)
	if opts.Gimp != nil && opts.Gimp.Available() == nil {
		in := gimp.Inputs{Base: monoTif, Color: false, CropFrac: opts.Preset.CropFrac}
		g, err := gimp.BuildImage(opts.Gimp, in, opts.Preset.Curve, 0, 0, base)
		if err != nil {
			return nil, err
		}
		return &postprocess.MonoOutput{Png: g.Png, Tif: g.Tif}, nil
	}
	script := monoScriptHeader + fmt.Sprintf("load %s\nsavetif %s\nsavepng %s\n", monoTif, base, base)
	if _, err := opts.Runner.Run(ctx, outDir, script, nil); err != nil {
		return nil, err
	}
	return &postprocess.MonoOutput{Png: base + ".png", Tif: base + ".tif"}, nil
}

// carryMonoOutputs copies the previous final's mono side-outputs onto a fresh Tier-A rerun result:
// the composite tweak didn't touch the mono renders, but the new Final record starts empty and would
// silently drop them from run.json / the results UI. Entries whose PNG vanished from disk are dropped.
func carryMonoOutputs(prev, next *postprocess.Result) {
	if prev == nil || next == nil || len(next.MonoOutputs) > 0 {
		return
	}
	for _, mo := range prev.MonoOutputs {
		if mo.Png != "" && !fileExists(mo.Png) {
			continue
		}
		next.MonoOutputs = append(next.MonoOutputs, mo)
		if mo.Png != "" {
			next.Outputs = append(next.Outputs, mo.Png)
		}
		if mo.Tif != "" {
			next.Outputs = append(next.Outputs, mo.Tif)
		}
	}
}

// appendMono records a produced mono deliverable on the result: the typed entry (for the UI) plus its
// files in Outputs (for download / S3 mirror), always AFTER the colour final so final.png stays first.
func appendMono(res *Result, mo *postprocess.MonoOutput, kind, note string) {
	if mo == nil {
		return
	}
	mo.Kind = kind
	res.Final.MonoOutputs = append(res.Final.MonoOutputs, *mo)
	if mo.Png != "" {
		res.Final.Outputs = append(res.Final.Outputs, mo.Png)
	}
	if mo.Tif != "" {
		res.Final.Outputs = append(res.Final.Outputs, mo.Tif)
	}
	res.Final.Notes = append(res.Final.Notes, note)
}
