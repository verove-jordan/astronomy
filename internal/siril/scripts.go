package siril

import (
	"fmt"
	"strings"
)

// scriptHeader requires a modern Siril and fixes the output extension so produced masters and
// stacks are predictably named `<name>.fits`.
const scriptHeader = "requires 1.2.0\nsetext fits\n"

// CalibMasters holds absolute paths to the master calibration frames to apply (any may be empty).
type CalibMasters struct {
	Bias string
	Dark string
	Flat string
}

// StackMasterScript links the FITS already in the work dir as sequence `seq` and stacks them
// with Winsorized sigma rejection into outName (extension added by Siril). Used for darks/bias:
// no normalization.
func StackMasterScript(seq, outName string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -nonorm -out=%s\n", seq, outName)
	return b.String()
}

// StackFlatScript builds a master flat: optionally bias-calibrate the flats, then stack with
// multiplicative normalization (the correct normalization for flat fields).
func StackFlatScript(seq, outName, biasPath string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	target := seq
	if biasPath != "" {
		fmt.Fprintf(&b, "calibrate %s -bias=%s -prefix=pp_\n", seq, biasPath)
		target = "pp_" + seq
	}
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -norm=mul -out=%s\n", target, outName)
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
// registers them together (global star alignment), co-registering the channels to one reference.
func AlignMastersScript(seq string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
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

// CalibrateRegisterFramedScript calibrates (if masters are given), computes registration in two passes
// (metrics only, no transformed images), then applies it with a chosen output framing. Used for the
// cross-session merge so frames from differently-oriented sessions are cropped to their common field of
// view (framing="min" = the area shared by all frames). The two-pass register also picks the reference
// by quality + framing, which suits heterogeneous data. transf defaults to homography — Siril's default,
// which already absorbs field rotation. Metrics land in <target>_.seq (read by grading), and seqapplyreg
// generates r_<target> for the frames that registered, preserving the 1:1 index space grading relies on.
func CalibrateRegisterFramedScript(seq string, m CalibMasters, transf, framing string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	target := seq
	if args := calibrateArgs(m); len(args) > 0 {
		fmt.Fprintf(&b, "calibrate %s %s -prefix=pp_\n", seq, strings.Join(args, " "))
		target = "pp_" + seq
	}
	reg := "register " + target + " -2pass"
	if transf != "" {
		reg += " -transf=" + transf
	}
	fmt.Fprintf(&b, "%s\n", reg)
	apply := "seqapplyreg " + target
	if framing != "" {
		apply += " -framing=" + framing
	}
	fmt.Fprintf(&b, "%s\n", apply)
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
// Winsorized sigma rejection + light (addscale) normalization into outName — no calibration or
// registration. Used for the comet-mode per-channel stacks (the frames are already globally star-aligned,
// or comet-translated): the rejection clips the *moving* feature (stars in the comet stack, the comet in
// the star stack), leaving the fixed one sharp.
func StackAlignedScript(seq, outName string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -norm=addscale -output_norm -out=%s\n", seq, outName)
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
// frames (1-based registered indices), then stacks only the survivors. Winsorized sigma rejection
// additionally clips residual satellite/plane trail pixels.
// weight (if non-empty) is a Siril stack weighting mode (noise|wfwhm|nbstars|nbstack); it favors the
// sharper/deeper subs and is used for the cross-session merge. Empty leaves the stack unweighted.
func StackSelectedScript(regSeq string, regCount int, rejected []int, outName, weight string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	if regCount > 0 {
		fmt.Fprintf(&b, "select %s 1 %d\n", regSeq, regCount)
	}
	for _, idx := range rejected {
		fmt.Fprintf(&b, "unselect %s %d %d\n", regSeq, idx, idx)
	}
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -norm=addscale -output_norm%s -filter-incl -out=%s\n",
		regSeq, weightArg(weight), outName)
	return b.String()
}

// weightArg renders the optional stack weighting flag (empty string when unweighted, keeping the
// command byte-identical to the unweighted path).
func weightArg(weight string) string {
	if weight == "" {
		return ""
	}
	return " -weight=" + weight
}

// ConvertScript converts the files in the work dir into a FITS sequence named `seq`.
func ConvertScript(seq string) string {
	return scriptHeader + fmt.Sprintf("convert %s -out=.\n", seq)
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
	if m.Dark != "" {
		args = append(args, "-cc=dark") // cosmetic hot/cold pixel correction from the dark
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
type SolveOptions struct {
	Coords     string
	FocalMM    float64
	PixelUm    float64
	Catalog    string
	LocalAsnet bool
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
}

// ColorCalibrateScript plate-solves the loaded image then runs SPCC, saving the calibrated result.
// It is run as its own Siril invocation so the caller can branch (fall back) when solving fails.
func ColorCalibrateScript(loadName, outName string, s SolveOptions, c SpccOptions) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "load %s\n", loadName)
	b.WriteString(platesolveCmd(s) + "\n")
	b.WriteString(spccCmd(c) + "\n")
	fmt.Fprintf(&b, "save %s\n", outName)
	return b.String()
}

// ParityProbeScript plate-solves a single frame WITHOUT flipping it (-noflip) and saves the result, so
// the caller can read the solved WCS and derive the image parity (sign of det(CD)). Because -noflip leaves
// the pixels untouched, the probe's parity matches the frames the caller will mirror.
func ParityProbeScript(loadName, outName string, s SolveOptions) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "load %s\n", loadName)
	fmt.Fprintf(&b, "%s -noflip\n", platesolveCmd(s))
	fmt.Fprintf(&b, "save %s\n", outName)
	return b.String()
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
	if s.Catalog != "" {
		cmd += " -catalog=" + s.Catalog
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
