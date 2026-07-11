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
      "ha_screen",
      "ha_black_point",
      "ha_exclude_stars",
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
      "star_reduce",
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
      "denoise_lum",
      "denoise_chroma",
      "background_ai",
    ],
  },
];

// Planetary / lunar (planetaryPatch): a fast finish + an expensive re-stack tier.
const PLANETARY_GROUPS: ParamGroup[] = [
  {
    titleKey: "paramDocs.groups.planetaryFinish",
    hintKey: "paramDocs.groups.planetaryFinishHint",
    keys: [
      "stretch",
      "highlight",
      "sharpen",
      "clahe",
      "saturation",
      "headroom",
    ],
  },
  {
    titleKey: "paramDocs.groups.planetaryStack",
    hintKey: "paramDocs.groups.planetaryStackHint",
    keys: [
      "best_percent",
      "ap_align",
      "deconv_fwhm",
      "deconv_iters",
      "deconv_alpha",
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
