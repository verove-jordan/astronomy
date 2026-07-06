// prompts.go holds the tiered knob menu and defect vocabulary shared by the finish supervisor
// (supervisorSystemPrompt, supervise_critique.go) and the AstroAgent chat (AssistSystemPrompt,
// assist.go), so the two prompts describe the exact same controls and never drift apart.
package pipeline

// tierKnobMenu is the whitelisted tier A/B/C controls with their cost and safe range — the single
// source of truth for both the supervisor and the chat assistant.
const tierKnobMenu = `TIER A — GIMP composite, re-renders in seconds:
- saturation (0..0.6): overall colour saturation.
- ha_screen (0..0.8): opacity of the red H-alpha layer (only if Ha present).
- ha_black_point (0..0.3): clips Ha background to black so its red lifts only bright HII knots.
- chroma_blur (0..12 px): blurs colour noise in an LRGB composite; luminance keeps detail.
- crop_frac (0..0.1): trims ragged stacking-edge bands.
- core_highlight_knee/ceil (0..1, knee<ceil): rolls off a blown nebula CORE (on the L luminance).
- highlight_knee/ceil (0..1, knee<ceil): star-safe highlight cap on the final composite — keeps bright STAR cores below white so they keep colour instead of burning to an orange/white blob.
- ha_exclude_stars (bool): screen Ha onto nebulosity only (keeps the red Ha off stars → no orange/pink star tint).

TIER B — linear finish prep, re-runs in tens of seconds to minutes:
- background_level (0.03..0.2): target sky brightness for the autostretch (lower = darker sky).
- linked_stretch (bool): keep neutral channel balance in the stretch.
- color_calibration (bool): SPCC photometric colour calibration.
- combined_background_ai (bool): 2nd background-gradient extraction pass on the combined RGB (fixes large-scale gradients/brown sky).
- background_degree (1..4): Siril polynomial background degree.
- color_denoise_ai (bool): AI colour denoise on the linear RGB (fixes chroma noise without blurring).
- star_reduce (0..1): StarNet++ star reduction opacity (1 = full stars, 0.5 = halved).

TIER C — re-stack from the raw frames, EXPENSIVE (minutes to hours). Use ONLY for structural defects that finishing cannot fix (e.g. insufficient_integration, trail_residue, heavy luminance_noise):
- roundness_floor (0.2..0.95), fwhm_sigma/background_sigma (1..5), star_count_frac (0.1..1): frame-rejection thresholds (raise to keep more frames, lower to reject harder).
- trail_mask_k (0..6): cross-frame satellite/plane trail masking (lower = more aggressive).
- denoise_chroma/denoise_lum (0..1): per-channel linear denoise strength.
- background_ai (bool): GraXpert per-channel background extraction.`

// defectVocabulary is the fixed "kind" set both prompts diagnose against.
const defectVocabulary = `background_cast_green, background_cast_magenta, background_cast_warm, background_too_bright, background_too_dark, shadows_crushed, highlights_blown, core_blown, stars_blown, stars_discolored, oversaturated, undersaturated, chroma_noise, luminance_noise, gradient, stars_bloated, stars_dominant, insufficient_integration, trail_residue, halo_artifacts, edge_artifacts`
