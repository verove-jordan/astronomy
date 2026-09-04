// Package mode defines the capture modes (deepsky/nebula/milkyway/planetary) and the output
// format, and maps each mode to a Preset that retunes grading, background extraction, stretch,
// Ha blending, saturation and final curves across the whole pipeline.
package mode

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/solar"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// BrightnessTarget maps the milkyway brightness control to an auto-levels target sky-background level
// (Siril-style, in (0,0.5)). It accepts the keywords darker/balanced/brighter or a raw 0..0.5 number;
// ok is false for an empty value (keep the preset default) or an unparseable/out-of-range one.
func BrightnessTarget(s string) (level float64, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 0, false
	case "darker", "dark":
		return 0.035, true
	case "balanced", "balance", "default":
		return 0.05, true
	case "brighter", "bright":
		return 0.07, true
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && v > 0 && v < 0.5 {
		return v, true
	}
	return 0, false
}

// Mode is a capture subject type; it drives the whole pipeline preset.
type Mode string

const (
	Deepsky   Mode = "deepsky"   // galaxies/clusters at native focal length (mono LRGB+Ha)
	Nebula    Mode = "nebula"    // large faint emission objects (mono LRGB+Ha, Ha-forward)
	Milkyway  Mode = "milkyway"  // wide-field one-shot-color (e.g. iPhone ProRAW/HEIC)
	Planetary Mode = "planetary" // Moon/planets via lucky imaging
	Livestack Mode = "livestack" // watch a source + incrementally stack during a session; finalize = deepsky
	Comet     Mode = "comet"     // moving comet: dual star/comet stack + star-layer recomposite
	Mosaic    Mode = "mosaic"    // tiled panels of one large object: per-panel deepsky stacks + WCS assembly
	Sun       Mode = "sun"       // the Sun in Hα or white light: limb-registered lucky imaging
	// Nightpano is a sky panorama swept by hand across many pointings: each pointing stacks with the
	// milkyway recipe, then every panel is plate-solved and reprojected onto ONE spherical canvas.
	// Distinct from Mosaic, which is gnomonic and cannot represent an arch wider than a hemisphere.
	Nightpano Mode = "nightpano"
	// Eclipse is a partially eclipsed Sun: the solar recipe, but with the occulting Moon measured as
	// a second circle and masked out of the stack and of every on-disc measurement. It is a separate
	// mode rather than a knob on Sun because the geometry changes what every measurement MEANS —
	// "the disc" is no longer the subject, "the limb" is two limbs, and the frame's brightest edge
	// belongs to a body that moves while the Sun does not.
	Eclipse Mode = "eclipse"
)

// Format is the desired output artifact.
type Format string

const (
	FormatImage Format = "image"
	FormatVideo Format = "video"
	FormatBoth  Format = "both"
)

// ColorModel is how the sensor records color.
type ColorModel string

const (
	Mono ColorModel = "mono" // per-filter monochrome frames → LRGB+Ha/OIII/SII
	OSC  ColorModel = "osc"  // one-shot color (Bayer) → a single RGB sequence
)

// The [SII] emission screen's colour. See Preset.SIITint.
const (
	// SIITintDeepRed renders [SII] as a crimson: green killed, a trace of blue kept so it separates
	// from the Ha screen's pure red instead of simply adding to it. The physically honest choice —
	// 672 nm really is past Hα — and the default.
	SIITintDeepRed = "deep_red"
	// SIITintGold renders [SII] as an amber accent. Not what the eye would see, but it is the
	// convention the Hubble palette established for sulphur, and it makes SII structure obvious
	// against Hα rather than merely deepening it.
	SIITintGold = "gold"
)

// IsSIITint reports whether s names a supported [SII] tint (empty = the default, deep red).
func IsSIITint(s string) bool {
	switch s {
	case "", SIITintDeepRed, SIITintGold:
		return true
	}
	return false
}

// Preset bundles every tunable the pipeline reads, derived from a Mode.
type Preset struct {
	Mode             Mode
	Color            ColorModel
	Grade            grade.Options     // frame-quality rejection thresholds
	BackgroundDegree int               // Siril subsky polynomial degree (0 = skip)
	HaScreen         float64           // Ha layer opacity when screened into the composite (0 = none)
	Saturation       float64           // final saturation boost
	Curve            []float64         // gentle value curve (post-combine); flat x,y pairs in 0..1
	LumCurve         []float64         // galaxy-brightness curve applied to the L luminance layer (LRGB)
	LumOpacity       float64           // opacity (0..1) of the L luminance layer in the LRGB composite (1 = full detail from L; lower = softer, more RGB-driven). 0/unset → full.
	Planetary        planetary.Options // lucky-imaging settings (planetary only)
	Sun              solar.Preset      // solar ingest/stack/finish settings (sun only)

	// CoreHighlightKnee / CoreHighlightCeil roll off the L luminance highlights (after LumCurve, before the
	// Ha screen) to tame a blown nebula core: identity below knee (outer nebula/stars/sky untouched),
	// asymptoting the bright centre to ceil < 1 so it dims to a structured knot the Ha screen tints pink.
	// Disabled unless 0 < knee < ceil < 1. See internal/gimp/compose.go.
	CoreHighlightKnee float64
	CoreHighlightCeil float64

	// HighlightKnee / HighlightCeil add a star-safe highlight roll-off to the final composite (the last tone
	// op): a per-channel shoulder that keeps bright STAR cores below white and, being per-channel, pulls a
	// warm star's dominant (red) channel down most — so stars never burn and keep natural colour instead of
	// an orange/white blob. Distinct from CoreHighlightKnee/Ceil (the nebula CORE, on the L luminance).
	// Disabled unless 0 < knee < ceil < 1. See internal/gimp/compose.go.
	HighlightKnee float64
	HighlightCeil float64

	// StarDesat (0..1) desaturates the brightest star cores/wings toward white in the final composite,
	// through a luminosity-masked copy. Under LAYER-MODE-LUMINANCE the exported chroma at a star pixel
	// comes from the thin RGB base's noisy, exaggerated colour PSF, so on a dense star field (a cluster)
	// bright stars render as solid blue/magenta discs; this pushes only the bright cores/wings toward
	// white while background/extended-object colour is untouched. 0 → off. See internal/gimp/compose.go.
	StarDesat float64

	// StretchHeadroom caps bright highlights (star cores, galaxy nuclei) just below 1.0 in LINEAR light
	// BEFORE the finishing autostretch, keeping their per-channel ratios (colour) intact — a ratio-
	// preserving tanh roll-off applied to the RGB base and L masters (nightscape.CompressHighlights).
	// The MTF autostretch fixes 1.0→1.0, so a core left at the linear max clips to pure white and loses
	// its colour; capping it at StretchHeadroom means the stretch maps it just below white and the star
	// keeps its natural hue. This is the deep-sky analogue of the planetary finish Headroom/fmul.
	// 0 or ≥1 disables it (legacy behaviour). See internal/pipeline (applyStretchHeadroom).
	StretchHeadroom float64

	// AutoFixStars enables the deterministic, gated post-finish star repair: after the standard finish,
	// the engine measures the exported image for burnt / colour-flattened star cores and — only when it
	// finds fixable ones — re-enters the finish (Tier B re-stretch with more headroom, then cheap Tier-A
	// colour passes), keeping the best-scoring result. A clean finish is a no-op (zero extra cost). It
	// needs no vision model (distinct from Supervise). StarFixMaxIters caps the repair passes (0 →
	// default). See internal/pipeline/starfix.go.
	AutoFixStars    bool
	StarFixMaxIters int

	// Noise reduction (Siril `denoise` on the linear stacks). Chroma is denoised harder than
	// luminance to cut color noise while preserving detail; 0 skips a channel class.
	DenoiseChroma float64
	DenoiseLum    float64
	DenoiseVST    bool
	DenoiseDA3D   bool
	// DenoiseStarlet replaces Siril's opaque, spatially-uniform `denoise` with the Go à-trous
	// (starlet) multiscale denoiser (internal/noise): per-tile sigma-adaptive soft-thresholds with an
	// explicit high-SNR protection mask, so stars/cores stay untouched while the sky is cleaned. Soft-
	// fail: on any error the linear master is left as-is. false → the legacy Siril denoise path.
	DenoiseStarlet bool
	// DenoiseAuto scales the denoise strength (starlet or Siril) by the master's MEASURED noise + the
	// stacked-frame count: a noisy/shallow master is denoised harder, a clean/deep one gently, around
	// the DenoiseChroma/DenoiseLum baselines. false → the baselines apply verbatim.
	DenoiseAuto bool

	// StackWeight sets the Siril stack weighting mode ("" | noise | wfwhm | nbstars | nbstack). wfwhm
	// weights each sub by its star sharpness, so the deeper/sharper subs dominate — the cross-session
	// merge already uses it; single-session and OSC stacks were previously unweighted. "" → unweighted.
	// It is copied into Stack.Weight at the stack call site, because a photometrically normalized
	// multi-group channel may override it per channel (see pipeline.photomStackWeight).
	StackWeight string

	// Stack is the LIGHT-frame combination recipe: which method combines the pixels, which outlier
	// test rejects them and with what parameters, how the frames are normalized and weighted. Its
	// default (stackalg.DefaultLights) is the count-adaptive Winsorized/GESD/percentile clause the
	// engine has always emitted, so an untouched preset stacks byte-identically to before the knob.
	Stack stackalg.Options
	// StackComet is the COMET-ALIGNED stack's own recipe (comet mode only). It defaults to an
	// asymmetric winsorization that erases the marching star trails while protecting the faint tail
	// — see stackalg.DefaultComet. The star-aligned half of a comet run uses Stack.
	StackComet stackalg.Options
	// Masters is the calibration-master stacking recipe (bias / dark / flat), each with its own
	// physically-mandated normalization. See stackalg.DefaultMasters.
	Masters stackalg.MasterOptions

	// PhotomNorm photometrically normalizes heterogeneous calibration groups (sessions shot at
	// different exposure/gain/temperature) in Go before the cross-session merge: each group's linear
	// curve is measured and mapped onto the reference group's scale/offset, so Siril's addscale only
	// mops up per-frame drift. Default true for the deep-sky modes; it engages only when a channel
	// has >1 group (single-session runs never reach it). The old mis-measure on real mixed-gain data
	// ("Ha clamped at 5×") is fixed: a flat (narrowband/pedestal) curve is seeded from the header
	// exposure/gain instead of fitted, and the absolute clamp is wide enough for genuine cross-gain
	// ratios. See internal/photom.
	PhotomNorm bool

	// FlattenBg subtracts each frame's OWN degree-1 sky gradient (Siril seqsubsky, mean level
	// preserved) before a MULTI-NIGHT merged registration: the nights' gradients lie at their own
	// field rotations, so they cannot cancel in the stack and step at footprint boundaries — the
	// background seams of task #354. Engages only when a channel merges >1 group; soft-fails to
	// the unflattened sequence.
	FlattenBg bool

	// SeamOffsetRefit re-measures each night's background pedestal INSIDE its registered footprint
	// overlap with the anchor night and adds the residual offset to the night's frames before the
	// registered pixels are written. The whole-frame photometric fit compares different sky mixes
	// when footprints rotate, and its identity epsilon (0.2·σ_sub) passes pedestal residuals that
	// stack into visible steps (~σ_master) — the straight cut lines of multi-night masters.
	// Multi-group channels only; offset-only; heavily guarded, soft-fails to no-op per group.
	SeamOffsetRefit bool

	// SeamNoiseEq fades the noise-DEPTH change at the coverage boundaries of a multi-night master:
	// a coverage-weighted starlet pass whose per-pixel strength ramps from 0 (full-depth core —
	// byte-identical) up to √(depth ratio)−1, capped, before the standard denoise. Never-covered
	// pixels are untouched. Multi-group channels only.
	SeamNoiseEq bool

	// Mosaic keeps EVERY night's full field: the cross-night merge lands on the union of all
	// registered footprints (frames zero-padded to the Go-computed union bbox, re-registered, and
	// applied on the padded anchor canvas — Siril's own framing=max yields per-frame canvases and
	// cannot build the union) instead of cropping to the anchor night's frame. Off (default) =
	// today's anchor-canvas behaviour, byte-identical. Consent knob: never enabled by a preset.
	// NOTE: this is the SAME-POINTING multi-night union canvas, wire key "union_canvas" (legacy
	// alias "mosaic") — unrelated to the tiled-panel Mode "mosaic", which forces it off.
	Mosaic bool
	// MosaicFill decides the never-covered region of a mosaic canvas after combine: "crop"
	// (default) trims the final to the largest rectangle where every channel has real data;
	// "fill" keeps the whole union with the uncovered sky extrapolated (normalized convolution +
	// matched grain). Only meaningful when Mosaic is on.
	MosaicFill string

	// ---- Tiled-panel mosaic (Mode "mosaic") — the offset-panel assembler's knobs. ----

	// MosaicOverlapExpected is the capture overlap fraction the plan promised between neighboring
	// panels; it scales the blend feather and the segmentation sanity checks.
	MosaicOverlapExpected float64
	// MosaicFeatherFrac scales the center-weighted blend feather:
	// featherPx = frac × overlap × min(panel W, H).
	MosaicFeatherFrac float64
	// MosaicPhotomMatch matches panel photometry on the canvas: "gain_offset" (default; sky
	// pedestal AND transparency per panel) | "offset" | "off".
	MosaicPhotomMatch string
	// MosaicCanvasCrop decides the assembled canvas edges: "common" (largest rectangle every
	// channel covers — default) | "union" (keep everything) | "plan" (crop to the plan's grid bbox).
	MosaicCanvasCrop string
	// MosaicMinPanelFrames drops a panel whose channels stacked fewer total frames (a stray
	// mis-filed pointing must not become a hole-riddled pseudo-panel).
	MosaicMinPanelFrames int
	// MosaicPanelSource picks the panel segmentation: "auto" (p01/-style folders first, else
	// OBJCTRA/OBJCTDEC clustering) | "folders" | "coords".
	MosaicPanelSource string

	// PanoProjection selects the nightpano canvas: "stereographic" (conformal, the natural
	// look-up-at-the-sky rendering) | "galactic" (the Milky Way laid out as a level band) | "altaz"
	// (the arch as it stood over the horizon at one instant) | "both" (the first two) | "all".
	PanoProjection string
	// PanoScaleDegPerPix is the canvas scale. The panels are about 0.02 deg/px, so the default 0.03
	// deliberately samples a little coarser than the data: at the panels' own scale the canvas is
	// four times the pixels for detail the plate solution cannot place that precisely.
	PanoScaleDegPerPix float64
	// PanoGroupStepDeg splits panels when consecutive frames move further than this on the sky
	// (0 → the measured default in internal/panelgroup).
	PanoGroupStepDeg float64
	// PanoBandMaskLatDeg is the galactic latitude inside which canvas pixels count as BAND rather
	// than background, for both the flattening and the colour. The band is the subject: measuring
	// the sky's own level and colour anywhere inside it neutralises the Milky Way to grey.
	PanoBandMaskLatDeg float64
	// PanoBackground removes the residual sky dome from the assembled canvas with a low-order
	// polynomial fitted OUTSIDE the band. Off keeps the canvas photometrically raw.
	PanoBackground bool

	// PanoForeground composites the landscape under the alt-az arch, taken from whichever panel was
	// aimed lowest. It applies to the "altaz" canvas only — the other projections have no horizon to
	// stand it on. Off by default.
	PanoForeground bool

	// FlatRadialOnly reduces a master flat to the smooth lens falloff fitted to it. See
	// nightscape.Options.FlatRadialOnly — it is what makes a phone flat usable.
	FlatRadialOnly bool

	// KeepMeteors searches the registered frames for streaks and blends the confident meteors back
	// into the linear sky, dropping satellites and aircraft. Applies to the milkyway and nightpano
	// recipes, which are the ones that sigma-clip a fixed field across many frames and so are the ones
	// that delete meteors by design.
	KeepMeteors bool

	// CoverageCrop crops the COLOUR-COMBINE inputs (never the persisted channel masters) to the
	// largest rectangle every channel covers at CoverageMinFrac of its stacked depth. Multi-night
	// merges leave each channel covering a different union of rotated footprints; combining them
	// yields regional colour casts and black wedges (task #354). Falls back to the full field with
	// a warning when the common rectangle collapses below ~35% of the canvas. Grouped runs only —
	// single-session channels carry no coverage grid, so this is inert there.
	CoverageCrop bool
	// CoverageMinFrac is the per-cell depth threshold: covered = reached by at least this fraction
	// of the channel's stacked frames (min 1).
	CoverageMinFrac float64

	// EdgeCrop trims the ragged edge off the COLOUR-COMBINE inputs (never the persisted masters):
	// the dead wedge a drifting session leaves plus the band beside it where the stack's sky sits
	// off its own level. Unlike CoverageCrop this is measured from the finished stack's pixels, so
	// it needs no registration geometry and works on a single-session run — which is exactly where
	// it was found: a 135 px drift left a 200 px skirt only 0.2% above the sky, which is +46σ to a
	// background model, and the sky fit that followed blew a quarter of the frame to white.
	EdgeCrop bool

	// CoreSatMask repairs sensor-saturated galaxy/star cores before a MULTI-NIGHT stack: pixels at
	// a group's post-normalization saturation ceiling are replaced from the sub-ceiling median of
	// the nights that still see the true value (transient/satmask.go). A clipped plateau shared by
	// several nights survives Siril's rejection (it is coherent, not an outlier) and flattens the
	// stacked core — the "burned centre". Multi-group channels only; a pixel clipped in EVERY night
	// is left untouched (no true value exists to restore).
	CoreSatMask bool

	// DropFilterWheelTransition drops the first frame of a run when its brightness is off (the
	// wheel was still moving). Conditional — only off-brightness frames are dropped.
	DropFilterWheelTransition bool

	// Register2Pass registers a single-session channel with Siril's TWO-PASS form instead of the
	// one-pass default. One-pass leaves the reference at frame 1, which is right only by luck: a
	// session that crosses the meridian puts frame 1 on the minority side of a 180° flip, and every
	// later frame then has to match a rotated reference that may also be the worst frame of the
	// night. Two-pass measures every frame first and picks the reference from those star counts and
	// FWHM. Off by default because it moves the output pixel grid (a different reference frames the
	// stack differently), which every byte-pinned run depends on. The multi-group/cross-session path
	// always registers in two passes and is unaffected by this knob.
	Register2Pass bool

	// Optional astro-AI host tools (used only when the binary is installed; otherwise skipped).
	// BackgroundAI runs GraXpert background extraction on the linear masters instead of Siril's
	// polynomial subsky. StarReduce > 0 runs StarNet++ in the finish and screens the stars back at
	// this opacity (0 = keep full stars, e.g. 0.5 = halved star intensity).
	BackgroundAI bool
	// CombinedBackgroundAI runs a second GraXpert background-extraction pass on the COMBINED linear RGB
	// (after per-channel extraction + rgbcomp, before SPCC) to remove the residual large-scale colour
	// gradient (amp-glow + light-pollution) that survives the combine. Falls back to an RBF subsky when
	// GraXpert is absent. This is what makes the whole sky homogeneous.
	CombinedBackgroundAI bool
	// ColorDenoiseAI runs a GraXpert AI *denoise* pass on the combined linear RGB (before SPCC) to cut
	// the heavy chrominance noise of thin colour subs. Being edge-preserving, it cleans the colour
	// without smearing star halos — unlike a gaussian ChromaBlur, which can then be dropped to 0.
	ColorDenoiseAI bool
	StarReduce     float64

	// ColorCalibration attempts plate-solve + SPCC for natural color + a neutral background,
	// falling back to background neutralization. LinkedStretch keeps that neutral balance.
	ColorCalibration bool
	LinkedStretch    bool

	// BackgroundLevel is the target sky-background brightness for the finishing autostretch
	// (Siril `autostretch [-linked] <shadowsclip> <targetbg>`), in (0,0.5]. Siril's bare-command
	// default of 0.25 lifts the sky to a bright grey that reads as a washed brown haze; deep-sky
	// finishing wants a dark sky (~0.06). 0 → engine default (0.06). See siril.AutostretchCmd.
	BackgroundLevel float64
	// OIIIScreen is the [OIII] layer's screen opacity when the natural family composites it as a TEAL
	// emission layer beside the red Ha screen (broadband runs with a real B filter only — without B
	// the OIII master already feeds the blue base). 0 (default) → off, byte-identical to before the
	// knob: the user opts in per run to light up shock-front data the RGB base under-shows.
	OIIIScreen float64
	// OIIIBlackPoint is the OIII layer's black-point clip before it is teal-screened (as HaBlackPoint).
	OIIIBlackPoint float64
	// SIIScreen is the [SII] layer's screen opacity — the third emission twin, composited beside the
	// red Ha and teal OIII screens on the natural family. 0 (default) → off, byte-identical to before
	// the knob, exactly like OIIIScreen: the user opts in per run.
	//
	// [SII] 671.6/673.1 nm is DEEPER red than Hα, so it cannot be made "more red" than the Ha screen
	// in sRGB — SIITint chooses how it is distinguished instead.
	SIIScreen float64
	// SIIBlackPoint is the SII layer's black-point clip before it is screened (as HaBlackPoint).
	SIIBlackPoint float64
	// SIITint selects the SII layer's colour: SIITintDeepRed (default) renders it as a crimson that
	// reads as "deeper than Hα", SIITintGold as the amber accent the Hubble palette conditions people
	// to expect from sulphur. Purely a look choice — the data is identical either way.
	SIITint string
	// HaBlackPoint clips the Ha layer's background to black before it is red-screened into the
	// composite (GIMP levels low-input, [0,1]). Without it the Ha background pedestal screens a red
	// wash over the whole frame (the brown sky). ~0.12 zeroes the background while keeping bright HII
	// knots. 0 → no clip (legacy behavior). Only meaningful when HaScreen > 0.
	HaBlackPoint float64
	// HaRBF flattens the Ha layer with Siril's RBF subsky instead of the gentle degree-limited
	// polynomial before it is red-screened. A residual ASYMMETRIC gradient (amp-glow, low horizon
	// glow) in the Ha layer becomes a red blotch across half the frame after the screen — an RBF
	// model removes it where a degree-1 plane cannot. Default true for the Ha-compositing modes.
	HaRBF bool
	// CometPerFrameStarnet de-stars EVERY comet-aligned frame before the comet stack (comet mode
	// only) — the cleanest comet layer possible, zero trail residuals, at the cost of one StarNet
	// pass per frame (minutes on a long session). Off by default; the asymmetric comet-stack
	// rejection already removes the trails on the normal path.
	CometPerFrameStarnet bool

	// ChromaBlur denoises colour in the GIMP LRGB finish: it blurs the (thin, noisy) RGB base this
	// many px while the L luminance keeps all detail — erasing the "pink" chroma noise of short colour
	// subs with no loss of sharpness. 0 → none (mono/OSC, where there is no separate luminance).
	ChromaBlur float64
	// ChromaSmoothPx smooths ONLY the colour of the combined linear RGB (after the joint GraXpert
	// denoise + background equalization, before SPCC) this many px, preserving the per-pixel RGB mean
	// exactly: m=(R+G+B)/3, c'=m+blend(c−m). The blur is GAUSSIAN-shaped (three box passes, σ≈px/√3 —
	// the old single box pass painted a literal square of smeared colour around every very bright
	// star) and STAR-PROTECTED: residuals are winsorized to the chroma noise scale, high-SNR pixels
	// keep their own chroma, and a dilated near-saturation core mask exempts star cores + wings. It
	// flattens the coherent 10–30 px colour PATCHES that survive the joint denoise (which a stretch +
	// saturation then amplify into red/blue blotches) without touching luminance — the L layer
	// supplies detail in LRGB, and the mean is byte-for-byte unchanged. 0 → none. See
	// internal/pipeline/chromasmooth.go.
	ChromaSmoothPx int
	// ChromaBgSmoothPx adds a second, much coarser mean-preserving chroma pass restricted to the sky
	// background (luminance under ~6σ above sky, smoothstep-feathered): it flattens the large
	// green/brown chroma mottle and residual walking-noise colour that survive the joint denoise at
	// scales the fine pass cannot reach, while galaxies/nebulae/stars (bright pixels) keep their
	// colour untouched. Luminance is byte-for-byte unchanged (same identity as ChromaSmoothPx).
	// 0 → off. See internal/pipeline/chromasmooth.go.
	ChromaBgSmoothPx int
	// LumBoost gently lifts the L luminance curve's midtones (the value is the peak lift at
	// mid-grey; sky-level points pinned by a shadow anchor, core/star points by a highlight
	// anchor) — "a brighter galaxy periphery" without touching sky level, core detail or colour
	// balance. Folded into the LumCurve spline's control points at compose time. 0 → off.
	LumBoost float64
	// SkyLumFlattenPx equalizes the sky's large-scale BRIGHTNESS on the stretched layers (grid
	// pitch in px) — the luminance twin of SkyChromaFlattenPx. The linear background passes leave
	// a ~1% sky-level residual (a stray-light glow's shape exceeds a degree-1 plane) that the
	// stretch amplifies into a grey-on-one-side sky; this fits a robust quadratic surface to the
	// tile sky levels (objects rejected from the fit) and equalizes the whole sky to ONE level —
	// the darkest genuine (glow-free) sky: glow comes down, a beyond-glow corner comes up — on
	// the colour base, the L luminance layer and the mono outputs. The shift is locally uniform,
	// so objects keep their contrast. 0 → off (default for nebula: faint emission wings read as
	// sky level there). See internal/pipeline/skylum.go.
	SkyLumFlattenPx int
	// SkyChromaFlattenPx neutralizes the sky's large-scale chroma on the STRETCHED RGB base, just
	// before the GIMP composite (grid pitch in px). The stretch amplifies sub-percent linear
	// background chroma residuals (RBF ringing around an edge artifact, denoise mottle) into visible
	// left/right colour bands and coloured discs no linear pass can reliably prevent; this measures
	// the sky chroma per tile (sky pixels only), smooths it, and subtracts a zero-sum field with
	// SNR-feathered protection — sky turns neutral, objects keep their colour, luminance is
	// untouched. Skipped for narrowband palettes. 0 → off. See internal/pipeline/skychroma.go.
	SkyChromaFlattenPx int
	// CropFrac trims this fraction off each edge of the exported image to drop ragged stacking-edge
	// bands (dithered frame borders); the layered .xcf keeps the full frame. 0 → no crop.
	CropFrac float64
	// TrailMaskK enables cross-frame transient masking before each channel is stacked: across the
	// REGISTERED subs, any pixel above its per-pixel median + k·MADσ (a satellite/plane trail segment,
	// cosmic ray or hot pixel — present in only one sub at that sky position) is replaced by the median.
	// It cleans a slow satellite that lands in many subs at marching positions, which neither frame
	// rejection nor a normal stack sigma-clip removes, with no global SNR cost. ~3.0 is a good default;
	// lower is more aggressive. 0 → disabled. See internal/transient.
	TrailMaskK float64
	// HaExcludeStars screens Ha onto extended nebulosity only (point-like stars median-filtered out),
	// instead of over everything. The default (false) applies Ha to the whole frame.
	HaExcludeStars bool
	// HaContinuumSub subtracts the scaled broadband continuum (k·R, or k·L when no R) from the linear
	// Ha master before its stretch, so the red screen shows only true Ha EMISSION: stars, the galaxy
	// disc and the sky pedestal cancel in the subtraction, letting the stretch lift faint HII
	// filaments the black-point clip used to erase. k is the median Ha/ref ratio over continuum-bright
	// pixels; soft-fails to screening the full Ha layer. See internal/pipeline/hacontinuum.go.
	HaContinuumSub bool

	// Previews emits per-channel and final preview PNGs for the UI.
	Previews bool

	// EmitLuminanceMono also saves a processed monochrome image of just the L channel next to the
	// colour final (calibrated + denoised + background-extracted + stretched — the same treatment L
	// gets inside the LRGB composite). Default true for deepsky/nebula. See internal/pipeline mono.go.
	EmitLuminanceMono bool
	// EmitAllChannelMono also saves one monochrome image integrating every co-registered channel
	// master (L/R/G/B/Ha…) into a synthetic luminance, weighted by each channel's sub-count, for
	// maximum signal. Default false (opt-in). See internal/pipeline mono.go.
	EmitAllChannelMono bool

	// Supervise enables the optional local-AI-agent finish: when on (set by the run request /
	// --supervise) and a host model server is reachable, the GIMP composite is re-rendered a few
	// times, each judged by a local vision model, keeping the best. Off in every mode by default.
	// SuperviseMaxIters bounds the loop (0 → engine default). See internal/pipeline supervise.go.
	Supervise         bool
	SuperviseMaxIters int
	// SuperviseTier caps how far the agent may reach when it re-processes between iterations:
	// "A" = GIMP composite only, "B" = also the linear finish prep, "C"/"" = also re-stack from the
	// raw frames (full autonomy). Empty → full. Tier C additionally needs raw frames (Options.Reprocess).
	SuperviseTier string
	// SuperviseTargetScore is the deterministic-score floor (0..10) the render must clear before the
	// agent may declare itself done (0 → the engine default, 7.0). Raising it makes the loop keep
	// pushing; it is also the series continue/stop threshold.
	SuperviseTargetScore float64
	// SuperviseConfirmRestack asks the user before a Tier-C re-stack (the old default). The default
	// is now FALSE — the loop runs autonomously within its per-tier budgets, per the product
	// decision "autonomous with caps, no mid-run confirmations".
	SuperviseConfirmRestack bool

	// Palette selects the channel→RGB colour mapping for the deep-sky finish: "" / "natural" (broadband
	// LRGB + Hα screen + SPCC), "hargb", or the narrowband palettes "hoo" / "sho" / "hos" / "foraxx"
	// (which need OIII/SII and disable the Hα screen + SPCC), "mono". A palette missing its required
	// filters falls back (see internal/pipeline resolvePalette). Empty → natural. Deepsky/nebula only.
	Palette string

	// Nightscape (milkyway) controls. Look selects the render style (natural/iphone/deepsky);
	// ForegroundFrame optionally overrides the auto-picked clean foreground source (a raw frame path);
	// Orientation is the final display transform (auto|none|cw|ccw|180, optionally +"-flip").
	Look            string
	ForegroundFrame string
	Orientation     string

	// Comet controls (comet mode). When all four are > 0 they override auto-detection with the comet's
	// pixel position in the first (X1,Y1) and last (X2,Y2) star-aligned frame; otherwise the comet is
	// auto-centroided. CometX1 etc. are registered-frame pixel coordinates.
	CometX1, CometY1, CometX2, CometY2 float64
}

// WantsVideo reports whether a video artifact should be produced.
func (f Format) WantsVideo() bool { return f == FormatVideo || f == FormatBoth }

// WantsImage reports whether a still image should be produced (always, except pure video).
func (f Format) WantsImage() bool { return f != FormatVideo }

// ParseMode validates a mode string.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(s)) {
	case Deepsky, Nebula, Milkyway, Planetary, Livestack, Comet, Mosaic, Sun, Nightpano, Eclipse:
		return Mode(strings.ToLower(s)), nil
	default:
		return "", fmt.Errorf("unknown mode %q (want: deepsky, nebula, milkyway, planetary, livestack, comet, mosaic, sun, nightpano, eclipse)", s)
	}
}

// ParseFormat validates a format string.
func ParseFormat(s string) (Format, error) {
	switch Format(strings.ToLower(s)) {
	case FormatImage, FormatVideo, FormatBoth:
		return Format(strings.ToLower(s)), nil
	default:
		return "", fmt.Errorf("unknown format %q (want: image, video, both)", s)
	}
}

// For returns the preset for a mode.
func For(m Mode) Preset {
	p := presetFor(m)
	// The stacking recipes are shared by every mode, so the per-mode literals below stay focused on
	// their own grade/look tuning. Filling them here (rather than in each literal) also means a mode
	// that never stacks light frames still carries a valid recipe if an OSC path reaches one.
	if p.Stack == (stackalg.Options{}) {
		p.Stack = stackalg.DefaultLights()
	}
	if p.StackComet == (stackalg.Options{}) {
		p.StackComet = stackalg.DefaultComet()
	}
	if p.Masters == (stackalg.MasterOptions{}) {
		p.Masters = stackalg.DefaultMasters()
	}
	return p
}

// presetFor is the per-mode tuning table; For wraps it with the shared stacking defaults.
func presetFor(m Mode) Preset {
	switch m {
	case Mosaic:
		// Tiled-panel mosaic: every panel stacks with the full deepsky tuning; the assembler owns
		// placement and edges, so the combine-time crops and the union-canvas knob (same-pointing
		// machinery, guarded against offset panels) are forced off.
		p := For(Deepsky)
		p.Mode = Mosaic
		p.Mosaic = false
		p.CoverageCrop = false
		p.CropFrac = 0
		p.SeamNoiseEq = true
		p.MosaicOverlapExpected = 0.20
		p.MosaicFeatherFrac = 0.6
		p.MosaicPhotomMatch = "gain_offset"
		p.MosaicCanvasCrop = "common"
		p.MosaicMinPanelFrames = 3
		p.MosaicPanelSource = "auto"
		return p
	case Comet:
		// Comet mode reuses the deepsky LRGB tuning (it runs the channel pipeline twice) but enables
		// StarNet so the star layer can be lifted. The dual star/comet stacking + recomposite is handled
		// by pipeline.ProcessComet, not the preset. The optional supervised finish re-tunes the comet
		// colour composite (background/saturation) — see internal/pipeline/supervise_comet.go.
		p := For(Deepsky)
		p.Mode = Comet
		p.StarReduce = 0.5 // ensure StarNet is wired (used to separate the star layer)
		p.Saturation = 0   // no satu on the comet composite by default; the supervisor may raise it
		return p
	case Nebula:
		return Preset{
			Mode:             Nebula,
			Color:            Mono,
			Grade:            grade.Options{RoundnessFloor: 0.50, RoundnessSigma: 3.0, FWHMSigma: 3.0, BackgroundSigma: 3.0, StarCountFrac: 0.4, RejectTrails: true},
			BackgroundDegree: 2,
			HaScreen:         0.50, // trimmed from 0.60: less global red push (warmth); HaExcludeStars keeps it off stars
			Saturation:       0.10,
			Curve:            []float64{0, 0, 0.20, 0.27, 0.5, 0.58, 0.8, 0.85, 1, 1}, // lift faint nebulosity

			DenoiseChroma: 0.85, DenoiseLum: 0.30, DenoiseVST: true, DenoiseDA3D: true,
			DenoiseStarlet: false, DenoiseAuto: false, // proven Siril denoise; the Go starlet over-cleaned real masters (σ÷7, unnatural texture) — validate before re-enabling
			StackWeight:               "wfwhm", // weight subs by star sharpness (was unweighted for single-session)
			PhotomNorm:                true,    // cross-session normalization (flat-curve meta seed + widened clamp fixed the old "Ha clamped at 5×" mis-measure; multi-group channels only)
			FlattenBg:                 true,    // per-frame degree-1 gradient flatten before a multi-night merge (kills footprint-boundary seams; multi-group channels only)
			SeamOffsetRefit:           true,    // overlap-fitted pedestal refit per night (kills the residual background steps at footprint boundaries; multi-group channels only)
			SeamNoiseEq:               true,    // coverage-weighted starlet fade of the noise-depth step at coverage boundaries (multi-group channels only)
			CoverageCrop:              true,    // crop the colour combine to the cross-channel common covered field (multi-night wedges/casts; masters untouched)
			CoverageMinFrac:           0.30,    // a cell counts as covered at ≥30% of the channel's stacked depth
			EdgeCrop:                  true,    // trim the ragged stacking edge off the combine inputs (drift wedge + the skirt beside it; masters untouched)
			CoreSatMask:               true,    // repair sensor-saturated cores from unsaturated nights before the multi-night stack
			BackgroundAI:              true,    // per-channel GraXpert background extraction
			CombinedBackgroundAI:      true,    // parity with deepsky: 2nd GraXpert pass on combined RGB → homogeneous sky
			ColorDenoiseAI:            true,    // parity with deepsky: GraXpert AI denoise on combined colour
			ChromaSmoothPx:            6,       // parity with deepsky: mean-preserving chroma smooth on the combined RGB → flattens residual colour patches
			ChromaBgSmoothPx:          24,      // parity with deepsky: coarse background-only chroma pass → flattens the large sky colour mottle
			SkyChromaFlattenPx:        32,      // parity with deepsky: post-stretch sky-chroma neutralization → no banded/discy sky after the stretch amplifies linear residuals
			StarReduce:                0.5,     // emission nebulae benefit most from star reduction
			TrailMaskK:                3.0,     // cross-frame transient mask: clean satellite trails / cosmic rays pre-stack
			CoreHighlightKnee:         0.64,    // roll off the L luminance core highlights → structured pink knot
			CoreHighlightCeil:         0.76,
			HighlightKnee:             0.85, // star-safe highlight cap: bright star cores stay coloured, never burn white
			HighlightCeil:             0.92, // roll cores a touch further below white (was 0.96) so they read coloured, not near-white
			StretchHeadroom:           0.90, // cap linear highlights ≤0.90 before the autostretch so star cores keep colour (no hard clip to white)
			AutoFixStars:              true, // gated post-finish repair of any residual burnt/colour-flattened stars (zero cost when clean)
			HaExcludeStars:            true, // median-remove stars before the red Ha screen → no orange/pink star tint
			HaContinuumSub:            true, // screen the emission-only excess (Ha − k·R): faint HII survives the clip
			DropFilterWheelTransition: true,
			ColorCalibration:          true,
			LinkedStretch:             true,
			BackgroundLevel:           0.09, // a touch brighter than deepsky to keep faint nebulosity visible
			LumOpacity:                1.0,  // L composites at full opacity by default (the UI can lower it)
			HaBlackPoint:              0.05, // the excess layer is emission-only — clip just the stretched pedestal (was 0.07 for the raw layer)
			HaRBF:                     true, // RBF-flatten the Ha layer so its screen can't paint a red gradient
			Previews:                  true,
			EmitLuminanceMono:         true, // also save a standalone processed L mono next to the colour final
		}
	case Milkyway:
		return Preset{
			Mode:             Milkyway,
			Color:            OSC,
			Grade:            grade.Options{RoundnessFloor: 0.45, RoundnessSigma: 3.5, FWHMSigma: 3.5, BackgroundSigma: 3.5, StarCountFrac: 0.3, RejectTrails: false},
			BackgroundDegree: 3, // strong light-pollution gradients from a phone
			// For milkyway, Saturation is a SCALE on the chosen Look's own saturation (1 = as the look
			// was designed) — the nightscape grade owns the absolute value; this knob tames/boosts it.
			Saturation: 1.0,
			Curve:      []float64{0, 0, 0.3, 0.30, 0.6, 0.62, 1, 1}, // near-linear, preserve star colors

			DenoiseChroma: 0.60, DenoiseVST: true, DenoiseDA3D: true,
			BackgroundAI:     true, // strong phone light-pollution gradients
			ColorCalibration: true,
			LinkedStretch:    true,
			// Auto-levels target sky-background ("Balanced") for the nightscape composite; the UI/CLI
			// brightness control overrides it (Darker 0.035 / Balanced 0.05 / Brighter 0.07). The dedicated
			// recipe reads this via nightscape.Options.Brightness (data-driven auto-stretch). After the v3
			// per-channel black-clip the sky is genuinely dark, so the targets are lower than v2's.
			BackgroundLevel: 0.05,
			Previews:        true,
			Look:            "natural", // dedicated nightscape recipe: foreground composite + faithful grade
			Orientation:     "exif",
		}
	case Nightpano:
		// Every panel is stacked by the milkyway recipe, so the panel-level knobs must match it
		// exactly — a panorama is that recipe run N times and then assembled.
		p := For(Milkyway)
		p.Mode = Nightpano
		// The canvas owns background extraction, and it must run ONCE over the whole arch: the Milky
		// Way band IS the large-scale gradient of a 57-by-72-degree panel, so flattening a panel
		// against its own background subtracts the subject. See internal/skypano/flatten.go.
		p.BackgroundAI = false
		p.BackgroundDegree = 0
		// The panels are placed by our own solver (internal/skypano), which Siril's cannot do at this
		// field width, and the sky is colour-balanced on the assembled canvas instead.
		p.ColorCalibration = false
		p.PanoProjection = "both"
		p.PanoScaleDegPerPix = 0.03
		p.PanoBandMaskLatDeg = 20
		p.PanoBackground = true
		return p
	case Planetary:
		return Preset{
			Mode: Planetary,
			// DoubleStack is NOT the reverted translation-only synthetic reference (which stacked
			// softer): pass 1 is the full two-level AP-warped, canonical-geometry, per-AP-selected
			// stack, and pass 2 re-registers the originals onto that master with a dense
			// per-AP-seeded grid (AutoStakkert's double-stack reference); cross-channel
			// co-registration uses the same dense field. DrizzleScale 1.5 stacks onto a finer
			// grid by default — hundreds of sub-pixel-dithered frames genuinely add resolution
			// there (drizzle_scale 1 returns to native).
			Planetary: planetary.Options{
				BestPercent: 15, Sharpen: true, APAlign: true, APWeights: true, DoubleStack: true,
				Calibrate: true, DrizzleScale: 1.5, Mosaic: true,
				Formats: []string{"png", "tif"}, Finish: planetary.DefaultFinish(),
			},
			Curve:    []float64{0, 0, 0.5, 0.52, 1, 1},
			Previews: true, // lucky-imaging sharpens; no denoise/color-cal
		}
	case Sun:
		// Solar imaging shares lucky imaging's shape and almost none of its tuning. Registration is
		// geometric — the fitted limb gives scale, centre and (with the annulus correlation) rotation
		// — so none of the star- or feature-matching knobs apply. The frame keep rate is far higher
		// than a planetary run's because a 40 mm aperture is diffraction-limited rather than
		// seeing-limited: frame-to-frame variation is small and the stack is SNR-limited, so
		// discarding most frames would cost more noise than it buys sharpness.
		return Preset{
			Mode:     Sun,
			Sun:      solar.DefaultPreset(),
			Previews: true,
		}
	case Eclipse:
		// Derived from Sun, because it IS the solar recipe — only the geometry differs. Every override
		// below names a measured reason.
		p := For(Sun)
		p.Mode = Eclipse
		s := &p.Sun
		// The two-body fit, which is the whole point: one circle handed a crescent's boundary points
		// converges on a blend of the two bodies and flips between them frame to frame.
		s.TwoBody = true
		// The Moon travels 0.508"/s against the Sun — 9.8 px/min at the 3.1"/px these captures run at,
		// so a sixty-second window smears the occulter's edge by ten pixels against a point spread
		// function of about two. Thirty seconds is the compromise: still 900 frames at 30 fps, and the
		// swept band the stack has to exclude stays around five pixels.
		s.WindowSeconds = 30
		// A window is only worth stacking if its frames are allowed to reach it. The solar defaults cap
		// a window at 150 frames, which at 30 fps is five seconds — it would split every window into
		// six and stack a fraction of what was shot.
		s.WindowFrames = 1200
		s.MaxFrames = 1200
		// Keep far more of the clip than a solar run does. At this aperture the limit is diffraction,
		// not seeing, so frames vary little and the stack is SNR-limited; the crescent is a small part
		// of the frame and needs every photon it can get.
		s.KeepPercent = 70
		// The capture is undersampled by roughly two and a half times — 3.1"/px against a diffraction
		// FWHM near 2.3" — and the disc wanders hundreds of pixels across a clip, so there is ample
		// sub-pixel dither for drizzle to work with.
		s.Drizzle = 1.5
		// Rendered in the colour the recording had, measured off the clip, rather than in a chosen
		// palette. It is the one thing about an eclipse that is genuinely photographic: the crescent
		// through an Hα etalon has a colour the phone recorded, and inventing a different one throws
		// that away. Falls back to gold when there is nothing to measure.
		s.Finish.Palette = solar.PaletteNative
		// Hand back the sharpest individual frames as well as the stack. On this capture the stack is
		// measurably blurrier than its own inputs, so the best single frame is a real candidate for
		// the best picture and the run should not make that choice silently.
		s.BestFrames = 12
		s.BestFrameGapSeconds = 20
		// The alignment-point field is OFF, measured rather than assumed. It is meant to correct
		// atmospheric distortion across the disc, and on a crescent most of the disc is occulter — so
		// most of its nodes correlate the Moon against the Moon and the field it fits is then applied
		// to the Sun. On 160 real frames it cost 2%: solar limb sigma 3.11 without it, 3.17 with. The
		// full-disc solar path had already measured it making the limb worse; a crescent only sharpens
		// the case.
		s.APAlign = false
		return p
	case Livestack:
		// Live stacking finalizes through the standard deep-sky path; the per-batch live preview reads
		// only the grade thresholds. Reuse the deepsky preset verbatim, just retagging the mode.
		p := For(Deepsky)
		p.Mode = Livestack
		return p
	default: // Deepsky
		return Preset{
			Mode:             Deepsky,
			Color:            Mono,
			Grade:            grade.DefaultOptions(),
			BackgroundDegree: 1,
			HaScreen:         0.42, // a touch brighter so HII regions read red (HaBlackPoint keeps it off the sky/stars)
			Saturation:       0.12, // gentler than 0.16, which over-saturated stars (too orange); colour still reads natural
			// Gentle value curve: with the background already flat (combined GraXpert) + neutral (SPCC), a
			// strong value curve would only re-amplify residual colour. Brightness/contrast for the galaxy
			// comes from LumCurve (the L luminance) instead — so the sky stays homogeneous, no banding.
			Curve: []float64{0, 0, 0.25, 0.24, 0.75, 0.78, 1, 1},
			// LumCurve lifts the galaxy from the clean L luminance (sky ~0.044, galaxy ~0.08 after the 0.06
			// autostretch): pull deep sky to near-black, lift the 0.05–0.30 band so the galaxy stands out,
			// and roll off highlights so star cores aren't clipped (natural, faded halos).
			LumCurve:   []float64{0, 0, 0.04, 0.025, 0.08, 0.20, 0.15, 0.40, 0.28, 0.58, 0.5, 0.75, 0.8, 0.92, 1, 1},
			LumOpacity: 1.0, // L composites at full opacity by default; the UI can lower it for a softer LRGB blend

			// Denoise the linear masters: luminance gently (preserve galaxy detail) and chroma hard (the
			// thin R/G/B sub-stacks are very noisy). VST suits the photon-limited linear data. ChromaBlur
			// then smooths residual colour noise in the finish — kept modest (6) so it doesn't smear
			// colour into star halos.
			DenoiseChroma: 0.85, DenoiseLum: 0.50, DenoiseVST: true, DenoiseDA3D: true,
			DenoiseStarlet: false, DenoiseAuto: false, // proven Siril denoise; the Go starlet over-cleaned real masters (σ÷7, unnatural texture) — validate before re-enabling
			StackWeight:               "wfwhm", // weight subs by star sharpness (was unweighted for single-session)
			PhotomNorm:                true,    // cross-session normalization (flat-curve meta seed + widened clamp fixed the old "Ha clamped at 5×" mis-measure; multi-group channels only)
			FlattenBg:                 true,    // per-frame degree-1 gradient flatten before a multi-night merge (kills footprint-boundary seams; multi-group channels only)
			SeamOffsetRefit:           true,    // overlap-fitted pedestal refit per night (kills the residual background steps at footprint boundaries; multi-group channels only)
			SeamNoiseEq:               true,    // coverage-weighted starlet fade of the noise-depth step at coverage boundaries (multi-group channels only)
			CoverageCrop:              true,    // crop the colour combine to the cross-channel common covered field (multi-night wedges/casts; masters untouched)
			CoverageMinFrac:           0.30,    // a cell counts as covered at ≥30% of the channel's stacked depth
			EdgeCrop:                  true,    // trim the ragged stacking edge off the combine inputs (drift wedge + the skirt beside it; masters untouched)
			CoreSatMask:               true,    // repair sensor-saturated cores from unsaturated nights before the multi-night stack
			ChromaBlur:                0,       // 0: GraXpert AI denoise handles colour noise; no blur → crisp star halos
			ChromaSmoothPx:            6,       // mean-preserving chroma smooth on the combined RGB → flattens residual colour patches (no luma cost)
			ChromaBgSmoothPx:          24,      // coarse background-only chroma pass → flattens the large sky colour mottle (objects/stars keep colour)
			SkyChromaFlattenPx:        32,      // post-stretch sky-chroma neutralization → no banded/discy sky after the stretch amplifies linear residuals
			SkyLumFlattenPx:           64,      // post-stretch sky-level equalization → no grey-on-one-side sky (galaxy fields; nebula keeps 0 to protect faint wings)
			CropFrac:                  0.035,   // trim ragged stacking-edge bands off the export
			TrailMaskK:                3.0,     // cross-frame transient mask: clean satellite trails / cosmic rays pre-stack
			CoreHighlightKnee:         0.64,    // roll off the L luminance above this so the blown nebula core dims
			CoreHighlightCeil:         0.76,    // ...to this asymptote → structured pink knot, outer tones untouched
			HighlightKnee:             0.85,    // star-safe highlight cap: bright star cores stay coloured, never burn white
			HighlightCeil:             0.92,    // roll cores a touch further below white (was 0.96) so they read coloured, not near-white
			StretchHeadroom:           0.90,    // cap linear highlights ≤0.90 before the autostretch so star cores keep colour (no hard clip to white)
			AutoFixStars:              true,    // gated post-finish repair of any residual burnt/colour-flattened stars (zero cost when clean)
			BackgroundAI:              true,    // per-channel GraXpert background extraction
			CombinedBackgroundAI:      true,    // 2nd GraXpert pass on the combined RGB + RBF subsky → homogeneous sky
			ColorDenoiseAI:            true,    // GraXpert AI denoise on the combined colour → no RGB chroma speckle
			HaExcludeStars:            true,    // Ha on the galaxy/nebulosity only (stars median-removed)
			HaContinuumSub:            true,    // screen the emission-only excess (Ha − k·R): faint HII survives the clip
			DropFilterWheelTransition: true,
			ColorCalibration:          true,
			LinkedStretch:             true,
			BackgroundLevel:           0.06, // dark, natural sky (vs Siril's washed-out 0.25 default)
			HaBlackPoint:              0.06, // the excess layer is emission-only — clip just the stretched pedestal (was 0.12 for the raw layer)
			HaRBF:                     true, // RBF-flatten the Ha layer so its screen can't paint a red gradient
			Previews:                  true,
			EmitLuminanceMono:         true, // also save a standalone processed L mono next to the colour final
		}
	}
}
