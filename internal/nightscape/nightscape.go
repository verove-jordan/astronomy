package nightscape

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/rawconv"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Look bundles the per-render-style tunables, ported verbatim from the reference recipe's presets
// (main.py lines ~1500–1569). natural ≈ a straight developed DNG; iphone ≈ an edited ProRAW (deep
// near-neutral sky, golden core); deepsky ≈ punchy/dramatic.
type Look struct {
	Name                                  string
	AsinhIntensity, AsinhIntensityFG      float64
	Saturation, GreenRemoval              float64
	MaskPercentile                        float64
	MaskDilation                          int
	MaskBlur                              float64
	SkyPercentile, NormPercentile         float64
	ToneStrength                          float64
	ShadowTint, HighlightTint             [3]float64
	NeutralizePercentile, BlackPercentile float64
	PerChannelBlack                       bool
	HighlightKnee                         float64 // luminance shoulder (0 disables): keeps the core from clipping to white
	HighlightCeiling                      float64 // shoulder asymptote (0 → default highlightCeiling); lower = dimmer core
	TargetBg                              float64 // auto-stretch target sky-background level (0 → 0.09 balanced)
}

// All three looks share an artifact-free grade (no per-channel black point, no polynomial gradient
// removal — both caused colour blotches/casts on phone nightscapes) and a highlight shoulder that
// keeps the Milky-Way core from clipping to flat white. They differ only in how hard they stretch and
// saturate: natural ≈ a faithful developed phone frame; iphone a touch warmer/punchier; deepsky bold.
var looks = map[string]Look{
	"natural": {
		Name: "natural", AsinhIntensityFG: 6, Saturation: 1.10, GreenRemoval: 0.25,
		MaskPercentile: 45, MaskDilation: 15, MaskBlur: 12, SkyPercentile: 55, NormPercentile: 99.9,
		ToneStrength: 0, NeutralizePercentile: 2.0, HighlightKnee: 0.30, HighlightCeiling: 0.38, TargetBg: 0.05,
	},
	"iphone": {
		Name: "iphone", AsinhIntensityFG: 7, Saturation: 1.12, GreenRemoval: 0.3,
		MaskPercentile: 45, MaskDilation: 15, MaskBlur: 12, SkyPercentile: 55, NormPercentile: 99.9,
		ToneStrength: 0.35, ShadowTint: [3]float64{0, 0, 0.02}, HighlightTint: [3]float64{0.04, 0.01, -0.03},
		NeutralizePercentile: 3.0, HighlightKnee: 0.35, HighlightCeiling: 0.70, TargetBg: 0.07,
	},
	"deepsky": {
		Name: "deepsky", AsinhIntensityFG: 12, Saturation: 1.3, GreenRemoval: 0.35,
		MaskPercentile: 45, MaskDilation: 15, MaskBlur: 10, SkyPercentile: 55, NormPercentile: 99.85,
		ToneStrength: 0.6, ShadowTint: [3]float64{-0.010, -0.020, 0.045}, HighlightTint: [3]float64{0.050, 0.010, -0.040},
		NeutralizePercentile: 4.0, HighlightKnee: 0.40, HighlightCeiling: 0.78, TargetBg: 0.09,
	},
}

// LookByName returns the named look (case-insensitive), falling back to natural for unknown/empty.
func LookByName(name string) Look {
	if l, ok := looks[strings.ToLower(strings.TrimSpace(name))]; ok {
		return l
	}
	return looks["natural"]
}

// LookNames lists the available looks (for CLI/UI validation).
func LookNames() []string { return []string{"natural", "iphone", "deepsky"} }

// Options configures one nightscape run.
type Options struct {
	// Siril drives registration (Process) and the optional sky colour calibration (enhanceSky). It is
	// required by Process; ComposeAlignedDir (the grade-only fast harness) may leave it nil, in which
	// case the sky background is neutralized in Go instead of via Siril.
	Siril *siril.Runner
	// Graxpert is the optional AI background-gradient + chroma-denoise tool for the sky stack. nil (or a
	// missing binary) soft-falls to no gradient removal/denoise — the auto-levels still balance the sky.
	Graxpert *graxpert.Runner

	Frames  []string // source raw frames, acquisition order
	WorkDir string   // Siril working directory (also where enhanceSky round-trips the sky FITS)
	OutDir  string   // where final artifacts are written
	Look    Look
	// Brightness overrides the auto-levels target sky-background (the "Darker/Balanced/Brighter"
	// control); 0 → the Look's own TargetBg. See autoStretch.
	Brightness float64
	// SaturationScale scales the Look's own saturation (1 or 0 = as designed; <1 tames neon colour,
	// up to 2 boosts) and HighlightCeilOverride replaces the Look's core highlight ceiling
	// (0 = keep; lower = dimmer, flatter Milky-Way core). Both are supervisor/params knobs.
	SaturationScale       float64
	HighlightCeilOverride float64

	// ColorCalibration enables plate-solve + SPCC on the sky stack for natural star colour; it engages
	// only when an OSC sensor is also configured (Spcc.OSCSensor) — a phone sensor is rarely in Siril's
	// SPCC database, so the default is the neutralization path. Any failure falls back to background
	// neutralization + green removal (a homogeneous neutral sky either way). Focal35mm is the EXIF
	// 35 mm-equivalent focal length (mm) used to derive the plate scale for solving; 0 → header/blind.
	ColorCalibration bool
	Solve            siril.SolveOptions
	Spcc             siril.SpccOptions
	Focal35mm        float64

	// DarkDir/FlatDir/BiasDir are optional calibration-frame folders (#4); empty keeps the proven
	// uncalibrated single-pass path. Offset == bias.
	DarkDir, FlatDir, BiasDir string
	// DarkFrames/FlatFrames/BiasFrames are calibration raws auto-detected among the input stills
	// (classified by inspect) and unioned with the *Dir folders. Frames captured this run are also
	// persisted to the reusable library (PhoneCalib) keyed by ISO/exposure/dimensions.
	DarkFrames, FlatFrames, BiasFrames []string
	// PhoneCalib is the persistent phone-calibration-master library: matched masters are reused when no
	// cal frames are supplied this run, and freshly-built masters are saved to it. nil → no persistence
	// (build-from-frames only). LibraryDir is where the master FITS are written.
	PhoneCalib calib.PhoneCalibStore
	LibraryDir string

	ForegroundFrame string // optional raw frame used as the clean foreground (and registration ref)
	Orientation     string // auto|none|cw|ccw|180 (+ -flip)
	RefIndex        int    // 1-based reference frame (0 = middle); ignored if ForegroundFrame set
	OnProgress      func(siril.Progress)
	// PreviewOnly makes gradeCompose write only final.png (skip the preview + linear FITS intermediates).
	// The supervised finish sets it for its per-iteration re-grades, which only need the PNG to score.
	PreviewOnly bool
}

// Result reports what Process produced.
type Result struct {
	FinalPNG       string
	PreviewPNG     string
	CompositeFITS  string
	SkyFITS        string
	ForegroundFITS string
	Width, Height  int
	InputFrames    int
	StackedFrames  int
	Warnings       []string
}

// note appends a non-empty warning (a helper for the many soft-fail enhancement steps).
func (r *Result) note(s string) {
	if s != "" {
		r.Warnings = append(r.Warnings, s)
	}
}

// Process runs the full nightscape recipe: orientation-correct develop → star-register on a forced
// reference (so sky and foreground share coordinates) → clean sky-only stack → linear colour grade →
// masked composite → export.
func Process(ctx context.Context, o Options) (*Result, error) {
	if len(o.Frames) < 2 {
		return nil, fmt.Errorf("nightscape needs at least 2 frames, got %d", len(o.Frames))
	}
	if o.Siril == nil {
		return nil, fmt.Errorf("nightscape needs a Siril runner")
	}
	if err := fsutil.EnsureDir(o.OutDir); err != nil {
		return nil, err
	}
	seqDir := filepath.Join(o.WorkDir, "01_seq")
	if err := fsutil.EnsureDir(seqDir); err != nil {
		return nil, err
	}

	// The reference frame is also the clean foreground. An override is processed as the reference
	// (first frame); otherwise the middle frame is used.
	convertFrames := o.Frames
	refIndex := o.RefIndex
	if o.ForegroundFrame != "" {
		convertFrames = append([]string{o.ForegroundFrame}, o.Frames...)
		refIndex = 1
	} else if refIndex <= 0 || refIndex > len(o.Frames) {
		refIndex = len(o.Frames)/2 + 1
	}
	res := &Result{InputFrames: len(o.Frames)}

	if _, warn, err := rawconv.PrepareTIFF(ctx, convertFrames, seqDir, nil); err != nil {
		return nil, fmt.Errorf("develop frames: %w", err)
	} else {
		res.Warnings = append(res.Warnings, warn...)
	}

	const hdr = "requires 1.2.0\nsetext fits\n"
	plan := planCalibration(ctx, o)
	if plan.active {
		// Opt-in calibration: convert lights to FITS, dark/flat/bias-correct them in Go (linear light),
		// then register the calibrated frames. Masters come from this run's cal frames (also saved to the
		// library) or a reused library master. Soft-fail — a bad cal set just warns and proceeds.
		if _, err := o.Siril.Run(ctx, seqDir, hdr+"convert light -out=.\n", o.OnProgress); err != nil {
			return nil, fmt.Errorf("convert: %w", err)
		}
		lights, _ := filepath.Glob(filepath.Join(seqDir, "light_*.fit*"))
		sort.Strings(lights)
		res.note(calibrateLights(ctx, o, plan, seqDir, lights))
		if _, err := o.Siril.Run(ctx, seqDir, hdr+fmt.Sprintf("setref light %d\nregister light -2pass\nseqapplyreg light\n", refIndex), o.OnProgress); err != nil {
			return nil, fmt.Errorf("register: %w", err)
		}
	} else {
		// Proven single-pass path (byte-identical to v1): convert + register in one Siril invocation.
		script := hdr + fmt.Sprintf("convert light -out=.\nsetref light %d\nregister light -2pass\nseqapplyreg light\n", refIndex)
		if _, err := o.Siril.Run(ctx, seqDir, script, o.OnProgress); err != nil {
			return nil, fmt.Errorf("register: %w", err)
		}
	}

	// Siril names frames r_light_00001.fits / light_00001.fits (extension follows `setext`).
	aligned, err := filepath.Glob(filepath.Join(seqDir, "r_light_*.fit*"))
	if err != nil || len(aligned) == 0 {
		return nil, fmt.Errorf("no aligned frames produced (register failed)")
	}
	sort.Strings(aligned)
	unaligned, _ := filepath.Glob(filepath.Join(seqDir, "light_*.fit*"))
	sort.Strings(unaligned)
	if refIndex-1 >= len(unaligned) {
		return nil, fmt.Errorf("reference frame %d out of range (%d converted)", refIndex, len(unaligned))
	}
	res.StackedFrames = len(aligned)
	return compose(ctx, o, aligned, unaligned[refIndex-1], res)
}

// ComposeAlignedDir runs only the post-registration compose stage over an already-registered Siril
// work dir (its 01_seq folder), picking the middle frame as the foreground. It exists so the colour
// grade can be re-tuned in seconds without re-developing and re-registering the frames; o supplies the
// Look/Orientation/OutDir and any optional runners (Graxpert/Siril) to exercise the enhancement chain.
func ComposeAlignedDir(ctx context.Context, o Options, seqDir string) (*Result, error) {
	aligned, _ := filepath.Glob(filepath.Join(seqDir, "r_light_*.fit*"))
	sort.Strings(aligned)
	unaligned, _ := filepath.Glob(filepath.Join(seqDir, "light_*.fit*"))
	sort.Strings(unaligned)
	if len(aligned) == 0 || len(unaligned) == 0 {
		return nil, fmt.Errorf("no registered frames in %s", seqDir)
	}
	if err := fsutil.EnsureDir(o.OutDir); err != nil {
		return nil, err
	}
	if o.WorkDir == "" {
		o.WorkDir = seqDir // enhanceSky round-trips the sky FITS here
	}
	ref := len(unaligned)/2 + 1
	res := &Result{InputFrames: len(unaligned), StackedFrames: len(aligned)}
	return compose(ctx, o, aligned, unaligned[ref-1], res)
}

// compose runs the per-pixel recipe over the registered frames: clean foreground, sky mask, clean
// sky stack, sky enhancement (GraXpert gradient/denoise + colour calibration), linear colour grade,
// masked composite, export.
func compose(ctx context.Context, o Options, aligned []string, fgPath string, res *Result) (*Result, error) {
	look := o.Look
	if look.Name == "" {
		look = LookByName("")
	}
	outDir := o.OutDir

	// Foreground: the unaligned reference frame, linearised + hot-pixel cleaned.
	fg, err := fits.ReadImage(fgPath)
	if err != nil {
		return nil, fmt.Errorf("read foreground frame: %w", err)
	}
	linearizeSRGB(fg)
	cleanHotPixels(fg, 5.0)

	// Sky mask from the clean linear foreground, captured before gradient removal touches its levels.
	alpha := buildSkyAlpha(fg, look.MaskPercentile, look.MaskDilation, look.MaskBlur)

	// Clean sky stack (per-pixel sky-only mean), linearising each aligned frame as it streams in.
	sky, err := computeCleanSkyStack(aligned, fg, look.SkyPercentile, true, 1.3, 0.5, linearizeSRGB)
	if err != nil {
		return nil, err
	}
	if os.Getenv("NS_DEBUG") != "" {
		_ = exportPNG(percentileStretch(sky.Clone(), 0.5, 99.5), filepath.Join(outDir, "dbg_sky_raw.png"))
		_ = exportPNG(percentileStretch(fg.Clone(), 0.5, 99.5), filepath.Join(outDir, "dbg_fg_raw.png"))
	}

	// Neutralise the global colour cast first (a gentle per-channel OFFSET to a common black — not a
	// per-channel clip, which speckles noisy darks), then flatten the sky's large-scale SPATIAL gradient
	// (warm horizon glow / light pollution) using a background modelled from sky pixels ONLY: the
	// foreground/drift is masked out, so the dark trees can't bias the horizon level (the v2 cause of the
	// red/violet tree-edge cast). Mask-aware Go model, not GraXpert (which sampled the whole frame). Both
	// run on the linear sky before the stretch.
	neutralizeBackground(sky, look.NeutralizePercentile)
	gradStep := max(sky.W, sky.H) / 16
	if gradStep < 8 {
		gradStep = 8
	}
	removeSkyGradient(sky, alpha, gradStep, gradStrength)

	// Sky enhancement (linear): GraXpert chroma denoise (its real strength) + plate-solve+SPCC when an
	// OSC sensor is configured. Soft-fail; gradient flattening + per-channel black are handled above and
	// in autoStretch, so this no longer runs the foreground-biased background subtraction.
	enhanceSky(ctx, sky, o, res)

	// Pull green to neutral on both layers (mild SCNR, linear), then denoise the sky's COLOUR speckle
	// (chroma blur — keeps stars/luminance sharp) so the saturation boost later can't re-amplify it. The
	// sigma-clipped stack handles the luminance grain; this cleans the residual chroma noise.
	removeGreenCast(sky, look.GreenRemoval)
	chromaBlur(sky, skyChromaBlur)
	neutralizeBackground(fg, look.NeutralizePercentile)
	removeGreenCast(fg, look.GreenRemoval)

	// Persist the pre-grade linear inputs (+ the resolved orientation) so the supervised finish and a
	// later post-run refine can re-grade in seconds without re-developing/re-registering. See Regrade.
	// The developed reference frame's dims feed the baked-rotation detection (orientDecision).
	orientMode := resolveOrientation(o, fg.W, fg.H)
	persistGradeInputs(outDir, sky, fg, alpha, orientMode, res)
	return gradeCompose(o, sky, fg, alpha, orientMode, res)
}

// gradeCompose runs the tunable colour grade over the cleaned linear sky/foreground + sky mask: auto-
// stretch, asinh foreground, masked composite, saturation / split-tone / highlight shoulder, then orient
// and export. Shared by compose (a full run) and Regrade (a re-tune from persisted linear inputs), so the
// supervised finish renders exactly what a full run would for the same Look/Brightness.
func gradeCompose(o Options, sky, fg *fits.Image, alpha []float32, orientMode string, res *Result) (*Result, error) {
	look := o.Look
	if look.Name == "" {
		look = LookByName("")
	}
	outDir := o.OutDir

	targetBg := o.Brightness
	ceilScale := 1.0
	if targetBg <= 0 {
		targetBg = look.TargetBg
	} else if look.TargetBg > 0 {
		// Give the Darker/Balanced/Brighter control authority over the bright core, not just the faint
		// background: scale the highlight ceiling with the chosen background target so "darker" also dims
		// the Milky-Way core and "brighter" lifts it (clamped so the shoulder stays well-defined).
		ceilScale = targetBg / look.TargetBg
		if ceilScale < 0.7 {
			ceilScale = 0.7
		} else if ceilScale > 1.35 {
			ceilScale = 1.35
		}
	}
	autoStretch(sky, targetBg, alpha)
	asinhStretch(fg, look.AsinhIntensityFG, look.NormPercentile, 0, false)

	composite, err := compositeLayers(sky, fg, alpha)
	if err != nil {
		return nil, err
	}
	satScale := o.SaturationScale
	if satScale <= 0 {
		satScale = 1
	}
	boostSaturation(composite, look.Saturation*satScale)
	splitTone(composite, look.ShadowTint, look.HighlightTint, look.ToneStrength, 0.85)
	ceil := look.HighlightCeiling
	if o.HighlightCeilOverride > 0 {
		ceil = o.HighlightCeilOverride
	}
	compressHighlights(composite, look.HighlightKnee, ceil*ceilScale)

	// Restore the intended display orientation (resolved by the caller: user override / EXIF / heuristic).
	composite = orient(composite, orientMode)
	res.Width, res.Height = composite.W, composite.H

	// Export: display PNG (primary) + a downscaled preview + linear intermediates for inspection.
	res.FinalPNG = filepath.Join(outDir, "final.png")
	if err := exportPNG(composite, res.FinalPNG); err != nil {
		return nil, fmt.Errorf("export png: %w", err)
	}
	if o.PreviewOnly { // supervised per-iteration render: only the PNG is scored — skip the heavy FITS
		return res, nil
	}
	res.PreviewPNG = filepath.Join(outDir, "final_preview.png")
	if err := exportPNG(downsample(composite, 1400), res.PreviewPNG); err != nil {
		res.Warnings = append(res.Warnings, "preview: "+err.Error())
	}
	res.CompositeFITS = filepath.Join(outDir, "composite.fits")
	_ = composite.WriteFITS(res.CompositeFITS)
	res.SkyFITS = filepath.Join(outDir, "stacked_sky.fits")
	_ = orient(sky, orientMode).WriteFITS(res.SkyFITS)
	res.ForegroundFITS = filepath.Join(outDir, "foreground_reference.fits")
	_ = orient(fg, orientMode).WriteFITS(res.ForegroundFITS)
	return res, nil
}

// persistGradeInputs writes the pre-grade linear sky/foreground, the sky mask, and the resolved
// orientation to outDir, so the supervised finish and a post-run refine can re-grade without re-
// developing. Best-effort — a failure just warns (refine falls back to a full re-process).
func persistGradeInputs(outDir string, sky, fg *fits.Image, alpha []float32, orientMode string, res *Result) {
	if err := sky.WriteFITS(filepath.Join(outDir, "lin_sky.fits")); err != nil {
		res.Warnings = append(res.Warnings, "persist lin_sky: "+err.Error())
		return
	}
	if err := fg.WriteFITS(filepath.Join(outDir, "lin_fg.fits")); err != nil {
		res.Warnings = append(res.Warnings, "persist lin_fg: "+err.Error())
		return
	}
	mask := fits.NewImage(sky.W, sky.H, 1)
	copy(mask.Pix[0], alpha)
	if err := mask.WriteFITS(filepath.Join(outDir, "sky_alpha.fits")); err != nil {
		res.Warnings = append(res.Warnings, "persist sky_alpha: "+err.Error())
		return
	}
	_ = os.WriteFile(filepath.Join(outDir, "grade.orient"), []byte(orientMode), 0o644)
}

// Regrade re-runs only the colour grade over the persisted pre-grade linear inputs (lin_sky/lin_fg/
// sky_alpha + grade.orient) in srcDir, with the Look/Brightness in o, writing the result to o.OutDir. It
// backs both the in-run supervised finish and a post-run refine of a milkyway run — re-tuning the grade
// in seconds without re-developing or re-registering. Errors if the linear inputs are missing.
func Regrade(ctx context.Context, o Options, srcDir string) (*Result, error) {
	sky, err := fits.ReadImage(filepath.Join(srcDir, "lin_sky.fits"))
	if err != nil {
		return nil, fmt.Errorf("regrade: read lin_sky: %w", err)
	}
	fg, err := fits.ReadImage(filepath.Join(srcDir, "lin_fg.fits"))
	if err != nil {
		return nil, fmt.Errorf("regrade: read lin_fg: %w", err)
	}
	mask, err := fits.ReadImage(filepath.Join(srcDir, "sky_alpha.fits"))
	if err != nil {
		return nil, fmt.Errorf("regrade: read sky_alpha: %w", err)
	}
	if err := fsutil.EnsureDir(o.OutDir); err != nil {
		return nil, err
	}
	orientMode := o.Orientation
	if b, rerr := os.ReadFile(filepath.Join(srcDir, "grade.orient")); rerr == nil && len(b) > 0 {
		orientMode = strings.TrimSpace(string(b))
	}
	_ = ctx // grade is pure Go (no I/O to cancel); ctx kept for a uniform signature
	return gradeCompose(o, sky, fg, mask.Pix[0], orientMode, &Result{})
}

// percentileStretch maps [pLo,pHi] (over all channels) to [0,1] — a quick linear look for debugging.
func percentileStretch(im *fits.Image, pLo, pHi float64) *fits.Image {
	all := allPixels(im)
	lo := percentile(all, pLo)
	hi := percentile(all, pHi)
	if hi <= lo {
		hi = lo + 1e-6
	}
	scale := float32(1.0 / (hi - lo))
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		for i := range p {
			v := (p[i] - float32(lo)) * scale
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			p[i] = v
		}
	}
	return im
}

// exportPNG writes an 8-bit sRGB PNG from a linear-light image (sky on top).
func exportPNG(im *fits.Image, path string) error {
	rgba := image.NewNRGBA(image.Rect(0, 0, im.W, im.H))
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			i := y*im.W + x
			r := im.Pix[0][i]
			g, b := r, r
			if im.C == 3 {
				g, b = im.Pix[1][i], im.Pix[2][i]
			}
			o := rgba.PixOffset(x, y)
			rgba.Pix[o+0] = to8Dithered(r, i*3)
			rgba.Pix[o+1] = to8Dithered(g, i*3+1)
			rgba.Pix[o+2] = to8Dithered(b, i*3+2)
			rgba.Pix[o+3] = 255
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, rgba)
}

func to8(v float32) uint8 {
	s := encodeSRGB(float64(v)) * 255
	if s < 0 {
		s = 0
	} else if s > 255 {
		s = 255
	}
	return uint8(s + 0.5)
}

// to8Dithered quantizes with a deterministic ±0.5 LSB triangular dither: the smooth stretched sky
// spans only a handful of 8-bit levels, and plain rounding turns that into visible banding (worst
// case the posterized look). Triangular noise spreads the quantization error over two levels — it
// reads as imperceptible fine grain instead of contours. Keyed by pixel index, so exports stay
// byte-reproducible.
func to8Dithered(v float32, key int) uint8 {
	s := encodeSRGB(float64(v))*255 + ditherTri(key)
	if s < 0 {
		s = 0
	} else if s > 255 {
		s = 255
	}
	return uint8(s + 0.5)
}

// ditherTri returns a deterministic triangular-distribution dither in (-1,1) LSB units for key —
// the sum of two independent uniform hashes.
func ditherTri(key int) float64 {
	h1 := uint32(key)*2654435761 + 0x9e3779b9
	h1 ^= h1 >> 16
	h2 := (uint32(key) + 0x85ebca6b) * 2246822519
	h2 ^= h2 >> 13
	u1 := float64(h1&0xffff) / 65536
	u2 := float64(h2&0xffff) / 65536
	return u1 + u2 - 1
}

// downsample nearest-neighbour shrinks an image so its larger axis is at most maxDim (for previews).
func downsample(in *fits.Image, maxDim int) *fits.Image {
	larger := in.W
	if in.H > larger {
		larger = in.H
	}
	if larger <= maxDim {
		return in
	}
	factor := (larger + maxDim - 1) / maxDim
	ow, oh := in.W/factor, in.H/factor
	out := fits.NewImage(ow, oh, in.C)
	for c := 0; c < in.C; c++ {
		for y := 0; y < oh; y++ {
			for x := 0; x < ow; x++ {
				out.Pix[c][y*ow+x] = in.Pix[c][(y*factor)*in.W+(x*factor)]
			}
		}
	}
	return out
}
