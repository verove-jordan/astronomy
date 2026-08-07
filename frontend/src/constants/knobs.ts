// The editable per-stage knob catalog for the "restart from a step with edited params" feature. It
// mirrors the Go whitelist + clamps (internal/pipeline/supervise_params.go) so the processing view shows
// the right knobs for each timeline stage and validates ranges before sending. Each stage maps to the
// re-entry tier it belongs to (A composite / B linear prep / C re-stack); editing a knob re-runs from
// that stage onward. `def` is a display hint for a knob the run did not override — it is never sent
// unless the user actually changes it (the editor sends only touched knobs, so an approximate hint can
// never silently reset a tuned value).

export type KnobKind = "number" | "toggle" | "select";

export interface KnobDef {
  key: string; // the JSON knob name (matches the Go whitelist)
  labelKey: string; // i18n key for the label
  kind: KnobKind;
  def: number | boolean | string; // display default when the run did not override it
  min?: number;
  max?: number;
  step?: number;
  options?: string[]; // for kind "select": the enum values (label via `${labelKey}Options.<value>`)
}

// The three re-entry tiers, keyed by their canonical stage group.
export type TierGroup = "final" | "combined" | "stacked";

// Tier A — GIMP composite (re-renders in seconds): the Final / composite milestones.
const COMPOSITE_KNOBS: KnobDef[] = [
  {
    key: "lum_opacity",
    labelKey: "rerun.knobs.lum_opacity",
    kind: "number",
    def: 1,
    min: 0,
    max: 1,
    step: 0.05,
  },
  {
    key: "lum_boost",
    labelKey: "rerun.knobs.lum_boost",
    kind: "number",
    def: 0,
    min: 0,
    max: 0.25,
    step: 0.02,
  },
  {
    key: "saturation",
    labelKey: "rerun.knobs.saturation",
    kind: "number",
    def: 0.12,
    min: 0,
    max: 0.35,
    step: 0.01,
  },
  {
    key: "star_desat",
    labelKey: "rerun.knobs.star_desat",
    kind: "number",
    def: 0,
    min: 0,
    max: 1,
    step: 0.05,
  },
  {
    key: "ha_screen",
    labelKey: "rerun.knobs.ha_screen",
    kind: "number",
    def: 0.42,
    min: 0,
    max: 0.8,
    step: 0.02,
  },
  {
    key: "ha_black_point",
    labelKey: "rerun.knobs.ha_black_point",
    kind: "number",
    def: 0.12,
    min: 0,
    max: 0.3,
    step: 0.01,
  },
  {
    key: "oiii_screen",
    labelKey: "rerun.knobs.oiii_screen",
    kind: "number",
    def: 0,
    min: 0,
    max: 0.8,
    step: 0.02,
  },
  {
    key: "oiii_black_point",
    labelKey: "rerun.knobs.oiii_black_point",
    kind: "number",
    def: 0.06,
    min: 0,
    max: 0.3,
    step: 0.01,
  },
  {
    key: "sii_screen",
    labelKey: "rerun.knobs.sii_screen",
    kind: "number",
    def: 0,
    min: 0,
    max: 0.8,
    step: 0.02,
  },
  {
    key: "sii_black_point",
    labelKey: "rerun.knobs.sii_black_point",
    kind: "number",
    def: 0.06,
    min: 0,
    max: 0.3,
    step: 0.01,
  },
  {
    key: "sii_tint",
    labelKey: "rerun.knobs.sii_tint",
    kind: "select",
    def: "deep_red",
    options: ["deep_red", "gold"],
  },
  {
    key: "chroma_blur",
    labelKey: "rerun.knobs.chroma_blur",
    kind: "number",
    def: 0,
    min: 0,
    max: 12,
    step: 0.5,
  },
  {
    key: "crop_frac",
    labelKey: "rerun.knobs.crop_frac",
    kind: "number",
    def: 0.035,
    min: 0,
    max: 0.1,
    step: 0.005,
  },
  {
    key: "ha_exclude_stars",
    labelKey: "rerun.knobs.ha_exclude_stars",
    kind: "toggle",
    def: true,
  },
];

// Tier B — linear finish prep (tens of s–min): the Combined / ColorCal milestones.
const PREP_KNOBS: KnobDef[] = [
  {
    key: "ha_continuum_sub",
    labelKey: "rerun.knobs.ha_continuum_sub",
    kind: "toggle",
    def: true,
  },
  {
    key: "palette",
    labelKey: "rerun.knobs.palette",
    kind: "select",
    def: "natural",
    options: ["natural", "hargb", "hoo", "sho", "hos", "foraxx", "mono"],
  },
  {
    key: "color_calibration",
    labelKey: "rerun.knobs.color_calibration",
    kind: "toggle",
    def: true,
  },
  {
    key: "linked_stretch",
    labelKey: "rerun.knobs.linked_stretch",
    kind: "toggle",
    def: true,
  },
  {
    key: "background_level",
    labelKey: "rerun.knobs.background_level",
    kind: "number",
    def: 0.06,
    min: 0.03,
    max: 0.2,
    step: 0.005,
  },
  {
    key: "stretch_headroom",
    labelKey: "rerun.knobs.stretch_headroom",
    kind: "number",
    def: 0.9,
    min: 0.7,
    max: 1,
    step: 0.02,
  },
  {
    key: "background_degree",
    labelKey: "rerun.knobs.background_degree",
    kind: "number",
    def: 1,
    min: 1,
    max: 4,
    step: 1,
  },
  {
    key: "combined_background_ai",
    labelKey: "rerun.knobs.combined_background_ai",
    kind: "toggle",
    def: true,
  },
  {
    key: "color_denoise_ai",
    labelKey: "rerun.knobs.color_denoise_ai",
    kind: "toggle",
    def: true,
  },
  {
    key: "chroma_smooth_px",
    labelKey: "rerun.knobs.chroma_smooth_px",
    kind: "number",
    def: 6,
    min: 0,
    max: 16,
    step: 1,
  },
  {
    key: "chroma_bg_smooth_px",
    labelKey: "rerun.knobs.chroma_bg_smooth_px",
    kind: "number",
    def: 24,
    min: 0,
    max: 64,
    step: 2,
  },
  {
    key: "sky_chroma_flatten_px",
    labelKey: "rerun.knobs.sky_chroma_flatten_px",
    kind: "number",
    def: 32,
    min: 0,
    max: 128,
    step: 4,
  },
  {
    key: "sky_lum_flatten_px",
    labelKey: "rerun.knobs.sky_lum_flatten_px",
    kind: "number",
    def: 64,
    min: 0,
    max: 256,
    step: 8,
  },
  {
    key: "star_reduce",
    labelKey: "rerun.knobs.star_reduce",
    kind: "number",
    def: 0,
    min: 0,
    max: 1,
    step: 0.05,
  },
  {
    key: "emit_luminance_mono",
    labelKey: "rerun.knobs.emit_luminance_mono",
    kind: "toggle",
    def: true,
  },
  {
    key: "emit_all_channel_mono",
    labelKey: "rerun.knobs.emit_all_channel_mono",
    kind: "toggle",
    def: false,
  },
];

// Tier C — re-stack from the raw frames (min–hours): the per-channel Stacked milestones.
// The stacking ALGORITHM itself lives here too: re-stacking is the only re-entry that can change how
// pixels were combined. The enum values mirror internal/stackalg's catalogue; the launch form builds
// its dropdowns from the engine instead (GET /api/mode-params), but this editor is a fixed catalog by
// design — it must render without a live fetch on a run reopened from disk.
const STACK_KNOBS: KnobDef[] = [
  {
    key: "stack_reject",
    labelKey: "rerun.knobs.stack_reject",
    kind: "select",
    def: "auto",
    options: [
      "auto",
      "none",
      "percentile",
      "sigma",
      "median_sigma",
      "winsorized",
      "linear_fit",
      "gesd",
      "mad",
      "rcr",
      "adaptive_weighted",
      "entropy_weighted",
    ],
  },
  {
    key: "stack_reject_low",
    labelKey: "rerun.knobs.stack_reject_low",
    kind: "number",
    def: 0,
    min: 0,
    max: 10,
    step: 0.1,
  },
  {
    key: "stack_reject_high",
    labelKey: "rerun.knobs.stack_reject_high",
    kind: "number",
    def: 0,
    min: 0,
    max: 10,
    step: 0.1,
  },
  {
    key: "stack_combine",
    labelKey: "rerun.knobs.stack_combine",
    kind: "select",
    def: "mean",
    options: ["mean", "median", "sum", "max", "min", "trimmed_mean"],
  },
  {
    key: "stack_norm",
    labelKey: "rerun.knobs.stack_norm",
    kind: "select",
    def: "addscale",
    options: ["none", "add", "addscale", "mul", "mulscale"],
  },
  {
    key: "stack_weight",
    labelKey: "rerun.knobs.stack_weight",
    kind: "select",
    def: "wfwhm",
    options: ["none", "noise", "wfwhm", "nbstars", "nbstack"],
  },
  {
    key: "fwhm_sigma",
    labelKey: "rerun.knobs.fwhm_sigma",
    kind: "number",
    def: 3,
    min: 1,
    max: 5,
    step: 0.1,
  },
  {
    key: "roundness_floor",
    labelKey: "rerun.knobs.roundness_floor",
    kind: "number",
    def: 0.5,
    min: 0.2,
    max: 0.95,
    step: 0.05,
  },
  {
    key: "background_sigma",
    labelKey: "rerun.knobs.background_sigma",
    kind: "number",
    def: 3,
    min: 1,
    max: 5,
    step: 0.1,
  },
  {
    key: "star_count_frac",
    labelKey: "rerun.knobs.star_count_frac",
    kind: "number",
    def: 0.4,
    min: 0.1,
    max: 1,
    step: 0.05,
  },
  {
    key: "trail_mask_k",
    labelKey: "rerun.knobs.trail_mask_k",
    kind: "number",
    def: 3,
    min: 0,
    max: 6,
    step: 0.5,
  },
  {
    key: "seam_offset_refit",
    labelKey: "rerun.knobs.seam_offset_refit",
    kind: "toggle",
    def: true,
  },
  {
    key: "seam_noise_eq",
    labelKey: "rerun.knobs.seam_noise_eq",
    kind: "toggle",
    def: true,
  },
  {
    // The multi-night union-canvas consent knob (wire key renamed from the legacy "mosaic" —
    // the engine still accepts both; unrelated to the tiled-panel mosaic MODE).
    key: "union_canvas",
    labelKey: "rerun.knobs.union_canvas",
    kind: "toggle",
    def: false,
  },
  {
    key: "union_canvas_fill",
    labelKey: "rerun.knobs.union_canvas_fill",
    kind: "select",
    def: "crop",
    options: ["crop", "fill"],
  },
  {
    key: "denoise_lum",
    labelKey: "rerun.knobs.denoise_lum",
    kind: "number",
    def: 0.5,
    min: 0,
    max: 1,
    step: 0.05,
  },
  {
    key: "denoise_chroma",
    labelKey: "rerun.knobs.denoise_chroma",
    kind: "number",
    def: 0.85,
    min: 0,
    max: 1,
    step: 0.05,
  },
  {
    key: "background_ai",
    labelKey: "rerun.knobs.background_ai",
    kind: "toggle",
    def: true,
  },
];

const KNOBS_BY_GROUP: Record<TierGroup, KnobDef[]> = {
  final: COMPOSITE_KNOBS,
  combined: PREP_KNOBS,
  stacked: STACK_KNOBS,
};

// Every timeline stage key → the knob group (re-entry tier) that reproduces it: aligned/colorcal share
// the linear-prep group, deconv/starless share the composite group.
const STAGE_TO_GROUP: Record<string, TierGroup> = {
  stacked: "stacked",
  aligned: "combined",
  combined: "combined",
  colorcal: "combined",
  deconv: "final",
  starless: "final",
  final: "final",
};

// The i18n key describing what a re-entry at each group's tier recomputes (for the UI cost hint).
export const TIER_HINT_KEY: Record<TierGroup, string> = {
  stacked: "rerun.tier.stack",
  combined: "rerun.tier.prep",
  final: "rerun.tier.composite",
};

export function tierGroupForStage(stage: string): TierGroup {
  return STAGE_TO_GROUP[stage] ?? "final";
}

export function knobsForStage(stage: string): KnobDef[] {
  return KNOBS_BY_GROUP[tierGroupForStage(stage)];
}
