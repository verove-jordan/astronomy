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

// The stacking algorithm itself (internal/stackalg): how pixels are combined and which outlier test
// rejects them. Edited through the StackingPanel rather than by hand, but documented here too so the
// glossary stays a complete reference for every key the JSON box accepts.
const STACK_ALGO_GROUP: ParamGroup = {
  titleKey: "paramDocs.groups.stackAlgo",
  hintKey: "paramDocs.groups.stackAlgoHint",
  keys: [
    "stack_engine",
    "stack_combine",
    "stack_reject",
    "stack_reject_low",
    "stack_reject_high",
    "stack_trim_frac",
    "stack_norm",
    "stack_fast_norm",
    "stack_weight",
    "stack_rejection_maps",
    "stack_feather",
    "stack_local_norm",
    "stack_local_norm_degree",
  ],
};

// The calibration masters' per-frame-type recipes: each type is stacked separately and their pools
// differ by an order of magnitude, so each carries its own algorithm. Edited through the
// StackingPanel's "Calibration masters" sub-section.
const MASTER_STACK_GROUP: ParamGroup = {
  titleKey: "paramDocs.groups.masterStack",
  hintKey: "paramDocs.groups.stackAlgoHint",
  keys: [
    "master_bias_combine",
    "master_bias_reject",
    "master_bias_low",
    "master_bias_high",
    "master_dark_combine",
    "master_dark_reject",
    "master_dark_low",
    "master_dark_high",
    "master_flat_combine",
    "master_flat_reject",
    "master_flat_low",
    "master_flat_high",
    "master_dark_flat_combine",
    "master_dark_flat_reject",
    "master_dark_flat_low",
    "master_dark_flat_high",
  ],
};

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
  STACK_ALGO_GROUP,
  MASTER_STACK_GROUP,
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
  STACK_ALGO_GROUP,
  {
    titleKey: "paramDocs.groups.cometStackAlgo",
    hintKey: "paramDocs.groups.stackAlgoHint",
    keys: ["comet_stack_reject", "comet_stack_low", "comet_stack_high"],
  },
];

// Milky-Way nightscape (nightscapePatch): a single grade tier, all re-render in seconds.
const MILKYWAY_GROUPS: ParamGroup[] = [
  {
    titleKey: "paramDocs.groups.milkywayGrade",
    hintKey: "paramDocs.groups.milkywayGradeHint",
    keys: [
      "look",
      "brightness",
      "saturation_scale",
      "highlight_ceiling",
      "keep_meteors",
    ],
  },
];

// Sky panorama (nightscapePatch + nightpanoPatch): every panel is stacked by the milkyway recipe, so
// the grade knobs are the same ones; the canvas group is what only exists once there are several
// pointings to join.
const NIGHTPANO_GROUPS: ParamGroup[] = [
  ...MILKYWAY_GROUPS,
  {
    titleKey: "paramDocs.groups.panoCanvas",
    hintKey: "paramDocs.groups.panoCanvasHint",
    keys: [
      "projection",
      "scale_deg_per_pix",
      "group_step_deg",
      "band_mask_lat_deg",
      "pano_background",
      "pano_foreground",
    ],
  },
];

// Solar (sunPatch): a fast finish tier and an expensive re-ingest/re-stack tier.
const SUN_GROUPS: ParamGroup[] = [
  {
    titleKey: "paramDocs.groups.sunFinish",
    hintKey: "paramDocs.groups.sunFinishHint",
    keys: [
      "flat_strength",
      "deconv_auto",
      "deconv_sigma",
      "deconv_iters",
      "sharpen_small",
      "sharpen_medium",
      "sharpen_large",
      "sharpen_denoise",
      "limb_flatten",
      "prominence_boost",
      "prominence_feather",
      "palette",
      "stretch",
      "contrast",
      "saturation",
      "background_level",
      "background_tint",
      "glow_strength",
      "glow_radius",
    ],
  },
  {
    titleKey: "paramDocs.groups.sunStack",
    hintKey: "paramDocs.groups.sunStackHint",
    keys: [
      "band",
      "keep_percent",
      "max_frames",
      "drizzle",
      "clip_sigma",
      "window_seconds",
      "window_frames",
      "min_frames",
      "crop_margin",
      "scale_tolerance",
      "bracket_merge",
      "bracket_stops",
      "transparency_floor",
      "ap_align",
      "ap_scale",
    ],
  },
];

// The eclipse sheet: which phases to show and how to arrange them. Kept out of SUN_GROUPS because
// the knobs need an occulter to mean anything, and a full-disc solar run has none.
const SEQUENCE_GROUP: ParamGroup = {
  titleKey: "paramDocs.groups.eclipseSequence",
  hintKey: "paramDocs.groups.eclipseSequenceHint",
  keys: [
    "sequence_panels",
    "sequence_angle_deg",
    "sequence_spacing",
    "site_lat",
    "site_lon",
  ],
};

const ECLIPSE_GROUPS: ParamGroup[] = [...SUN_GROUPS, SEQUENCE_GROUP];

const GROUPS_BY_MODE: Record<string, ParamGroup[]> = {
  deepsky: DEEPSKY_GROUPS,
  nebula: DEEPSKY_GROUPS,
  livestack: DEEPSKY_GROUPS,
  planetary: PLANETARY_GROUPS,
  comet: COMET_GROUPS,
  milkyway: MILKYWAY_GROUPS,
  nightpano: NIGHTPANO_GROUPS,
  mosaic: MOSAIC_GROUPS,
  sun: SUN_GROUPS,
  // An eclipse run is the solar recipe with a second circle in the geometry; it exposes exactly
  // the same knob surface, so it shares the groups rather than copying them.
  eclipse: ECLIPSE_GROUPS,
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
