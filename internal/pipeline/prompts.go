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
- oiii_screen (0..0.8): opacity of the teal [OIII] layer (only if OIII present beside a real B; 0 = off).
- oiii_black_point (0..0.3): clips the OIII background to black so its teal lifts only shock fronts.
- sii_screen (0..0.8): opacity of the [SII] layer (only if SII present; 0 = off).
- sii_black_point (0..0.3): clips the SII background to black so its tint lifts only real emission.
- sii_tint ("deep_red"|"gold"): colour of the [SII] screen. deep_red (default) is a crimson that reads as "deeper than Ha"; gold is the amber accent the Hubble palette established — pick gold when SII structure is being lost against the red Ha layer.
- chroma_blur (0..12 px): blurs colour noise in an LRGB composite; luminance keeps detail.
- lum_opacity (0..1): opacity of the L (luminance) layer in an LRGB composite (1 = full detail from L; lower = softer, more RGB-driven). Only if a separate L channel is present.
- crop_frac (0..0.1): trims ragged stacking-edge bands.
- core_highlight_knee/ceil (0..1, knee<ceil): rolls off a blown nebula CORE (on the L luminance).
- highlight_knee/ceil (0..1, knee<ceil): star-safe highlight cap on the final composite — keeps bright STAR cores below white so they keep colour instead of burning to an orange/white blob.
- star_desat (0..1): desaturates the brightest star cores/wings toward white — fixes solid blue/magenta colour DISCS on dense star fields (clusters) and magenta SHO stars; background/nebulosity colour untouched.
- ha_exclude_stars (bool): screen Ha onto nebulosity only (keeps the red Ha off stars → no orange/pink star tint).

TIER B — linear finish prep, re-runs in tens of seconds to minutes:
- background_level (0.03..0.2): target sky brightness for the autostretch (lower = darker sky).
- linked_stretch (bool): keep neutral channel balance in the stretch.
- color_calibration (bool): SPCC photometric colour calibration.
- combined_background_ai (bool): 2nd background-gradient extraction pass on the combined RGB (fixes large-scale gradients/brown sky).
- background_degree (1..4): Siril polynomial background degree.
- color_denoise_ai (bool): AI colour denoise on the linear RGB (fixes chroma noise without blurring).
- chroma_smooth_px (0..16 px, 0 = off): star-protected fine chroma smoothing on the linear RGB (flattens residual colour patches; luminance and star colours untouched).
- chroma_bg_smooth_px (0..64 px, 0 = off): coarse background-only chroma smoothing (flattens large sky colour mottle; objects/stars keep their colour).
- sky_chroma_flatten_px (0..128 px grid, 0 = off): post-stretch sky-chroma neutralization — measures and subtracts the sky's large-scale colour field AFTER the stretch, so banded/tinted sky (left-right colour sweep, coloured discs) turns neutral while objects keep their colour.
- sky_lum_flatten_px (0..256 px grid, 0 = off): post-stretch sky BRIGHTNESS equalization — fits a smooth surface to the sky level AFTER the stretch and equalizes the whole sky to one level, the darkest genuine (glow-free) sky: glow comes down, a beyond-glow corner comes up; objects keep their contrast (uniform local shift).
- lum_boost (0..0.25, 0 = off): gentle midtone lift of the L luminance curve (value = peak lift at mid-grey; sky-level AND core/star points pinned) — brighter galaxy periphery/arms without touching sky level, core detail or colour balance.
- star_reduce (0..1): StarNet++ star reduction opacity (1 = full stars, 0.5 = halved).
- stretch_headroom (0.7..1.0, 0 = off): caps bright star cores this far below white BEFORE the stretch so they keep colour instead of burning; lower = more protection (use when star cores are blown/white).
- palette ("natural"|"hargb"|"hoo"|"sho"|"hos"|"foraxx"|"mono"): channel→RGB colour mapping. natural/hargb are broadband (SPCC + Hα screen); the narrowband palettes (hoo/sho/hos/foraxx) need OIII/SII filters and disable the Hα screen + SPCC; a palette missing its filters falls back toward natural.

TIER C — re-stack from the raw frames, EXPENSIVE (minutes to hours). Use ONLY for structural defects that finishing cannot fix (e.g. insufficient_integration, trail_residue, heavy luminance_noise):
- roundness_floor (0.2..0.95), fwhm_sigma/background_sigma (1..5), star_count_frac (0.1..1): frame-rejection thresholds (raise to keep more frames, lower to reject harder).
- trail_mask_k (0..6): cross-frame satellite/plane trail masking (lower = more aggressive).
- denoise_chroma/denoise_lum (0..1): per-channel linear denoise strength.
- background_ai (bool): GraXpert per-channel background extraction.`

// defectVocabulary is the fixed "kind" set both prompts diagnose against.
const defectVocabulary = `background_cast_green, background_cast_magenta, background_cast_warm, background_too_bright, background_too_dark, shadows_crushed, highlights_blown, core_blown, stars_blown, stars_discolored, oversaturated, undersaturated, chroma_noise, luminance_noise, gradient, stars_bloated, stars_dominant, insufficient_integration, trail_residue, halo_artifacts, edge_artifacts`
