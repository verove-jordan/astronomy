// A reference glossary for the "advanced parameters" JSON: every tunable knob for the selected mode,
// grouped by the pipeline re-entry TIER its change triggers (finish / prep / re-stack — the cost model),
// so the raw JSON editor is self-documenting. Each key's human description lives in i18n under
// `paramDocs.<key>`; the groups mirror the Go whitelist per mode (internal/pipeline/params_patch.go
// ParamsFor + knownParamKeys, and the per-mode knob menus in prompts.go / planetary.go /
// supervise_comet.go / supervise_nightscape.go). Because each mode exposes a different knob set, the
// glossary is mode-aware: `groupsForMode(mode)` returns the groups for that mode.

export interface ParamGroup {
  titleKey: string; // i18n: the group heading
  hintKey: string; // i18n: what re-running this tier recomputes (the cost)
  keys: string[]; // the JSON keys in this tier (each has a paramDocs.<key> description)
}

// Deep-sky / nebula / livestack share the full tiered surface (supervisePatch).
const DEEPSKY_GROUPS: ParamGroup[] = [
  {
    titleKey: "paramDocs.groups.composite",
    hintKey: "paramDocs.groups.compositeHint",
    keys: [
      "saturation",
      "star_desat",
      "lum_opacity",
      "lum_boost",
      "ha_screen",
      "ha_black_point",
      "oiii_screen",
      "oiii_black_point",
      "sii_screen",
      "sii_black_point",
      "sii_tint",
      "ha_exclude_stars",
      "ha_continuum_sub",
      "chroma_blur",
      "core_highlight_knee",
      "core_highlight_ceil",
      "highlight_knee",
      "highlight_ceil",
      "crop_frac",
    ],
  },
  {
    titleKey: "paramDocs.groups.prep",
    hintKey: "paramDocs.groups.prepHint",
    keys: [
      "palette",
      "color_calibration",
      "linked_stretch",
      "background_level",
      "stretch_headroom",
      "background_degree",
      "combined_background_ai",
      "color_denoise_ai",
      "chroma_smooth_px",
      "chroma_bg_smooth_px",
      "sky_chroma_flatten_px",
      "sky_lum_flatten_px",
      "star_reduce",
      "emit_luminance_mono",
      "emit_all_channel_mono",
    ],
  },
  {
    titleKey: "paramDocs.groups.stack",
    hintKey: "paramDocs.groups.stackHint",
    keys: [
      "fwhm_sigma",
      "roundness_floor",
      "background_sigma",
      "star_count_frac",
      "trail_mask_k",
      "seam_offset_refit",
      "seam_noise_eq",
      "union_canvas",
      "union_canvas_fill",
      "denoise_lum",
      "denoise_chroma",
      "background_ai",
    ],
  },
];

// Tiled-panel mosaic (mosaicPatch on top of the deepsky surface): the assembler's own knobs, then
// the shared deep-sky finish groups.
const MOSAIC_GROUPS: ParamGroup[] = [
  {
    titleKey: "paramDocs.groups.mosaicAssembly",
    hintKey: "paramDocs.groups.mosaicAssemblyHint",
    keys: [
      "overlap_expected",
      "feather_frac",
      "photom_match",
      "canvas_crop",
      "min_panel_frames",
      "panel_source",
    ],
  },
  ...DEEPSKY_GROUPS,
];

// Planetary / lunar (planetaryPatch): a fast finish + an expensive re-stack tier.
const PLANETARY_GROUPS: ParamGroup[] = [
  {
    titleKey: "paramDocs.groups.planetaryFinish",
    hintKey: "paramDocs.groups.planetaryFinishHint",
    keys: [
      "stretch",
      "highlight",
      "shadow_lift",
      "sharpen",
      "clahe",
      "saturation",
      "headroom",
      "limb_balance",
      "earthshine_gain",
      "earthshine_feather",
      "true_lum",
    ],
  },
  {
    titleKey: "paramDocs.groups.planetaryStack",
    hintKey: "paramDocs.groups.planetaryStackHint",
    keys: [
      "best_percent",
      "ap_align",
      "double_stack",
      "calibrate",
      "deconv_fwhm",
      "deconv_iters",
      "deconv_alpha",
      "drizzle_scale",
      "align_points",
    ],
  },
];

// Comet (cometPatch): a fast colour re-combine + the frame-rejection re-stack tier.
const COMET_GROUPS: ParamGroup[] = [
  {
    titleKey: "paramDocs.groups.cometFinish",
    hintKey: "paramDocs.groups.cometFinishHint",
    keys: ["background_level", "background_degree", "saturation"],
  },
  {
    titleKey: "paramDocs.groups.cometStack",
    hintKey: "paramDocs.groups.cometStackHint",
    keys: [
      "roundness_floor",
      "fwhm_sigma",
      "background_sigma",
      "star_count_frac",
      "trail_mask_k",
      "seam_offset_refit",
      "seam_noise_eq",
      "union_canvas",
      "union_canvas_fill",
      "per_frame_starnet",
    ],
  },
];

// Milky-Way nightscape (nightscapePatch): a single grade tier, all re-render in seconds.
const MILKYWAY_GROUPS: ParamGroup[] = [
  {
    titleKey: "paramDocs.groups.milkywayGrade",
    hintKey: "paramDocs.groups.milkywayGradeHint",
    keys: ["look", "brightness", "saturation_scale", "highlight_ceiling"],
  },
];

const GROUPS_BY_MODE: Record<string, ParamGroup[]> = {
  deepsky: DEEPSKY_GROUPS,
  nebula: DEEPSKY_GROUPS,
  livestack: DEEPSKY_GROUPS,
  planetary: PLANETARY_GROUPS,
  comet: COMET_GROUPS,
  milkyway: MILKYWAY_GROUPS,
  mosaic: MOSAIC_GROUPS,
};

// groupsForMode returns the knob groups for a stacking mode (deep-sky is the fallback for any unknown).
export function groupsForMode(mode: string): ParamGroup[] {
  return GROUPS_BY_MODE[mode] ?? DEEPSKY_GROUPS;
}

// Back-compat alias: the default deep-sky groups (some callers still import the flat list).
export const PARAM_GROUPS = DEEPSKY_GROUPS;

// Every key the glossary documents across all modes (for a completeness check against the whitelist).
export const DOCUMENTED_PARAMS = new Set(
  Object.values(GROUPS_BY_MODE).flatMap((groups) =>
    groups.flatMap((g) => g.keys),
  ),
);
