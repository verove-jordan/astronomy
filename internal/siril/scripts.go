package siril

import (
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// scriptHeader requires a modern Siril, fixes the output extension so produced masters and stacks
// are predictably named `<name>.fits`, and pins 32-bit float processing regardless of host
// preferences: dark subtraction must keep negative pixels (16-bit clips them at zero, biasing the
// background statistics rejection relies on), and the Go readers (fits.ReadPlaneBand) require
// Siril's outputs to be BITPIX -32.
const scriptHeader = "requires 1.2.0\nsetext fits\nset32bits\n"

// Rejection returns the stack rejection clause best suited to the number of frames being stacked:
// percentile clipping for tiny stacks, Winsorized sigma clipping for the common mid range, and the
// Generalized Extreme Studentized Deviate test for large stacks — where it removes the correlated
// outliers (walking noise from drifting fixed-pattern residuals, trail remnants) that a 3σ
// winsorized clip leaves behind. n <= 0 (count unknown) keeps the long-proven winsorized default.
//
// The decision itself lives in stackalg.AutoReject (shared with the native combiner and the UI's
// "recommended for N frames" badge); this renders it as the Siril clause.
func Rejection(n int) string {
	return rejectionClause(mustCombine(stackalg.CombineMean), stackalg.Resolve(stackalg.Options{}, n))
}

// mustCombine looks up a combination method the package itself names — a miss is a programming
// error, not a runtime condition.
func mustCombine(c stackalg.Combine) stackalg.CombineInfo {
	info, _ := stackalg.CombineOf(c)
	return info
}

// CalibMasters holds absolute paths to the master calibration frames to apply (any may be empty).
// CFA marks a one-shot-color (Bayer) light sequence: cosmetic correction and flat division run in
// CFA-aware mode, the flat's per-channel transmission is equalized, and the frames are debayered
// after calibration (so the convert step must NOT debayer).
type CalibMasters struct {
	Bias string
	Dark string
	Flat string
	CFA  bool
	// DarkOptimize enables Siril dark optimization (calibrate -opt): Dark is a same-camera master of a
	// DIFFERENT exposure whose thermal signal is scaled onto the lights. Requires both Dark and Bias —
	// calibrateArgs ignores it otherwise.
	DarkOptimize bool
	// BadPixelMap is the absolute path of a Siril defect list ("P x y H|C", find_hot format) measured
	// from the matched dark pool (internal/calib defect scan). When set, cosmetic correction uses this
	// map (-cc=bpm) instead of -cc=dark: it also repairs the warm and unstable/flickering (RTS) pixels
	// a master-dark sigma clip cannot see — the pixels that smear into walking noise on undithered
	// drifting sequences.
	BadPixelMap string
}

// StackMasterScript converts the frames already in the work dir into sequence `seq` and stacks
// them into outName (extension added by Siril). o carries the combination/rejection recipe —
// stackalg.DefaultMasters().Bias/.Dark reproduces the historical un-normalized, count-adaptive
// stack. `convert` — not `link` — so a calibration set captured as 16-bit TIFF (SharpCap lunar
// darks/flats) stacks exactly like FITS; for FITS inputs convert symlinks, which is what link did.
func StackMasterScript(seq, outName string, frames int, o stackalg.Options) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "convert %s -out=.\n", seq)
	fmt.Fprintf(&b, "stack %s %s -out=%s\n", seq, StackClause(o, frames), outName)
	return b.String()
}

// StackFlatScript builds a master flat: optionally bias-calibrate the flats, then stack them.
// o defaults to stackalg.DefaultMasters().Flat — multiplicative normalization (the correct
// normalization for flat fields, where only the relative shape matters) with count-adaptive
// rejection. Uses `convert` for the same TIFF-tolerance as StackMasterScript.
func StackFlatScript(seq, outName, biasPath string, frames int, o stackalg.Options) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "convert %s -out=.\n", seq)
	target := seq
	if biasPath != "" {
		fmt.Fprintf(&b, "calibrate %s -bias=%s -prefix=pp_\n", seq, biasPath)
		target = "pp_" + seq
	}
	fmt.Fprintf(&b, "stack %s %s -out=%s\n", target, StackClause(o, frames), outName)
	return b.String()
}

// LightStackScript calibrates a light sequence with the matched masters, registers it
// (global star alignment), and stacks the registered frames into outName.
func LightStackScript(seq string, m CalibMasters, outName string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)

	target := seq
	if args := calibrateArgs(m); len(args) > 0 {
		fmt.Fprintf(&b, "calibrate %s %s -prefix=pp_\n", seq, strings.Join(args, " "))
		target = "pp_" + seq
	}
	fmt.Fprintf(&b, "register %s\n", target)
	fmt.Fprintf(&b, "stack r_%s rej winsorized 3 3 -norm=addscale -output_norm -out=%s\n", target, outName)
	return b.String()
}

// AlignMastersScript links the per-channel master stacks in the work dir as sequence `seq` and
// co-registers them to one reference, CROPPED to the common field of view (2-pass register +
// seqapplyreg -framing=min). Without the min framing, a channel whose pointing/footprint differs
// (e.g. an Ha set shot at an offset) leaves a zero-coverage strip in its registered image, which the
// layer stretch then amplifies into a coloured band across the composite.
func AlignMastersScript(seq string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	fmt.Fprintf(&b, "register %s -2pass\n", seq)
	fmt.Fprintf(&b, "seqapplyreg %s -framing=min\n", seq)
	return b.String()
}

// AlignPairScript links a 2-image sequence (index 1 = an already-aligned reference, index 2 = the
// image to align) and registers it with the reference PINNED to image 1 via setref — so r_<seq>_00002
// lands on the reference's pixel grid no matter which image Siril would have ranked better. Star
// detection is RELAXED (setfindstar half-sigma + relax) because this rescue exists precisely for a
// weak channel master — thin subs or a moonlit sky — whose dim stars the default detection misses
// (the cause of a real G-channel joint-registration failure). Used only after the joint cross-channel
// registration failed for this channel.
func AlignPairScript(seq string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	b.WriteString("setfindstar -sigma=0.5 -roundness=0.42 -relax=on\n")
	fmt.Fprintf(&b, "setref %s 1\n", seq)
	fmt.Fprintf(&b, "register %s\n", seq)
	return b.String()
}

// CalibrateOnlyScript calibrates a light sequence with the matched masters WITHOUT registering or
// stacking, producing pp_<seq> calibrated frames. Used for cross-session integration: each session's
// frames are calibrated with their own masters, then all calibrated frames are registered together.
func CalibrateOnlyScript(seq string, m CalibMasters) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	if args := calibrateArgs(m); len(args) > 0 {
		fmt.Fprintf(&b, "calibrate %s %s -prefix=pp_\n", seq, strings.Join(args, " "))
	}
	return b.String()
}

// CalibrateSingleScript is CalibrateOnlyScript for a one-frame group: `link` converts the lone
// frame (light_00001.fits) but writes NO .seq — Siril does not consider a single image a sequence —
// so the sequence `calibrate` aborts with "No sequence `light' found" (task #352: a night that
// contributed one frame per filter killed every channel). calibrate_single takes the converted
// image directly, accepts the same option set, and saves the same pp_-prefixed output, so the
// CalibratedSeq naming contract holds unchanged. Verified on Siril 1.4.3 (host macOS) and 1.4.4
// (container linux/arm64).
func CalibrateSingleScript(seq string, m CalibMasters) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	if args := calibrateArgs(m); len(args) > 0 {
		fmt.Fprintf(&b, "calibrate_single %s_00001 %s -prefix=pp_\n", seq, strings.Join(args, " "))
	}
	return b.String()
}

// CalibrateRegisterScript calibrates (if masters are given) and registers a light sequence
// WITHOUT stacking, so the per-frame registration metrics are written to the .seq for grading.
func CalibrateRegisterScript(seq string, m CalibMasters) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	target := seq
	if args := calibrateArgs(m); len(args) > 0 {
		fmt.Fprintf(&b, "calibrate %s %s -prefix=pp_\n", seq, strings.Join(args, " "))
		target = "pp_" + seq
	}
	fmt.Fprintf(&b, "register %s\n", target)
	return b.String()
}

// RegisterOnlyScript registers an already-linked (and already-calibrated) sequence in place, without
// re-linking or re-calibrating. It is used when calibration and registration are split around a
// Go-side step (e.g. flat-residual repair on the calibrated pp_ frames): calibrate → repair → this.
func RegisterOnlyScript(seq string) string {
	return scriptHeader + fmt.Sprintf("register %s\n", seq)
}

// Register2PassScript links an already-calibrated sequence and computes its registration in two
// passes — per-frame metrics and homographies land in <seq>_.seq, no transformed images are
// written. The caller inspects the sequence in Go (anchor reference choice, transform sanity)
// before applying it with ApplyRegistrationScript. transf defaults to homography — Siril's
// default, which already absorbs field rotation between sessions.
func Register2PassScript(seq, transf string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	reg := "register " + seq + " -2pass"
	if transf != "" {
		reg += " -transf=" + transf
	}
	fmt.Fprintf(&b, "%s\n", reg)
	return b.String()
}

// flattenPrefix names the per-frame background-flattened copies seqsubsky writes ahead of a
// multi-night merged registration.
const flattenPrefix = "flat_"

// FlattenedSeq is the sequence name seqsubsky produces for seq under the pipeline's flatten prefix.
func FlattenedSeq(seq string) string { return flattenPrefix + seq }

// FlattenRegister2PassScript is Register2PassScript with a per-frame background flatten ahead of
// the metric pass: link → seqsubsky (each frame's own degree-`degree` gradient removed, its MEAN
// level preserved — pinned by TestSirilLive_SeqSubskyPrefixAndLevel) → 2-pass register over the
// flattened sequence. Multi-night merges stack frames whose sky gradients lie at their own field
// rotations; scalar addscale matching cannot remove them, and they surface as background STEPS at
// footprint boundaries in the master (task #354's seams). One script, so a seqsubsky failure fails
// atomically and the caller retries unflattened. Registered outputs land under FlattenedSeq(seq).
func FlattenRegister2PassScript(seq, transf string, degree int) string {
	if degree < 1 {
		degree = 1
	}
	if degree > 4 {
		degree = 4
	}
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	fmt.Fprintf(&b, "seqsubsky %s %d -prefix=%s\n", seq, degree, flattenPrefix)
	reg := "register " + FlattenedSeq(seq) + " -2pass"
	if transf != "" {
		reg += " -transf=" + transf
	}
	fmt.Fprintf(&b, "%s\n", reg)
	return b.String()
}

// ApplyRegistrationScript applies a previously computed 2-pass registration (a fresh Siril process
// finds the sequence by name in the CWD — the StackSelectedScript pattern). refIndex > 0 pins the
// sequence reference first (setref, 1-based): with framing "current" the output canvas is exactly
// that frame's field, so every channel of a multi-night run lands on the SAME anchor-night canvas
// no matter how each night was framed — where the old framing=min intersected every registered
// footprint and one drifted/rotated/mis-matched frame collapsed the master to a sliver.
func ApplyRegistrationScript(seq string, refIndex int, framing string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	if refIndex > 0 {
		fmt.Fprintf(&b, "setref %s %d\n", seq, refIndex)
	}
	apply := "seqapplyreg " + seq
	if framing != "" {
		apply += " -framing=" + framing
	}
	fmt.Fprintf(&b, "%s\n", apply)
	return b.String()
}

// ApplyRegistrationSelectedScript is ApplyRegistrationScript restricted to the selected frames:
// each excluded 1-based sequence index is unselected before `seqapplyreg -filter-incl`, so an
// excluded frame can neither be registered nor — the mosaic union case — inflate a framing=max
// canvas with a false star match's absurd footprint. Outputs renumber sequentially over the
// included frames (live-pinned by TestSirilLive_FramingMaxRespectsSelection).
func ApplyRegistrationSelectedScript(seq string, refIndex int, framing string, excluded []int) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	if refIndex > 0 {
		fmt.Fprintf(&b, "setref %s %d\n", seq, refIndex)
	}
	for _, idx := range excluded {
		fmt.Fprintf(&b, "unselect %s %d %d\n", seq, idx, idx)
	}
	apply := "seqapplyreg " + seq
	if framing != "" {
		apply += " -framing=" + framing
	}
	fmt.Fprintf(&b, "%s -filter-incl\n", apply)
	return b.String()
}

// CalibrateStarAlignToRefScript calibrates a light sequence (if masters are given) and registers it with
// the reference frame pinned to refIndex (1-based). Comet mode pins the session-MIDDLE frame so the stars
// (and the star layer composited back) settle at the mid-session geometry — minimizing drift. It produces
// the registered r_<target> sequence and writes per-frame metrics to <target>_.seq for grading. Mirrors
// CalibrateRegisterScript plus the forced reference (the nightscape middle-frame pattern).
func CalibrateStarAlignToRefScript(seq string, m CalibMasters, refIndex int) string {
	if refIndex < 1 {
		refIndex = 1
	}
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	target := seq
	if args := calibrateArgs(m); len(args) > 0 {
		fmt.Fprintf(&b, "calibrate %s %s -prefix=pp_\n", seq, strings.Join(args, " "))
		target = "pp_" + seq
	}
	fmt.Fprintf(&b, "setref %s %d\n", target, refIndex)
	fmt.Fprintf(&b, "register %s -2pass\n", target)
	fmt.Fprintf(&b, "seqapplyreg %s\n", target)
	return b.String()
}

// StackAlignedScript links already-aligned frames in the work dir as sequence `seq` and stacks them with
// count-adaptive rejection + light (addscale) normalization into outName — no calibration or
// registration. Used for the comet-mode per-channel stacks (the frames are already globally star-aligned,
// or comet-translated): the rejection clips the *moving* feature (stars in the comet stack, the comet in
// the star stack), leaving the fixed one sharp. o defaults to stackalg.DefaultLights().
func StackAlignedScript(seq, outName string, frames int, o stackalg.Options) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	fmt.Fprintf(&b, "stack %s %s -out=%s\n", seq, StackClause(o, frames), outName)
	return b.String()
}

// StackCometScript stacks COMET-ALIGNED frames with ASYMMETRIC Winsorized rejection: the coma is
// consistent frame-to-frame (a tight high clip never touches it), while the star trails marching
// through are bright one-or-two-frame HIGH outliers at any given pixel — σ-high 1.8 rejects them
// where the symmetric 3/3 left residual streaks. σ-low stays loose (4) so the faint tail's noisy
// low samples are never clipped away. o defaults to stackalg.DefaultComet(), which encodes exactly
// that asymmetry; frames only matters if the user switches the rejection back to auto.
func StackCometScript(seq, outName string, frames int, o stackalg.Options) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	fmt.Fprintf(&b, "stack %s %s -out=%s\n", seq, StackClause(o, frames), outName)
	return b.String()
}

// PixelMathScript evaluates a Siril pixel-math expression and saves the result as outName. Image operands
// are referenced as $name$ — a FITS file in the work dir, without extension (e.g. "$stars$ - $starless$"
// to isolate the comet star layer, or "max($comet$, $stars$)" to screen the stars back over the starless
// comet). Mirrors the inline `pm "max(...)"` blends in internal/postprocess.
func PixelMathScript(expr, outName string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "pm %q\n", expr)
	fmt.Fprintf(&b, "save %s\n", outName)
	return b.String()
}

// CalibratedSeq is the sequence name after calibration — the input to registration and the
// stable, 1:1-with-inputs index space used for grading. Its .seq file is this name + "_.seq".
func CalibratedSeq(seq string, m CalibMasters) string {
	if len(calibrateArgs(m)) > 0 {
		return "pp_" + seq
	}
	return seq
}

// RegisteredSeq is the registered sequence produced by CalibrateRegisterScript.
func RegisteredSeq(seq string, m CalibMasters) string {
	return "r_" + CalibratedSeq(seq, m)
}

// StackSelectedScript resets the registered sequence to all-included, unselects our graded-out
// frames (1-based registered indices), then stacks only the survivors with count-adaptive rejection
// (sized to the frames actually stacked), which additionally clips residual satellite/plane trail
// pixels and — on large stacks (GESD) — the walking-noise outliers of drifting sequences.
// o carries the user's stacking recipe (stackalg.DefaultLights() = the historical clause); its
// Weight, when set, favours the sharper/deeper subs and is what the cross-session merge uses.
func StackSelectedScript(regSeq string, regCount int, rejected []int, outName string, o stackalg.Options) string {
	// Siril names a registered sequence after its frame prefix, trailing separator included
	// (frames r_pp_light_00001.fits → sequence file "r_pp_light_.seq"). Addressing it exactly
	// avoids the noisy-but-benign name-lookup recovery ("Reading sequence failed: r_pp_light.seq")
	// that used to shadow real errors in the log. Frame-file derivation (RegisteredSeq/
	// CalibratedSeq callers) keeps the bare name.
	seqName := regSeq + "_"
	var b strings.Builder
	b.WriteString(scriptHeader)
	if regCount > 0 {
		fmt.Fprintf(&b, "select %s 1 %d\n", seqName, regCount)
	}
	for _, idx := range rejected {
		fmt.Fprintf(&b, "unselect %s %d %d\n", seqName, idx, idx)
	}
	fmt.Fprintf(&b, "stack %s %s -filter-incl -out=%s\n",
		seqName, StackClause(o, regCount-len(rejected)), outName)
	return b.String()
}

// ConvertScript converts the files in the work dir into a FITS sequence named `seq`.
func ConvertScript(seq string) string {
	return scriptHeader + fmt.Sprintf("convert %s -out=.\n", seq)
}

// IntegrateChannelsScript links the already co-registered channel masters staged in the work dir as
// sequence `seq` (one symlink per channel — no registration, they share one grid) and stacks them into
// a single synthetic-luminance master at outName (absolute, extension-less → outName.fits). This pools
// every channel's photons into one high-SNR mono for the "combined all-channel" output.
//
// Siril names the linked sequence with a trailing separator (staged files 0_L.fits… become "<seq>_.seq"),
// so the stack addresses "<seq>_" — exactly as StackSelectedScript does. This Siril's average-stacking
// grammar has no bare "mean"; it requires a rejection method + two sigmas, so a *gentle* winsorized
// rejection is used (which caps rather than drops, and clips ~0% across the few, dissimilar channel
// frames — it only trims a gross per-channel artifact). `addscale` normalization brings the very
// different broadband/narrowband channels onto one footing so none swamps the others, and weight (e.g.
// "nbstack") favours the deeper channels by their STACKCNT sub-count (empty → unweighted).
func IntegrateChannelsScript(seq, outName string, weight stackalg.Weight) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	// `rej none` (live-pinned grammar, and NOT user-configurable here): the "frames" are DIFFERENT
	// CHANNELS, not repeated samples — outlier rejection treats a channel's real morphology (an
	// Ha-bright starburst core) as per-pixel outliers and clips it patchily, posterizing the
	// monostack's bright cores.
	o := stackalg.DefaultChannelIntegration()
	o.Weight = weight
	fmt.Fprintf(&b, "stack %s_ %s -out=%s\n", seq, StackClause(o, 0), outName)
	return b.String()
}

// PlanetaryStackScript stacks the best (selected) frames of a converted video sequence — no
// registration (planetary/lunar surfaces have no stars) — then optionally sharpens, stretches
// and saves to the given formats.
func PlanetaryStackScript(seq string, count int, rejected []int, outName string, sharpen bool, formats []string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	if count > 0 {
		fmt.Fprintf(&b, "select %s 1 %d\n", seq, count)
	}
	for _, idx := range rejected {
		fmt.Fprintf(&b, "unselect %s %d %d\n", seq, idx, idx)
	}
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -nonorm -filter-incl -out=%s\n", seq, outName)
	fmt.Fprintf(&b, "load %s\n", outName)
	if sharpen {
		b.WriteString("unsharp 3 0.8\n")
	}
	b.WriteString("autostretch\n")
	for _, f := range formats {
		b.WriteString(saveCmd(f, outName) + "\n")
	}
	return b.String()
}

// StackPlanetaryChannelScript converts one channel's pre-aligned lucky-imaging frames (only the kept,
// surface-registered frames were written, so all are included) into a sequence and stacks them with
// brightness normalization + winsorized rejection into outName (a linear master). Run with CWD set to
// the aligned-frames dir; `seq` is the convert target name (e.g. "al").
func StackPlanetaryChannelScript(seq, outName string) string {
	return scriptHeader +
		fmt.Sprintf("convert %s -out=.\n", seq) +
		fmt.Sprintf("stack %s rej winsorized 3 3 -norm=addscale -output_norm -out=%s\n", seq, outName)
}

// DeconvolveLuminanceScript sharpens a linear master IN PLACE by Richardson-Lucy deconvolution with a
// Gaussian PSF — it recovers PSF-blurred (seeing/optics) surface detail with far less edge-overshoot
// than an unsharp mask, so it sharpens without burning highlights. Run on LINEAR data before any
// stretch (planetary lucky-imaging), on the luminance (L) or the single mono master. master is an
// absolute base path (no extension); the file is reloaded and overwritten.
func DeconvolveLuminanceScript(master string, fwhm float64, iters, alpha int) string {
	var sb strings.Builder
	sb.WriteString(scriptHeader)
	fmt.Fprintf(&sb, "load %s\n", master)
	fmt.Fprintf(&sb, "makepsf manual -gaussian -fwhm=%.1f\n", fwhm)
	fmt.Fprintf(&sb, "rl -iters=%d -tv -alpha=%d\n", iters, alpha)
	fmt.Fprintf(&sb, "save %s\n", master)
	return sb.String()
}

// PlanetaryFinishScript composes the (already deconvolved-luminance) per-channel linear masters into the
// final image and exports it. With r/g/b set it builds a colour image (l supplies the luminance via
// `rgbcomp -lum=L`); otherwise it loads the single mono master. Then a highlight-safe generalized
// hyperbolic stretch (`ght`, keeping [HP,1] linear so bright craters/highlands don't blow out), à-trous
// wavelet sharpening (boost the mid-fine layers), gentle CLAHE for local relief, and — for colour — a
// saturation boost. All paths absolute; outBase has no extension.
// PlanetaryFinish tunes the lucky-imaging finish (stretch / wavelet sharpen / local contrast / colour).
// Its defaults reproduce the original hand-tuned constants; the supervised finish re-tunes these.
type PlanetaryFinish struct {
	Stretch    float64 // ght -D: overall hyperbolic stretch intensity
	Highlight  float64 // ght -HP: highlight-protection point ([HP,1] stays linear, keeping bright craters intact)
	// ShadowLift opens the shadow tones (crater floors, the terminator side) by sliding the ght
	// symmetry point — where the stretch is most intense — down into the shadows: SP = 0.18·(1−s) +
	// 0.04·s. Dark tones gain slope (visible detail) instead of compressing toward black; [HP,1]
	// stays linear so the highlights are untouched. 0 (default) emits the historical `-SP=0.18`
	// literal, keeping the finish script byte-identical.
	ShadowLift float64
	Sharpen    float64 // à-trous mid-layer boost scalar (1 = default; 0 = none, >1 sharper); ignored when sharpen=false
	Clahe      float64 // CLAHE clip limit (local relief)
	Saturation float64 // colour saturation boost (colour path only)
	// Headroom scales the linear image down by this factor (fmul) BEFORE the stretch/sharpen, so the
	// brightest real detail lands at HP and there is ~(1−Headroom) room above for the wavelet/CLAHE
	// overshoot before it clips to white — this is what stops the bright full-Moon disk "burning". Only
	// applied when 0 < Headroom < 1; 1 or 0 → no scaling (a refine/agent can disable it).
	Headroom float64
	// EarthshineGain reveals the Moon's unlit side (earthshine) in the final render: 0 = off (the
	// default), the UI opt-in sends 1.0, clamped to 0.2..2 when enabled. PlanetaryFinishScript
	// deliberately ignores it — the lift is a Go compositing step between the finish script and the
	// export script (planetary.runFinish) — so with it unset the single-script finish stays
	// byte-identical to the historical path.
	EarthshineGain float64
	// EarthshineFeather is the terminator protection margin of the earthshine composite, as a
	// FRACTION of the fitted disc radius (drizzle-safe): it sets the hard dilation of the
	// illumination mask's byte-identical lit support (larger keeps the lift farther from the
	// lit edge). 0 means the default (0.006); clamped to 0.002..0.02 when set. Ignored by the
	// finish script, exactly like EarthshineGain.
	EarthshineFeather float64
	// TrueLum splits the colour finish so Go can re-impose the deconvolved L master as the EXACT
	// output luminance after the RGB compose: Siril's `rgbcomp -lum` leaks the soft, un-deconvolved
	// RGB lightness into the composite, visibly diluting the sharp L that carries the detail. On by
	// default for LRGB runs (planetary.runFinish); mono runs and a zero-value PlanetaryFinish are
	// unaffected. Like EarthshineGain, this script deliberately ignores it.
	TrueLum bool
	// LimbBalance (0..1) compresses the smooth ILLUMINATION field of the lit surface before the
	// stretch — local (crater-scale) contrast is untouched, so the bright limb keeps its detail
	// instead of stretching into a burnt band while the terminator keeps its depth. A Go stage on
	// master copies (planetary.limbBalance); the scripts deliberately ignore it. 0 = off.
	LimbBalance float64
}

// DefaultPlanetaryFinish is the original mineral-Moon finish (ght -D=0.6 -SP=0.18 -HP=0.85, wrecons
// 1 2.2 1.8 1.1 1 1 = Sharpen 1.0, clahe 1.2, satu 0.8), with a 0.85 headroom so the bright disk keeps
// structure instead of clipping to white after the sharpen/CLAHE boost. ShadowLift 0 (off) → the
// historical `-SP=0.18` line.
func DefaultPlanetaryFinish() PlanetaryFinish {
	return PlanetaryFinish{Stretch: 0.6, Highlight: 0.85, Sharpen: 1.0, Clahe: 1.2, Saturation: 0.8,
		Headroom: 0.85, TrueLum: true, EarthshineFeather: 0.006}
}

func PlanetaryFinishScript(r, g, b, l, mono, outBase string, sharpen bool, fin PlanetaryFinish, formats []string) string {
	var sb strings.Builder
	sb.WriteString(scriptHeader)
	color := false
	switch {
	case r != "" && g != "" && b != "" && l != "":
		fmt.Fprintf(&sb, "rgbcomp %s %s %s -lum=%s -out=%s\n", r, g, b, l, outBase)
		fmt.Fprintf(&sb, "load %s\n", outBase)
		color = true
	case r != "" && g != "" && b != "":
		fmt.Fprintf(&sb, "rgbcomp %s %s %s -out=%s\n", r, g, b, outBase)
		fmt.Fprintf(&sb, "load %s\n", outBase)
		color = true
	default:
		fmt.Fprintf(&sb, "load %s\n", mono)
	}
	writePlanetaryTone(&sb, color, sharpen, fin)
	for _, f := range formats {
		sb.WriteString(saveCmd(f, outBase) + "\n")
	}
	return sb.String()
}

// writePlanetaryTone emits the tone chain shared by PlanetaryFinishScript and PlanetaryToneScript
// (single source, so the split finish can never drift from the historical one).
func writePlanetaryTone(sb *strings.Builder, color, sharpen bool, fin PlanetaryFinish) {
	// Scale down first so the brightest detail lands at HP with room to spare: the sharpen + CLAHE that
	// follow overshoot the highlights, and without this headroom the bright disk clips to a burned white.
	if fin.Headroom > 0 && fin.Headroom < 1 {
		fmt.Fprintf(sb, "fmul %.3f\n", fin.Headroom)
	}
	// Highlight-safe stretch: everything above HP stays linear, so the Moon's bright ray-craters and
	// highlands keep their structure instead of clipping to white. ShadowLift slides the symmetry
	// point (max-slope tone) into the shadows so dark detail gains slope instead of crushing toward
	// black; at 0 the emitted line is byte-identical to the historical `-SP=0.18`.
	sp := "0.18"
	if fin.ShadowLift > 0 {
		sp = fmt.Sprintf("%.3f", 0.18*(1-fin.ShadowLift)+0.04*fin.ShadowLift)
	}
	fmt.Fprintf(sb, "ght -D=%.3f -SP=%s -HP=%.3f\n", fin.Stretch, sp, fin.Highlight)
	if sharpen {
		// À-trous wavelet sharpen: boost the mid-fine detail layers (crater edges), leave coarse ≈1, then
		// a gentle CLAHE for local relief. The Sharpen scalar scales the mid-layer boost (1.0 reproduces the
		// original 1 2.2 1.8 1.1 1 1). Deconvolution already recovered the fine detail, so no unsharp.
		sb.WriteString("wavelet 6 2\n")
		fmt.Fprintf(sb, "wrecons 1 %.2f %.2f %.2f 1 1\n", 1+1.2*fin.Sharpen, 1+0.8*fin.Sharpen, 1+0.1*fin.Sharpen)
		fmt.Fprintf(sb, "clahe %.2f 12\n", fin.Clahe)
	}
	if color {
		// The Moon's mineral colour (blue titanium maria, tan iron highlands) is real but faint at these
		// exposures — boost saturation to reveal it (the classic "mineral Moon").
		fmt.Fprintf(sb, "satu %.3f 0\n", fin.Saturation)
	}
}

// PlanetaryComposeScript is the compose half of the split colour finish: ONLY the rgbcomp into
// outBase, so Go can re-impose the deconvolved L as the true luminance before the tone stage.
func PlanetaryComposeScript(r, g, b, l, outBase string) string {
	var sb strings.Builder
	sb.WriteString(scriptHeader)
	if l != "" {
		fmt.Fprintf(&sb, "rgbcomp %s %s %s -lum=%s -out=%s\n", r, g, b, l, outBase)
	} else {
		fmt.Fprintf(&sb, "rgbcomp %s %s %s -out=%s\n", r, g, b, outBase)
	}
	return sb.String()
}

// PlanetaryToneScript is the tone half of the split finish: load the composite (or the mono
// master), run the shared tone chain, save the given formats to outBase.
func PlanetaryToneScript(loadPath, outBase string, color, sharpen bool, fin PlanetaryFinish, formats []string) string {
	var sb strings.Builder
	sb.WriteString(scriptHeader)
	fmt.Fprintf(&sb, "load %s\n", loadPath)
	writePlanetaryTone(&sb, color, sharpen, fin)
	for _, f := range formats {
		sb.WriteString(saveCmd(f, outBase) + "\n")
	}
	return sb.String()
}

// ExportScript loads an already-finished image (outBase.fits, written by the finish script and
// possibly rewritten by the Go earthshine composite) and saves it in the given formats — the second
// stage of the two-stage earthshine finish.
func ExportScript(outBase string, formats []string) string {
	var sb strings.Builder
	sb.WriteString(scriptHeader)
	fmt.Fprintf(&sb, "load %s\n", outBase)
	for _, f := range formats {
		sb.WriteString(saveCmd(f, outBase) + "\n")
	}
	return sb.String()
}

func saveCmd(format, base string) string {
	switch format {
	case "png":
		return "savepng " + base
	case "tif", "tiff":
		return "savetif " + base
	case "jpg", "jpeg":
		return "savejpg " + base + " 95"
	default:
		return "save " + base
	}
}

func calibrateArgs(m CalibMasters) []string {
	var args []string
	if m.Dark != "" {
		args = append(args, "-dark="+m.Dark)
	}
	if m.Flat != "" {
		args = append(args, "-flat="+m.Flat)
	}
	if m.Bias != "" {
		args = append(args, "-bias="+m.Bias)
	}
	switch {
	case m.BadPixelMap != "":
		// Per-frame cosmetic repair from the measured defect map (hot/cold + warm + unstable/RTS
		// pixels found across the raw dark pool) — strictly stronger than -cc=dark, which only sees
		// the master's static outliers. Verified syntax on Siril 1.4.3: `-cc=bpm <file>`.
		args = append(args, "-cc=bpm "+m.BadPixelMap)
	case m.Dark != "":
		args = append(args, "-cc=dark") // cosmetic hot/cold pixel correction from the dark
	}
	if m.DarkOptimize && m.Dark != "" && m.Bias != "" {
		args = append(args, "-opt") // scale the different-exposure dark onto the lights' thermal signal
	}
	// One-shot-color: only meaningful when there is at least one master to apply. -cfa makes the
	// cosmetic/flat maths CFA-aware, -equalize_cfa balances the flat's Bayer channels, and -debayer
	// demosaics after calibration (the convert step stays raw Bayer).
	if m.CFA && len(args) > 0 {
		args = append(args, "-cfa")
		if m.Flat != "" {
			args = append(args, "-equalize_cfa")
		}
		args = append(args, "-debayer")
	}
	return args
}

// DenoiseOptions tunes the Siril `denoise` command. Modulation in (0,1) blends the denoised and
// original images (1 = full strength); VST applies the generalized Anscombe transform (recommended
// for photon-limited linear sub-stacks); DA3D adds a detail-preserving final stage.
type DenoiseOptions struct {
	Modulation float64
	VST        bool
	DA3D       bool
	NoCosmetic bool
	Indep      bool
}

// Enabled reports whether the denoise would do anything (a zero modulation is a no-op).
func (o DenoiseOptions) Enabled() bool { return o.Modulation > 0 }

// DenoiseScript loads a (linear) image, denoises it and overwrites it in place.
func DenoiseScript(loadName, outName string, o DenoiseOptions) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "load %s\n", loadName)
	b.WriteString("denoise" + denoiseArgs(o) + "\n")
	fmt.Fprintf(&b, "save %s\n", outName)
	return b.String()
}

func denoiseArgs(o DenoiseOptions) string {
	var args []string
	// -vst (Anscombe, best for linear photon noise) is incompatible with -da3d/-sos, so VST wins.
	if o.VST {
		args = append(args, "-vst")
	} else if o.DA3D {
		args = append(args, "-da3d")
	}
	if o.Indep {
		args = append(args, "-indep")
	}
	if o.NoCosmetic {
		args = append(args, "-nocosmetic")
	}
	if o.Modulation > 0 && o.Modulation < 1 {
		args = append(args, fmt.Sprintf("-mod=%.2f", o.Modulation))
	}
	if len(args) == 0 {
		return ""
	}
	return " " + strings.Join(args, " ")
}

// PreviewScript loads an image, optionally downscales it, auto-stretches and saves a PNG thumbnail
// for the UI. downscale in (0,1) shrinks the preview; <=0 keeps full size.
func PreviewScript(loadName, outName string, downscale float64) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "load %s\n", loadName)
	if downscale > 0 && downscale < 1 {
		fmt.Fprintf(&b, "resample %.3f\n", downscale)
	}
	b.WriteString("autostretch\n")
	fmt.Fprintf(&b, "savepng %s\n", outName)
	return b.String()
}

// SolveOptions parameterize Siril `platesolve`. Coords ("RA,Dec" or "HH:MM:SS,DD:MM:SS") may be
// empty to use the header WCS; LocalAsnet uses a local astrometry.net solve (offline) when set.
// AstroCat/XpsampDir point Siril at the LOCAL Gaia DR3 catalogues (the astrometric extract file and
// the xp_sampled chunk directory) via `set core.catalogue_gaia_*` script lines — callers set them
// only when the files actually exist, and a set AstroCat makes `-catalog=localgaia` the default so
// plate-solving works fully offline.
type SolveOptions struct {
	Coords     string
	FocalMM    float64
	PixelUm    float64
	Catalog    string
	LocalAsnet bool
	AstroCat   string // local Gaia astrometric catalogue file (core.catalogue_gaia_astro)
	XpsampDir  string // local Gaia xp_sampled chunk dir (core.catalogue_gaia_photo)
}

// SpccOptions parameterize Siril `spcc` (SpectroPhotometric Color Calibration).
type SpccOptions struct {
	MonoSensor string
	OSCSensor  string
	RFilter    string
	GFilter    string
	BFilter    string
	WhiteRef   string
	Narrowband bool
	Catalog    string // "" (Siril picks local when installed) | "gaia" | "localgaia"
}

// ColorCalibrateScript plate-solves the loaded image then runs SPCC, saving the calibrated result.
// It is run as its own Siril invocation so the caller can branch (fall back) when solving fails.
func ColorCalibrateScript(loadName, outName string, s SolveOptions, c SpccOptions) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	b.WriteString(catalogueSetCmds(s))
	fmt.Fprintf(&b, "load %s\n", loadName)
	b.WriteString(platesolveCmd(s) + "\n")
	b.WriteString(spccCmd(c) + "\n")
	fmt.Fprintf(&b, "save %s\n", outName)
	return b.String()
}

// PhotometricCalibrateScript plate-solves and colour-calibrates the linear image with Siril's PCC
// (Gaia photometry, no per-star spectra) — the ladder rung between SPCC and the star-field fallback.
// It exists because SPCC can fail where PCC succeeds on the very same solve: the distro arm64 Siril
// 1.4.4 segfaults inside SPCC's aperture photometry (verified in-container on real data, local AND
// online catalogues), while `pcc` completes in seconds. Same catalogue redirection as
// ColorCalibrateScript so the offline Gaia data is used when present.
func PhotometricCalibrateScript(loadName, outName string, s SolveOptions) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	b.WriteString(catalogueSetCmds(s))
	fmt.Fprintf(&b, "load %s\n", loadName)
	b.WriteString(platesolveCmd(s) + "\n")
	b.WriteString("pcc\n")
	fmt.Fprintf(&b, "save %s\n", outName)
	return b.String()
}

// ParityProbeScript plate-solves a single frame WITHOUT flipping it (-noflip) and saves the result, so
// the caller can read the solved WCS and derive the image parity (sign of det(CD)). Because -noflip leaves
// the pixels untouched, the probe's parity matches the frames the caller will mirror.
func ParityProbeScript(loadName, outName string, s SolveOptions) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	b.WriteString(catalogueSetCmds(s))
	fmt.Fprintf(&b, "load %s\n", loadName)
	fmt.Fprintf(&b, "%s -noflip\n", platesolveCmd(s))
	fmt.Fprintf(&b, "save %s\n", outName)
	return b.String()
}

// catalogueSetCmds emits `set core.catalogue_gaia_*` lines pointing Siril at the local Gaia
// catalogues, so every solve/SPCC in the script uses the offline data regardless of the user's own
// Siril preferences (and identically in the Docker engine). Empty options yield no lines.
func catalogueSetCmds(s SolveOptions) string {
	var b strings.Builder
	if s.AstroCat != "" {
		b.WriteString(setCmd("core.catalogue_gaia_astro", s.AstroCat) + "\n")
	}
	if s.XpsampDir != "" {
		b.WriteString(setCmd("core.catalogue_gaia_photo", s.XpsampDir) + "\n")
	}
	return b.String()
}

// setCmd formats a Siril `set key=value` line, quoting the whole key=value token only when the
// value contains whitespace (same tokenizer rule as sirilKV).
func setCmd(key, val string) string {
	tok := key + "=" + val
	if strings.ContainsAny(tok, " \t") {
		return "set " + fmt.Sprintf("%q", tok)
	}
	return "set " + tok
}

// MirrorFramesScript flips each named frame about the horizontal axis in place (load/mirrorx/save),
// inverting its parity so a mirror-flipped session can co-register with the others. Siril has no
// sequence-level mirror, so each frame is handled individually; an empty list yields a header-only no-op.
func MirrorFramesScript(names []string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	for _, n := range names {
		fmt.Fprintf(&b, "load %s\nmirrorx\nsave %s\n", n, n)
	}
	return b.String()
}

func platesolveCmd(s SolveOptions) string {
	cmd := "platesolve"
	if s.Coords != "" {
		cmd += " " + s.Coords
	}
	if s.FocalMM > 0 {
		cmd += fmt.Sprintf(" -focal=%.1f", s.FocalMM)
	}
	if s.PixelUm > 0 {
		cmd += fmt.Sprintf(" -pixelsize=%.2f", s.PixelUm)
	}
	switch {
	case s.Catalog != "":
		cmd += " -catalog=" + s.Catalog
	case s.AstroCat != "":
		// A local astrometric catalogue is installed — prefer it so solving needs no network.
		cmd += " -catalog=localgaia"
	}
	if s.LocalAsnet {
		cmd += " -localasnet"
	}
	return cmd
}

func spccCmd(c SpccOptions) string {
	args := []string{"spcc"}
	switch {
	case c.OSCSensor != "":
		args = append(args, sirilKV("-oscsensor", c.OSCSensor))
	case c.MonoSensor != "":
		args = append(args, sirilKV("-monosensor", c.MonoSensor))
		if c.RFilter != "" {
			args = append(args, sirilKV("-rfilter", c.RFilter))
		}
		if c.GFilter != "" {
			args = append(args, sirilKV("-gfilter", c.GFilter))
		}
		if c.BFilter != "" {
			args = append(args, sirilKV("-bfilter", c.BFilter))
		}
	}
	if c.WhiteRef != "" {
		args = append(args, sirilKV("-whiteref", c.WhiteRef))
	}
	if c.Narrowband {
		args = append(args, "-narrowband")
	}
	if c.Catalog != "" {
		args = append(args, "-catalog="+c.Catalog)
	}
	return strings.Join(args, " ")
}

// sirilKV formats a `-key=value` argument, wrapping the WHOLE token in double quotes. Siril's SSF
// tokenizer splits on whitespace and does NOT honor quotes around only the value, so quoting just
// the value (e.g. -monosensor="ZWO ASI1600MM") makes Siril read the trailing ASI1600MM" as a
// separate, invalid argument. The entire token must be quoted: "-monosensor=ZWO ASI1600MM".
// Verified against Siril 1.4.3 `spcc`: the value-only form aborts with "Invalid argument", the
// whole-token form succeeds.
func sirilKV(key, val string) string {
	return fmt.Sprintf("%q", key+"="+val)
}

// SubskyCmd returns a Siril `subsky` background-extraction command for the given polynomial degree,
// clamped to Siril's valid [1,4] range (Siril rejects degree 0 and >4 with "Polynomial degree order
// must be within the [1, 4] range"). Centralised so no caller can emit an out-of-range degree.
func SubskyCmd(degree int) string {
	if degree < 1 {
		degree = 1
	}
	if degree > 4 {
		degree = 4
	}
	return fmt.Sprintf("subsky %d\n", degree)
}

// SubskyRBFCmd builds a Siril RBF (radial-basis-function) background extraction — a smooth, spline-like
// model that handles complex asymmetric gradients (amp-glow + vignetting + light pollution) far better
// than a low-degree polynomial. Used as the deterministic fallback for the combined-RGB gradient pass
// when GraXpert is unavailable. `-tolerance=1.5` keeps stars/galaxy out of the background samples;
// `-dither` avoids banding on the low-dynamic-range sky.
func SubskyRBFCmd() string {
	return "subsky -rbf -smooth=0.5 -samples=30 -tolerance=1.5 -dither\n"
}

// AutostretchCmd builds a Siril `autostretch` with an explicit dark target background. Siril's bare
// `autostretch` targets a bright 0.25 background — a washed-out, lifted sky that reads as a brown/grey
// haze once channels are combined and curved. Deep-sky finishing wants a dark sky (~0.05–0.08), so we
// pass the full signature `autostretch [-linked] <shadowsclip> <targetbg>` (shadowsclip stays at the
// −2.8σ default; the second positional is the target background). `linked` stretches all channels with
// one transfer function, preserving the (SPCC-)neutralized color balance instead of re-cast­ing it.
// targetBg is clamped to a sane (0, 0.5] range; 0 falls back to 0.06.
func AutostretchCmd(linked bool, targetBg float64) string {
	if targetBg <= 0 || targetBg > 0.5 {
		targetBg = 0.06
	}
	cmd := "autostretch"
	if linked {
		cmd += " -linked"
	}
	return cmd + fmt.Sprintf(" -2.8 %.3f", targetBg)
}

// NeutralizeScript is the offline color fallback: extract the background (equalizing channels toward
// a neutral sky) and remove the residual green cast, saving in place. scnrType: 0 average-neutral.
func NeutralizeScript(loadName, outName string, bgDegree, scnrType int) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "load %s\n", loadName)
	if bgDegree > 0 {
		fmt.Fprintf(&b, "subsky %d\n", bgDegree)
	}
	fmt.Fprintf(&b, "rmgreen %d\n", scnrType)
	fmt.Fprintf(&b, "save %s\n", outName)
	return b.String()
}

// FinishScript stretches a (color-calibrated, neutral) image and exports it. A linked stretch keeps
// the channel balance so the background stays neutral rather than re-acquiring a cast.
func FinishScript(loadName, outName string, linked bool, saturation float64, formats []string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "load %s\n", loadName)
	if linked {
		b.WriteString("autostretch -linked\n")
	} else {
		b.WriteString("autostretch\n")
	}
	if saturation > 0 {
		fmt.Fprintf(&b, "satu %.2f\n", saturation)
	}
	for _, f := range formats {
		b.WriteString(saveCmd(f, outName) + "\n")
	}
	return b.String()
}
