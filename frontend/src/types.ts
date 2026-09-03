// API response types mirroring the Go engine's JSON.

export interface Frame {
  path: string;
  type: string;
  filter?: string;
  exposure_ms: number;
  gain: number;
  offset: number;
  iso?: number;
  temp_milli_c: number;
  has_temp: boolean;
  width: number;
  height: number;
  // Colour-filter-array pattern (e.g. "GRBG") for a one-shot-color frame still in its raw Bayer
  // mosaic; absent for monochrome and for already-debayered frames.
  bayer?: string;
  // Plane count: 1 for monochrome and for undebayered CFA, 3 for an already-demosaiced RGB frame.
  // Absent means undetermined (read as 1). With `bayer` this names the three states the pipeline
  // distinguishes — mono, CFA awaiting debayer, and RGB.
  channels?: number;
  object?: string;
  date_obs?: string;
  date_obs_ms?: number;
  // Capture-night key "YYYY-MM-DD" (local-noon bucketed); absent when the frame carries no DATE-OBS.
  session?: string;
  class_source: string;
  filter_confidence?: number;
  wheel_transition?: boolean;
}

export interface SetKey {
  type: string;
  object?: string;
  filter?: string;
  exposure_ms: number;
  gain: number;
  offset: number;
  iso?: number;
  temp_bucket_c: number;
  bin: number;
  // Capture night of a per-night set — present only on multi-night scans, and only for lights/flats.
  session?: string;
  // True for a one-shot-color set. Colour and monochrome frames never share a set: nothing could
  // stack them together or calibrate one with the other's master.
  color?: boolean;
}

export interface FrameSet {
  key: SetKey;
  count: number;
  total_integration_ms: number;
}

// DetectedRun is one contiguous same-filter block found by signal-based channel detection.
export interface DetectedRun {
  filter: string;
  count: number;
  confidence: number;
  first_frame: string;
  last_frame: string;
  wheel_transition?: number;
}

export interface ChannelDetection {
  order: string[];
  overall_confidence: number;
  runs: DetectedRun[];
}

export interface Inventory {
  root: string;
  frames: Frame[];
  sets: FrameSet[];
  videos: Frame[];
  warnings: string[];
  channel_detection?: ChannelDetection;
  // How the capture records colour, decided from its lights: "mono" (a filter wheel, stacked per
  // filter and combined), "osc" (one-shot color, stacked as a single RGB channel), or "mixed" (both
  // in one folder, which no single run can stack).
  color_model?: "mono" | "osc" | "mixed";
  // Per-capture-night summary (sorted by night, undated bucket last); absent when nothing is dated.
  sessions?: SessionInfo[];
}

// SessionInfo summarizes one capture night of a scan (key "" = the undated bucket).
export interface SessionInfo {
  key: string;
  start_ms?: number;
  end_ms?: number;
  counts: Record<string, number>; // frame counts by type (LIGHT/DARK/FLAT/BIAS/…)
  configs?: SessionConfig[];
}

// SessionConfig is one distinct light-capture configuration within a night, with its frame count.
export interface SessionConfig {
  filter?: string;
  exposure_ms: number;
  gain: number;
  offset: number;
  bin: number;
  temp_bucket_c: number;
  count: number;
}

// PreviewImage is the decoded binary buffer from GET /api/preview: a downsampled, linearly-normalized
// 16-bit image (samples 0–65535) the file viewer stretches client-side. c = 1 (mono) or 3 (RGB,
// interleaved). autoLo/autoHi are suggested default black/white points.
export interface PreviewImage {
  w: number;
  h: number;
  c: number;
  autoLo: number;
  autoHi: number;
  data: Uint16Array;
}

export interface Master {
  type: string;
  filter?: string;
  exposure_ms: number;
  gain: number;
  offset: number;
  temp_milli_c: number;
  bin: number;
  frame_count: number;
  path: string;
}

// PhoneMaster is a reusable phone/DSLR calibration master (iPhone DNG darks/bias/flats), keyed by
// ISO / exposure / sensor dimensions rather than gain/offset/bin.
export interface PhoneMaster {
  type: string;
  iso: number;
  exposure_ms: number;
  camera_model?: string;
  width: number;
  height: number;
  frame_count: number;
  path: string;
}

export interface GradeMetric {
  index: number;
  path: string;
  fwhm: number;
  wfwhm: number;
  roundness: number;
  star_count: number;
  background: number;
  trail_detected: boolean;
  trail_score: number;
  rejected: boolean;
  reject_reason?: string;
}

export interface Selection {
  dark?: Master;
  flat?: Master;
  bias?: Master;
  notes?: string[];
}

// Calibration suggestions (POST /api/calib/preview): per inspected light channel, the master
// dark/flat/bias that would be applied — built from the capture's own cal frames (from_capture) or
// reused from the library. `id` is the per-(channel,role) key sent back to exclude one.
export interface CalibSuggestion {
  id: string;
  role: string; // "dark" | "flat" | "bias"
  master: Master;
  from_capture?: boolean;
}
export interface CalibChannel {
  filter: string;
  exposure_ms: number;
  gain: number;
  offset: number;
  temp_bucket_c: number;
  bin: number;
  // Capture night of the light set on a multi-night scan (groups the per-night calibration mapping).
  session?: string;
  suggestions: CalibSuggestion[];
  notes?: string[];
}
export interface CalibPreview {
  channels: CalibChannel[];
}

// AlignPointsEstimate mirrors the Go struct returned by POST /api/planetary/align-points: how many
// stacking reference points the first luminance frame supports at a given minimum detail size.
export interface AlignPointsEstimate {
  frame: string;
  width: number;
  height: number;
  window_px: number;
  cell_px: number;
  per_axis: number;
  total_points: number;
  usable_points: number;
  usable_fraction: number;
  suggested_align_points: number;
  auto_per_axis: number;
  disc: { cx: number; cy: number; r: number; ok: boolean };
}

// SetQaReport mirrors internal/setqa.Report (POST /api/quality/sets): the pre-stack stray-light
// check over every light set of a selection. SetQaSet.id is the inspect SetKey.ID exclusion token
// carried by RunRequest.exclude_sets.
export interface SetQaReason {
  code:
    | "border_glow"
    | "strong_gradient"
    | "stack_visible"
    | "outlier_vs_siblings"
    | "channel_imbalance";
  border?: string;
  channel?: string;
  amplitude_pct: number;
  sigma: number;
  text: string;
}
export interface SetQaImpact {
  filter: string;
  filter_frames: number;
  filter_integration_ms: number;
  lost_frames: number;
  lost_integration_ms: number;
  lost_integration_pct: number;
  snr_factor: number;
  empties_filter: boolean;
}
export interface SetQaSet {
  id: string;
  key: SetKey;
  count: number;
  total_integration_ms: number;
  sampled: number;
  measured: boolean;
  affected_frac: number;
  border_sigma: number;
  border_pct: number;
  grad_sigma: number;
  grad_pct: number;
  worst_border?: string;
  stacked_sigma: number;
  score: number;
  flagged: boolean;
  reasons?: SetQaReason[];
  preview_frame?: string;
  impact: SetQaImpact;
}
export interface SetQaReport {
  sets: SetQaSet[];
  flagged: number;
  warnings?: string[];
}

export interface ChannelResult {
  object: string;
  filter: string;
  exposure_ms: number;
  input_frames: number;
  stacked_frames: number;
  output_path?: string;
  preview_path?: string;
  selection: Selection;
  metrics?: GradeMetric[];
  dither?: DitherReport;
  // Per-group photometric-normalization records of a cross-session merge (mirrors run.json).
  photom?: PhotomRecord[];
  // Per-night/per-session provenance of a cross-session merge (masters used, parity, previews).
  groups?: GroupResult[];
  error?: string;
  // Coverage of the anchor canvas by this channel's STACKED frames (grouped runs): fraction at the
  // preset's minimum depth + the grayscale mask thumbnail path.
  covered_frac?: number;
  coverage_mask?: string;
  canvas?: { w: number; h: number; off_x: number; off_y: number };
  mosaic_fill?: {
    filled_frac: number;
    applied: boolean;
    mask_png?: string;
    noise_sigma?: number;
  };
  seam?: unknown;
}

// CombineCrop mirrors Go pipeline.CombineCrop: the coverage-derived crop of the combine inputs.
export interface CombineCrop {
  x: number;
  y: number;
  w: number;
  h: number;
  frac: number; // rectangle area / canvas area
  applied: boolean;
  note?: string;
}

// PhotomRecord mirrors Go photom.GroupRecord: how one group's linear scale was mapped onto the
// reference group before the cross-session stack.
export interface PhotomRecord {
  session_id?: number;
  session?: string; // capture-night key — the UI's join key
  label: string;
  scale: number;
  offset: number;
  resid: number;
  frames: number;
  clamped?: boolean;
  meta_disagree?: boolean;
  meta_seeded?: boolean; // curves too flat to measure — scale IS the header exposure/gain prediction
  ref?: boolean; // the photometric reference group
  applied: boolean;
  // Which ladder rung set the scale: measured | seeded | bg-matched | offset-only | identity.
  method?: string;
  // The fitted transform would have clipped the sky below zero and was degraded (scale/offset show
  // the degraded values).
  reverted?: boolean;
}

// GroupResult mirrors Go pipeline.GroupResult: one calibration group's provenance inside a
// cross-session channel merge (run.json `channels[].groups`).
export interface GroupResult {
  session_id: number;
  current?: boolean;
  session?: string;
  filter: string;
  exposure_ms: number;
  gain: number;
  offset: number;
  temp_bucket_c: number;
  bin: number;
  frames: number;
  dark?: string;
  flat?: string;
  bias?: string;
  flat_source?: string; // "run" | "session-rebuild" | "none"
  parity_flipped?: boolean;
  // Median field rotation (degrees) and footprint overlap (0..1) vs the run's anchor canvas,
  // measured from the merged registration (multi-group channels only).
  rotation_deg?: number;
  overlap_frac?: number;
  photom?: PhotomRecord;
  // How many of this night's frames reached the channel stack vs were rejected (registration +
  // grading) — the per-night contribution ledger.
  stacked_frames?: number;
  rejected_frames?: number;
  prenorm_preview?: string;
  normalized_preview?: string;
}

// DitherReport classifies the capture-time pointing pattern from the registration offsets:
// "dithered" (residual fixed-pattern noise decorrelates and is rejected), "drift"/"static"
// (walking-noise risk — the run-level warning recommends dithering), or "mixed".
export interface DitherReport {
  pattern: string;
  frames: number;
  span_px: number;
  step_median_px: number;
  direction_r: number;
  drift_px_per_frame: number;
  note?: string;
}

// Defect is one issue the vision model diagnosed in a supervised render.
export interface Defect {
  kind: string;
  severity: string; // low | medium | high
  note?: string;
}

// IterationRecord is one pass of the optional local-AI-agent finish supervisor.
export interface IterationRecord {
  index: number;
  tier?: string; // pipeline re-entry tier used: A (composite) | B (finish prep) | C (re-stack)
  png_path: string;
  det_score: number;
  model_score: number;
  combined_score: number;
  reasoning: string;
  defects?: Defect[];
  chosen: boolean;
  params?: Record<string, number>;
}

// StagePreview is one saved processing-milestone preview PNG (stacked/aligned/combined/colorcal/
// starless/final). `stage` is a key the UI maps to a localized label; `filter` is set for per-channel
// milestones (L/R/G/B/Ha). `index` orders the timeline left→right.
export interface StagePreview {
  index: number;
  stage: string;
  filter?: string;
  // Capture night of a per-session milestone (the prenorm/normalized pairs); absent for run-level ones.
  session?: string;
  // Mosaic panel folder ("p01"…) of a per-panel milestone; absent for run-level ones.
  tile?: string;
  png_path: string;
}

// One auxiliary monochrome deliverable saved next to the colour final: the processed Luminance-only
// image ("luminance") or the combined all-channel integration ("all_channels"). png/tif are also in
// FinalResult.outputs; this typed list drives the dedicated mono viewer in RunResultPanels.
export interface MonoOutput {
  kind: string;
  png: string;
  tif?: string;
}

export interface FinalResult {
  mode: string;
  channels: string[];
  outputs: string[];
  notes?: string[];
  iterations?: IterationRecord[];
  mono_outputs?: MonoOutput[];
}

export interface RunResult {
  input_dir: string;
  output_dir: string;
  object?: string;
  run_id?: string;
  // Engine build that produced this run (internal/buildinfo: "version" or "version (built_at)";
  // "dev" = un-stamped build). Present on nested and flat (planetary) results alike.
  engine?: string;
  detection?: ChannelDetection;
  masters: Master[];
  channels: ChannelResult[];
  final?: FinalResult;
  // The capture night whose canvas every channel master was registered onto (grouped runs only).
  anchor_night?: string;
  // Coverage-derived crop applied to the colour-combine inputs (grouped runs): the common covered
  // rectangle in canvas pixels, or the honest full-field fallback (applied=false + note).
  combine_crop?: CombineCrop;
  // Ragged-stacking-edge trim of the colour-combine inputs (edgecrop.go), measured from the stack's
  // own pixels — so unlike combine_crop it is present on single-session runs too.
  edge_crop?: CombineCrop;
  warnings: string[];
  // Planetary / comet lucky-imaging runs return a flat result (no `final` wrapper): the stacked
  // image outputs plus frame stats. RunResultPanels falls back to these when `final` is absent.
  outputs?: string[];
  notes?: string[];
  source?: string;
  frame_count?: number;
  stacked_frames?: number;
  frames?: PlanetaryFrame[];
  // Supervised-finish passes for a flat (planetary) result — nested results carry these under `final`.
  iterations?: IterationRecord[];
  // Saved processing-milestone previews (stacked/aligned/combined/finish…), for the stage timeline.
  stage_previews?: StagePreview[];
  // The run's resolved options block (run.json `options`) — the PROCESSING mode plus every fine knob,
  // persisted for provenance. `mode` here is deepsky/nebula/planetary/…; do NOT confuse it with
  // `final.mode`, which is the channel COMPOSITION (LRGB/HaLRGB/SHO/mono) and never a processing mode.
  options?: RunOptions;
}

// RunOptions is run.json's `options` block: the processing mode plus the fine knobs the run resolved.
// Open-ended because the knob set grows with the pipeline; only `mode` is relied on by name.
export interface RunOptions {
  mode?: string;
  [key: string]: unknown;
}

// StarLabel is one named object on the final image (stars.json): x/y in final-image pixel coords.
export interface StarLabel {
  x: number;
  y: number;
  name: string;
  secondary?: string; // next designation ("α Lyr" for Vega) / common name for DSOs
  kind: "star" | "dso";
  type?: string; // DSO display type (galaxy, emission_nebula, …)
  mag: number; // 99 = unknown (sorts last)
  diameter_arcmin?: number;
  // The object's catalogued footprint, pre-projected by the engine into final-image pixels (see
  // internal/annotate.Extent). Absent for stars and for DSOs with no catalogued size.
  extent?: StarLabelExtent;
  // What the star catalogue knows about this star (nil for DSOs, and for stars the shallow embedded
  // catalogue could only name).
  star?: StarCatalogInfo;
}

// StarCatalogInfo is the catalogue's record of one identified star, straight from
// internal/annotate.StarInfo. It rides on both the text labels and the plotted detections, so the
// hover card renders one block either way.
//
// absmag / ci / rv_km_s are `number | null | undefined` on purpose: zero is a real measurement for
// all three (an A0 star has B−V = 0), so "absent" cannot be encoded as 0 without inventing data.
export interface StarCatalogInfo {
  name?: string;
  secondary?: string;
  mag?: number; // catalogue V magnitude — MEASURED, unlike DetectedStar.mag
  ra_deg?: number;
  dec_deg?: number;
  dist_pc?: number; // 0/absent = unknown; no star sits at 0 pc
  absmag?: number | null; // absolute magnitude — intrinsic brightness
  ci?: number | null; // B−V colour index — the star's colour, so its temperature
  rv_km_s?: number | null; // radial velocity, km/s; positive = receding
  spect?: string; // MK spectral type, e.g. "G2 V"
  con?: string; // 3-letter IAU constellation
}

// StarLabelExtent is a DSO's elliptical footprint in FINAL-IMAGE pixels: semi-axes plus the major
// axis's angle in image space (radians, +x toward +y — the argument canvas `ellipse()` wants). The
// engine resolves sky orientation, so the overlay only has to apply the viewer's zoom.
export interface StarLabelExtent {
  rx_px: number;
  ry_px: number;
  angle_rad: number;
}

// SkyFrame anchors the final image on the sky: its centre plus the midpoints of its far x and y
// edges. Three points fix orientation, field of view AND parity — it mirrors annotate.Frame, which
// is what internal/scene3d feeds newBasis. The engine has always served it inside `solve`; this type
// simply never declared it, so TypeScript dropped it on the floor.
export interface SkyFrame {
  center_ra: number;
  center_dec: number;
  x_edge_ra: number;
  x_edge_dec: number;
  y_edge_ra: number;
  y_edge_dec: number;
}

// StarAnnotations is GET/POST /api/jobs/{id}/stars — the run's persisted stars.json: the star
// count on the linear master (windowed to the final image) plus name labels when the field's
// astrometric solution validated.
export interface StarAnnotations {
  count: number;
  image: { width: number; height: number };
  solved: boolean;
  solve?: {
    method?: string;
    reason?: string;
    scale_arcsec_px?: number;
    mag_zero_point?: number;
    row_order?: string;
    // The image's three sky anchors — the only record of the field's ROLL, and so the only way to
    // place anything external (a galactic direction, the Milky Way) into the 3D scene correctly.
    frame?: SkyFrame;
    // Which catalogue named the stars: "athyg" (the downloaded 2.5-million-star one) or "embedded"
    // (the built-in magnitude-9 extract), plus how many plotted stars it actually resolved.
    star_catalog?: string;
    identified?: number;
  };
  labels: StarLabel[];
  // Detected star positions in final-image pixels, brightest first — the individuals behind `count`.
  // Capped engine-side, so `stars.length < count` means this is the brightest slice. Present even
  // when `solved` is false: counting needs no astrometric solution.
  stars?: DetectedStar[];
  computed_at?: string;
}

// Scene3DManifest is GET /api/jobs/{id}/scene3d — the run's 3D field map, minus the star field
// itself (that rides in `points`, 24 bytes per star, so it can go straight into a GPU buffer).
// `available` false is a normal answer, not an error: `reason` then says why the run has no scene.
export interface Scene3DManifest {
  version: number;
  available: boolean;
  reason?: string;
  // True when the run's annotation is simply too old to build a scene from — recomputing the stars
  // fixes it, as opposed to a run that can never have one.
  needs_recompute?: boolean;
  image: { width: number; height: number };
  camera: Scene3DCamera;
  depth: Scene3DDepth;
  stars: Scene3DCounts;
  photometric: Scene3DPhotometric;
  billboards?: Scene3DBillboard[];
  // Full paths for /api/file — the binary star field and the billboard texture.
  points?: string;
  backdrop?: string;
  computed_at?: string;
}

// Scene3DCamera is the pinhole that reproduces the photograph: a camera at the origin looking down
// +Z with these half-field tangents renders the star field back into the picture it came from.
export interface Scene3DCamera {
  tan_half_w: number;
  tan_half_h: number;
  fov_y_deg: number;
  center_ra: number;
  center_dec: number;
  // False on a mirrored field (a session shot through a star diagonal).
  right_handed: boolean;
}

// Scene3DDepth is the field's distance structure in parsec. near/far are the 5th/95th percentiles
// and are what the logarithmic depth warp spans — the raw extremes are one foreground dwarf and one
// background giant, and letting either set the scale flattens everything between them.
export interface Scene3DDepth {
  near_pc: number;
  far_pc: number;
  min_pc: number;
  max_pc: number;
  median_pc: number;
}

export interface Scene3DCounts {
  plotted: number;
  placed: number;
  measured: number;
  estimated: number;
  unknown: number;
  identified: number;
  named: number;
  // Stars drawn at their blackbody hue rather than at the sampled pixel colour, and those with a
  // real measured space velocity.
  physical_colour: number;
  moving: number;
}

// Scene3DPhotometric grades the estimated distances. holdout_median_ratio near 1 means estimates
// land where the measured parallaxes say they should; holdout_scatter_dex is how far a single one
// may be trusted, in decades of distance.
export interface Scene3DPhotometric {
  calibrated: boolean;
  reason?: string;
  pairs: number;
  rms?: number;
  holdout_n?: number;
  holdout_median_ratio?: number;
  holdout_scatter_dex?: number;
}

// Scene3DBillboard is one catalogued object placed at its distance. The footprint is in
// final-image pixels, already projected by the engine; the object's line of sight follows from that
// centre and the camera, so it is not shipped separately (and cannot disagree with the footprint).
export interface Scene3DBillboard {
  name: string;
  secondary?: string;
  type?: string;
  dist_pc: number;
  // "measured" = derived from this frame's own member stars; "table" = the catalogued value.
  dist_source: string;
  table_dist_pc?: number;
  members?: number;
  sigma_dex?: number;
  x: number;
  y: number;
  rx_px: number;
  ry_px: number;
  angle_rad: number;
  // How the object occupies space, when enough is known to say. Absent = the flat plane.
  shape?: Scene3DShape;
}

// Scene3DShape is the engine's decision about an object's three-dimensional form. `source` is the
// honesty tier and is always shown: "measured" means the geometry follows from catalogued numbers,
// "assumed" that the form is a standard assumption at a measured size, "modelled" that no
// measurement of the third dimension exists at all. `note` says so in words, `cite` names the
// published structure a curated shape follows.
export interface Scene3DShape {
  kind: "plane" | "disc" | "shell" | "volume";
  source: "measured" | "assumed" | "modelled";
  note: string;
  cite?: string;
  inclination_deg?: number;
  position_angle_deg?: number;
  // True whenever the near and far edges cannot be told apart — which, from an ellipse alone, always.
  flip_ambiguous?: boolean;
  radius_pc?: number;
  thickness_pc?: number;
  profile?: Scene3DVolumeProfile;
}

export interface Scene3DVolumeProfile {
  depth_pc: number;
  exponent: number;
  bowl?: number;
  hollow?: number;
}

export interface DetectedStar {
  x: number;
  y: number;
  // Half-max radius in final-image px — markers scale to this so a bloated bright star reads big and
  // a faint pinprick small, instead of every star wearing the same ring.
  r_px?: number;
  // The star's own colour, sampled from the linear master and lifted toward white so an outline in
  // it stays legible ("#a8c8ff" hot, "#ffcc99" cool). Absent for mono masters.
  hex?: string;
  // Estimated apparent magnitude (99 = the frame could not be photometrically anchored). Derived
  // from solve.mag_zero_point, itself fitted on the catalogue stars identified in this frame.
  mag?: number;
  // The catalogue entry this detection was identified as, when one projects onto it. Far more
  // detections carry this than carry a text label: labels are capped and spaced so the image stays
  // readable, but hovering any marker should still answer "what is this?".
  star?: StarCatalogInfo;
}

// PlanetaryFrame is one lucky-imaging frame's quality record (kept/rejected + sharpness score).
export interface PlanetaryFrame {
  index: number;
  file: string;
  filter?: string;
  score: number;
  kept: boolean;
}

// JobParams mirrors the POST /api/jobs body (also returned in Job.params).
export interface JobParams {
  path?: string;
  paths?: string[];
  mode?: string;
  format?: string;
  filter_map?: Record<string, string>;
  drop_wheel_transition?: boolean;
  color_calibration?: boolean;
  denoise?: boolean;
  ha_exclude_stars?: boolean;
  mosaic?: boolean; // legacy alias of union_canvas (multi-night, same pointing)
  union_canvas?: boolean;
  mosaic_plan_id?: number; // tiled-mosaic mode: the saved plan this run stacks
  // Extra monochrome side-outputs (deepsky/nebula): processed Luminance-only (default on) and the
  // combined all-channel integration (default off), saved next to the colour final.
  output_luminance?: boolean;
  output_mono_stack?: boolean;
  // Deep-sky colour palette (natural|hargb|hoo|sho|hos|foraxx|mono); empty/absent → natural.
  palette?: string;
  supervise?: boolean;
  // Gated deterministic star repair (deepsky/nebula; default on). The stretch_headroom knob it (and the
  // supervisor/refine) tunes rides in `params` below, like the other fine knobs.
  auto_fix_stars?: boolean;
  sequential?: boolean;
  live?: boolean;
  // Storage: "local" | "s3" (full-S3 free-local-after), plus the target bucket/prefix.
  storage_mode?: string;
  s3?: { bucket?: string; prefix?: string };
  low_disk?: boolean; // staged low-disk S3 processing (download/free one channel at a time)
  // Standalone S3 transfer/backup job (upload|sync|download|removeLocal). Mirrors the Go TransferRequest
  // JSON; the mirror destination base is `s3://<bucket>/<prefix>/<namespace>/<rel_path>`.
  transfer?: {
    op?: string;
    bucket?: string;
    prefix?: string;
    namespace?: string; // "data" | "output" (empty for external-drive copies)
    rel_path?: string;
    local_root?: string;
  };
  // Other intercept jobs (no pipeline recipe): backup/restore/move sub-requests, and the masters-only
  // calibration build flag (kind "masters").
  backup?: Record<string, unknown>;
  restore?: Record<string, unknown>;
  move?: Record<string, unknown>;
  build_masters?: boolean;
  // Milkyway (nightscape) run options.
  look?: string;
  brightness?: number;
  orientation?: string;
  dark_dir?: string;
  flat_dir?: string;
  bias_dir?: string;
  // Cross-session reuse toggles.
  reuse_disabled?: boolean;
  reuse_sessions?: string[];
  calib_exclude?: string[];
  // Light sets excluded by the Import stray-light check (inspect SetKey.ID tokens dropped
  // before grouping). Shown readonly on the job page.
  exclude_sets?: string[];
  // Force the available dark/flat/bias masters onto the lights even when gain/exposure/temperature don't
  // match (relaxes the calibration matcher). Default false = strict, physically-matched calibration.
  force_calibration_frames?: boolean;
  // Frozen snapshot of the calibration masters matched at queue time (which darks/flats/bias are included
  // and with what params) — shown on the job page. See CalibrationPanel (readonly).
  calib_plan?: CalibPreview;
  // Fine tunable-knob overrides (same whitelist/clamps as the supervisor) + the free-text objective
  // the agent carries, its re-entry ceiling and iteration cap.
  params?: Record<string, unknown>;
  goal?: string;
  // Imaging target for plate-solve/SPCC seeding — a catalogue name ("M66") or "RA,Dec" — for
  // captures whose headers/folders can't identify the field. Never renames the run.
  target?: string;
  tier?: string;
  max_iters?: number;
  // Agent improvement series this job belongs to (0/absent = none).
  series_id?: number;
}

// PresetPayload is the situation recipe a processing preset carries: the subset of the /api/jobs body a
// preset re-applies to the launch form (mirrors internal/preset.Payload). Input-specific fields (paths,
// calibration, reuse, S3, orientation) are deliberately absent — a preset is a recipe, not a run.
export interface PresetPayload {
  mode?: string;
  format?: string;
  palette?: string;
  look?: string;
  brightness?: string;
  goal?: string;
  color_calibration?: boolean;
  denoise?: boolean;
  ha_exclude_stars?: boolean;
  mosaic?: boolean;
  output_luminance?: boolean;
  output_mono_stack?: boolean;
  drop_wheel_transition?: boolean;
  supervise?: boolean;
  params?: Record<string, unknown>;
}

// PresetItem is one entry in the preset picker: a built-in (builtin=true, id=0, name is a slug the UI
// translates, category set) or a user-saved preset (builtin=false, name is the user's text). Mirrors
// internal/preset.Item.
export interface PresetItem {
  id: number;
  name: string;
  category?: string;
  builtin: boolean;
  favorite?: boolean; // user presets only — built-ins are never starred
  payload: PresetPayload;
  created_at?: number;
  updated_at?: number;
}

// JobResume is the pause/resume checkpoint (jobs.resume). Cause distinguishes a manual pause (stays
// paused until the user continues) from an error pause (auto-resumed with backoff).
export interface JobResume {
  phase?: string;
  cause?: "manual" | "error";
  attempts?: number;
  next_retry_ms?: number;
  reason?: string;
}

export interface Job {
  id: number;
  session_id: number;
  kind: string;
  status: string;
  progress: number;
  current_step: string;
  error: string;
  params?: JobParams;
  resume?: JobResume;
  log_tail?: string;
  result: RunResult;
  started_at_ms: number; // 0 until the job leaves the queue and starts processing
  finished_at_ms: number; // 0 until the job reaches a terminal state
  series_id?: number; // agent improvement series (0 = none)
  created_at: number;
  updated_at: number;
}

// Series is one durable agent improvement campaign over a target; each attempt is a normal job
// linked by jobs.series_id (GET /api/series, GET /api/series/{id}).
export interface Series {
  id: number;
  object: string;
  kind: string;
  input_path: string;
  goal: string;
  status: string; // active | done | stopped
  auto_continue: boolean;
  max_attempts: number;
  target_score: number;
  best_job_id: number;
  best_score: number;
  created_at: number;
  updated_at: number;
  attempts?: number; // attempt count — present on GET /api/series list rows only
}

export interface BrowseEntry {
  name: string;
  path: string;
  is_dir: boolean;
  local?: boolean; // present on local disk (only set when browsing with an S3 bucket)
  remote?: boolean; // present on the S3 mirror
  storage_class?: string; // S3 explorer: "" == STANDARD; e.g. GLACIER / DEEP_ARCHIVE / GLACIER_IR
  archived?: boolean; // S3 explorer: needs a Glacier restore before it can be downloaded
}

// S3 connection status (GET /api/s3/status). configured = credentials present in the env; reachable +
// buckets are filled when a connection test succeeds. conn_id names the default UI-managed connection
// (absent when the backend runs on env credentials) so the store can persist it beside bucket/prefix.
export interface S3Status {
  configured: boolean;
  endpoint?: string;
  reachable?: boolean;
  buckets?: string[];
  error?: string;
  conn_id?: number;
}

// One capture folder of a past processing (GET /api/processed). `exists` = usable (on local disk OR the
// S3 mirror); `local` = present on local disk. `exists && !local` means it was freed after an S3 push and
// must be pulled back from the mirror before it can be inspected/re-run. `rel` is the DataDir-relative slash
// path — the authoritative ledger key the mirror pull uses (a client-side rel guess misses nested folders).
export interface ProcessedPath {
  path: string;
  exists: boolean;
  local: boolean;
  rel: string;
}

// One past processing (a job) and the capture folders it consumed (GET /api/processed).
// `signature` is the backend-computed folder-set key (store.SelectionSignature) used to dedup
// history rows and join saved-selection names/stars; optional for old-backend tolerance.
export interface ProcessedGroup {
  job_id: number;
  kind: string;
  object?: string;
  mode?: string;
  format?: string;
  status: string;
  created_at_ms: number;
  signature?: string;
  paths: ProcessedPath[];
}

// A saved (named/starred) selection riding along GET /api/processed, with its folders annotated by
// the same existence machinery as the groups — an orphaned selection (jobs pruned) still renders.
export interface SavedSelectionInfo {
  id: number;
  name: string;
  favorite: boolean;
  signature: string;
  mode?: string;
  format?: string;
  updated_at_ms: number;
  paths: ProcessedPath[];
}

// Per-folder processing info derived client-side to annotate the folder browser. groupColor is set
// only when the folder's (most recent) processing spanned multiple folders, so siblings of one
// processing share a colour.
export interface ProcessedFolder {
  jobId: number;
  object?: string;
  kind: string;
  runs: number; // how many past jobs included this folder
  groupColor?: string;
  groupSize?: number;
}

// A de-duplicated past folder-set offered for re-running in the Import "Processing history". Several
// jobs over the same set collapse into one entry (runs counts them); the most recent supplies the rest.
// `selection` is the saved name/star joined by signature; `jobId: 0` marks an orphaned saved selection
// (its jobs aged out of the history window) synthesized from the saved row alone.
export interface ProcessingHistoryEntry {
  jobId: number;
  object?: string;
  mode?: string;
  format?: string;
  status: string;
  createdAtMs: number;
  runs: number;
  signature: string;
  selection?: { id: number; name: string; favorite: boolean };
  paths: ProcessedPath[];
}

// LogLine is one console line with the wall-clock time it was captured (null for legacy/untimed lines).
// seq is a monotonic id assigned at produce time so the log list keeps stable :key across ring-buffer
// trims (index keys would re-patch the whole list on every trimmed line).
export interface LogLine {
  ts: number | null;
  text: string;
  seq?: number;
}

// RunSummary is one durable on-disk run (GET /api/runs).
export interface RunSummary {
  object: string;
  run_id: string;
  dir: string;
  run_json: string;
  final_preview?: string;
  mode?: string;
  channels?: string[];
  created_at_ms: number;
  // Engine build that produced the run (from its run.json; absent when the summary predates stamping).
  engine?: string;
}

// Health is the GET /api/health snapshot; engine identifies the serving build ("dev" = un-stamped).
export interface EngineBuild {
  version: string;
  built_at: string;
}
export interface Health {
  status: string;
  data_dir: string;
  output_dir: string;
  library_dir: string;
  engine: EngineBuild;
}

// Environment health (GET /api/environment): deep per-tool probes + the offline plate-solve
// catalogue situation, with human-readable run-impacting warnings. Cached ~5 min server-side.
export interface EnvTool {
  ok: boolean;
  detail?: string; // version / resolved kind / probe state (may be "probing")
  err?: string;
}
export interface EnvPlateSolve {
  local_gaia_astro: boolean;
  xpsamp_chunks: number;
  local_asnet: boolean;
  catalog: string; // effective platesolve -catalog value ("" = Siril default/online)
}
export interface Environment {
  siril: EnvTool;
  gimp: EnvTool;
  graxpert: EnvTool;
  starnet: EnvTool;
  raw_developer: EnvTool;
  llm: EnvTool;
  plate_solve: EnvPlateSolve;
  checked_ms: number;
  warnings?: string[];
}

// Cross-session reuse: prior light data a run can fold in to grow total integration.
export interface ReuseSessionInfo {
  session_id: number;
  frames: number;
  integration_ms: number;
  filters: string[];
  // Distinct capture nights this prior session contributes ("YYYY-MM-DD", sorted).
  nights?: string[];
}

// ---- POST /api/calib/plan: the joined per-session run plan (pipeline.RunPlanPreview) ----

// PlanMaster is one master a group would use, with its provenance.
export interface PlanMaster {
  source: string; // "library" | "capture" | "session-rebuild"
  master?: Master;
  raw_flats?: number; // session-rebuild: how many raw flats will stack
  suggest_id?: string; // the calib_exclude key (same identity as the calibration preview)
}

// PlanGroup is one (session, night, config) calibration group and its masters (nil role = skipped).
export interface PlanGroup {
  session_id: number;
  current?: boolean;
  session?: string;
  exposure_ms: number;
  gain: number;
  offset: number;
  temp_bucket_c: number;
  bin: number;
  frames: number;
  dark?: PlanMaster;
  flat?: PlanMaster;
  bias?: PlanMaster;
  notes?: string[];
}

export interface PlanChannel {
  filter: string;
  groups: PlanGroup[];
}

// PlanSession is one (session, capture-night) contribution to the run, current capture first.
export interface PlanSession {
  session_id: number;
  current?: boolean;
  session?: string;
  frames: number;
  integration_ms: number;
  filters?: string[];
}

export interface RunPlanPreview {
  object: string;
  has_coords: boolean;
  sessions?: PlanSession[];
  channels: PlanChannel[];
  reuse: ReuseSummary;
  // The capture night whose canvas every channel master will be registered onto (grouped runs).
  anchor_night?: string;
  warnings?: string[];
}

export interface ReuseSummary {
  prior_sessions: number;
  prior_frames: number;
  added_integration_ms: number;
  sessions?: ReuseSessionInfo[];
}

export interface ReusePreview {
  object: string;
  has_coords: boolean;
  current_frames: number;
  current_integration_ms: number;
  reuse: ReuseSummary;
}

// --- Tonight's targets (GET /api/sky/targets) ---

export type SkyObjectType =
  | "galaxy"
  | "nebula"
  | "emission_nebula"
  | "planetary_nebula"
  | "reflection_nebula"
  | "dark_nebula"
  | "open_cluster"
  | "cluster"
  | "globular"
  | "supernova_remnant"
  | "other";

// Each sub-score is 0..1, higher = better — including `moon`, which is the multiplier (1 = no harm).
export interface SkySubScores {
  max_alt: number;
  alt_now: number;
  dark_hours: number;
  framing: number;
  detectability: number;
  moon: number;
  light_pollution: number; // multiplicative sky-glow factor (1 = pristine site)
  weather?: number; // live-conditions multiplier behind score_live (absent without forecast coverage)
}

export interface SkyFlags {
  detectability_known: boolean;
  framing_known: boolean;
  circumpolar: boolean;
  visible: boolean;
}

export interface AltSample {
  t_ms: number;
  alt_deg: number;
}

// Emission-line makeup for filter-wheel planning (strengths 0..1). Curated for popular targets, else
// derived from the object type (see `source`).
export interface SkyComposition {
  ha: number;
  oiii: number;
  sii: number;
  hb?: number;
  nii?: number;
  broadband: boolean;
  palette: string; // e.g. SHO / HOO / LRGB
  filters: string[]; // filters worth loading: L/R/G/B/Ha/OIII/SII
  source: string; // "curated" | "typical"
  note?: string;
}

export interface SkyTarget {
  name: string;
  aliases?: string[];
  catalog: string;
  type: SkyObjectType;
  common_name?: string; // friendly name from OpenNGC ("Fireworks Galaxy")
  morphology?: string; // Hubble class from OpenNGC
  ra_deg: number;
  dec_deg: number;
  alt_now_deg: number;
  az_now_deg: number;
  airmass_now: number;
  max_alt_deg: number;
  transit_utc_ms: number;
  transit_local: string;
  dark_hours_above_min: number;
  size_arcmin: number;
  size_minor_arcmin?: number; // minor axis → true ellipse with size_arcmin
  position_angle_deg?: number; // OpenNGC ellipse PA (deg E of N) — mosaic planner "align to object"
  mag_v: number;
  surface_brightness: number;
  fov_fill_pct: number; // NOT clamped: >100 means the target overflows the frame
  moon_sep_deg: number;
  score: number;
  // Weather-aware score (clear-sky score × the night's hourly observability over the target's
  // usable hours); absent when no forecast covers the selected night.
  score_live?: number;
  subscores: SkySubScores;
  flags: SkyFlags;
  reason: string;
  composition: SkyComposition;
  alt_series?: AltSample[];
  // Visual (eyepiece) mode: the recommended eyepiece and the view it gives (absent in camera mode).
  chosen_eyepiece?: string;
  eyepiece_focal_mm?: number;
  mag_x?: number;
  true_fov_deg?: number;
  exit_pupil_mm?: number;
}

export interface SkyMoonInfo {
  illum_fraction: number;
  alt_now_deg: number;
  up_now: boolean;
  phase: string; // i18n key suffix, e.g. "waxing_gibbous"
  rise_utc_ms: number; // 0 when no moonrise within tonight's chart window
  set_utc_ms: number;
  rise_local: string;
  set_local: string;
}

export interface SkySunInfo {
  set_utc_ms: number; // 0 when the sun doesn't set (e.g. polar summer)
  rise_utc_ms: number;
  set_local: string;
  rise_local: string;
}

export interface DarkWindow {
  kind: string; // astronomical | nautical | civil | best_effort
  no_astro_dark: boolean;
  dusk_utc_ms: number;
  dawn_utc_ms: number;
  dusk_local: string;
  dawn_local: string;
  dark_hours: number;
  night_start_ms: number; // night-chart window (≈ sunset−30m … sunrise+30m)
  night_end_ms: number;
  sun: SkySunInfo;
  moon: SkyMoonInfo;
  sun_series: AltSample[];
  moon_series: AltSample[];
}

export interface SkyLocation {
  lat: number;
  lon: number;
  elevation_m: number;
  timezone: string;
  source: string; // "config" | "query"
}

export interface SkyEyepiece {
  label: string;
  focal_mm: number;
  afov_deg: number;
}

export interface SkyEquipment {
  focal_mm: number;
  aperture_mm: number;
  pixel_um: number;
  sensor_w_px: number;
  sensor_h_px: number;
  image_scale_arcsec_px: number;
  fov_w_deg: number;
  fov_h_deg: number;
  f_ratio: number;
  barlow_x: number; // Barlow/amplifier factor folded into scale, FOV, f-ratio & magnification (1 = none)
  reducer_x: number; // focal reducer folded into the same values, independently of the Barlow (1 = none)
  mode?: "camera" | "visual"; // present only in eyepiece (visual) mode
  eyepieces?: SkyEyepiece[]; // the configured visual kit (visual mode)
}

// Polar-scope alignment: where the pole star sits on the reticle right now (GET /api/sky/polar).
export interface PolarResult {
  hemisphere: "north" | "south";
  pole_star_name: string;
  pole_star_ra_deg: number;
  pole_star_dec_deg: number;
  ha_deg: number;
  position_angle_deg: number; // reticle angle, clockwise from 12 o'clock (inverting scope)
  clock_hour: number; // position_angle_deg / 30, in [0,12)
  separation_deg: number; // pole star → true celestial pole
  alt_deg: number;
  az_deg: number;
  lst_deg: number;
  pole_star_visible: boolean;
  lat_too_low: boolean;
}

export interface PolarQueryEcho {
  at_utc_ms: number;
  at_local: string;
  location: SkyLocation;
}

export interface PolarResponse {
  query: PolarQueryEcho;
  result: PolarResult;
}

export interface SkyQueryEcho {
  at_utc_ms: number;
  at_local: string;
  location: SkyLocation;
  equipment: SkyEquipment;
  min_alt_deg: number;
  twilight: string;
  limit: number;
}

// SiteQuality is the artificial sky brightness at the observing site (the `site` field of the sky API).
export interface SiteQuality {
  sqm: number; // zenith brightness, mag/arcsec² (higher = darker)
  bortle: number; // 1 (pristine) … 9 (inner city)
  source: string; // "api" | "atlas" | "default"
  retrieved_ms: number;
}

export interface SkyTargetsResponse {
  query: SkyQueryEcho;
  darkness: DarkWindow;
  count: number;
  targets: SkyTarget[];
  site: SiteQuality;
  warnings?: string[];
}

// Address-search hit from GET /api/sky/geocode.
export interface GeoResult {
  label: string;
  lat: number;
  lon: number;
}

// A saved observing location (Tonight favorites), persisted in localStorage. id is a coordinate key so
// saving the same spot twice is idempotent; label is the place name (from search) or a "lat, lon" string.
export interface LocationFavorite {
  id: string;
  label: string;
  lat: number;
  lon: number;
  elevation_m?: number;
}

// A named, reusable telescope + camera + eyepiece rig the user can save and pick from later. Now
// persisted server-side (table equipment_setups, see stores/equipment.ts) so the desktop that plans a
// mosaic and the phone that shoots it agree on the optics; the id is the row id as a string. Numeric
// fields are optional so a partially-filled rig can still be saved.
export interface EquipmentSetup {
  id: string;
  name: string;
  focal_mm?: number;
  aperture_mm?: number;
  barlow?: number;
  reducer?: number;
  pixel_um?: number;
  sensor_w?: number;
  sensor_h?: number;
  camera_name?: string; // filled by "use connected camera" once a camera is attached
  eyepieces: SkyEyepiece[];
}

// EquipmentSetupRow is the wire shape of GET/POST /api/equipment. Field names match the plan-request
// optics (sensor_w_px/barlow_x); stores/equipment.ts projects it onto EquipmentSetup.
export interface EquipmentSetupRow {
  id: number;
  name: string;
  focal_mm: number;
  aperture_mm: number;
  pixel_um: number;
  sensor_w_px: number;
  sensor_h_px: number;
  barlow_x: number;
  reducer_x: number;
  camera_name: string;
  eyepieces: SkyEyepiece[];
  favorite: boolean;
  created_at: number;
  updated_at: number;
}

// Dark-sky finder (GET /api/sky/darksites): a ranked candidate observing site.
export interface DarkSiteHorizon {
  elevation_m: number;
  openness_pct: number; // % of azimuths with a clear (low) horizon
  max_obstruction_deg: number;
  worst_azimuth_deg: number;
  mean_obstruction_deg: number;
  south_obstruction_deg: number; // worst obstruction within ±90° of due south
  south_worst_azimuth_deg: number;
  south_openness_pct: number; // south-weighted openness (matters most for deep-sky)
  canopy_m: number; // tree/forest canopy height at the site (0 = clearing / no data)
  octants: number[]; // max obstruction angle per 45° sector (N, NE, … NW)
}
// The selected night's forecast at one candidate. sample_hours 0 means "no forecast" — which must
// never be read as "a bad night".
export interface DarkSiteWeather {
  start_ms: number;
  end_ms: number;
  sample_hours: number;
  score: number; // 0..100, moonlight-weighted
  cloud_pct: number;
  cloud_low_pct: number;
  cloud_high_pct: number;
  clear_hours: number;
  best?: { start_ms: number; end_ms: number; verdict: number };
  seeing_arcsec: number; // 0 = unknown
  seeing_source?: "derived" | "7timer";
  transparency: number; // 0..1; 0 = unknown
  dew_risk: "low" | "moderate" | "high" | "";
  min_temp_c: number;
  wind_kmh: number;
  elevation_m: number;
  deck_top_m?: number;
  flags?: string[]; // fog_risk | frost | above_inversion | beyond_horizon
}
// The normalised terms behind score, so the UI can re-blend as the slider moves without re-searching.
export interface DarkSiteSub {
  darkness: number;
  openness: number;
  weather: number;
  weather_known: boolean;
}
export interface DarkSite {
  lat: number;
  lon: number;
  sqm: number;
  bortle: number;
  distance_km: number; // straight-line (great-circle) distance
  drive_km?: number; // road distance from the observer (absent = not computed)
  drive_min?: number; // estimated driving time, minutes (absent = not computed)
  score: number;
  sub: DarkSiteSub;
  elevation_m?: number;
  horizon?: DarkSiteHorizon;
  weather?: DarkSiteWeather;
}
export interface DarkSkyConfidence {
  model: string;
  members: number;
  clear_members: number;
  agreement: number; // 0..1
  mean_cloud_pct: number;
  spread_pct: number;
}
export interface DarkSkyNight {
  index: number; // 0 = tonight
  start_ms: number;
  end_ms: number;
  kind: string; // astronomical | nautical | civil | best_effort
  dark_hours: number;
  moon_illum: number;
  moon_up_hours: number;
  confidence?: DarkSkyConfidence;
}
export interface DarkSitesResult {
  count: number;
  cells_scanned: number;
  night?: DarkSkyNight;
  weather_weight: number;
  candidates: DarkSite[];
  warnings: string[];
}

// Upcoming observing nights (GET /api/sky/nights) — the night picker's data.
export interface SkyNight {
  index: number;
  start_ms: number;
  end_ms: number;
  start_local: string;
  end_local: string;
  date_local: string;
  kind: string;
  dark_hours: number;
  moon_illum: number;
  moon_up_hours: number;
  moon_phase: string; // i18n key under tonight.moonPhase.*
  low_confidence: boolean;
}
export interface SkyNightsResult {
  nights: SkyNight[];
  timezone: string;
}

// --- Offline light-pollution atlas (GET/POST /api/sky/lightpollution/atlas) ---

export interface AtlasCoverage {
  present: boolean;
  min_lat: number;
  min_lon: number;
  max_lat: number;
  max_lon: number;
  rows: number;
  cols: number;
  unit: string;
  built_at_ms: number;
}

export interface AtlasBuildState {
  status: "idle" | "building" | "done" | "error";
  done: number;
  total: number;
  error?: string;
  coverage: AtlasCoverage;
}

export interface AtlasBuildRequest {
  region?: string; // "france" | "europe" | "world"
  min_lat?: number;
  min_lon?: number;
  max_lat?: number;
  max_lon?: number;
}

// --- Sky calendar (GET /api/sky/events) ---

export type SkyEventKind =
  | "solar_eclipse"
  | "lunar_eclipse"
  | "conjunction"
  | "opposition"
  | "elongation"
  | "planet_moon"
  | "moon_phase"
  | "supermoon"
  | "equinox"
  | "solstice"
  | "perihelion"
  | "aphelion"
  | "meteor_shower"
  | "comet"
  | "satellite_transit";

export type InstrumentTier = "naked_eye" | "binocular" | "telescope" | "none";

// How observable/spectacular an event is per instrument, each 0..100.
export interface EventVisibility {
  naked_eye: number;
  binocular: number;
  telescope: number;
}

// A named timing point inside an extended event (eclipse phases, transit ingress/egress).
export interface EventContact {
  label: string;
  utc_ms: number;
  alt_deg: number;
}

// One contributor to an event's score, for the "why this score" breakdown (weight 0..1).
export interface ScoreFactor {
  key: string; // rarity | altitude | sun_up | moon | brightness
  weight: number;
  detail?: string;
}

export interface SkyEvent {
  id: string;
  kind: SkyEventKind;
  subtype?: string;
  peak_utc_ms: number;
  start_utc_ms?: number;
  end_utc_ms?: number;
  duration_ms?: number;
  contacts?: EventContact[];
  bodies?: string[];
  title: string; // English fallback; the UI composes a localized title from the structured fields
  extra_text?: string;
  separation_deg?: number;
  magnitude?: number;
  has_mag: boolean;
  zhr?: number;
  score: number;
  visibility: EventVisibility;
  score_factors?: ScoreFactor[];
  instrument: InstrumentTier;
  notable: boolean;
  reason?: string;
  ra_deg?: number;
  dec_deg?: number;
  has_position: boolean;
  alt_at_best_deg: number;
  az_at_best_deg?: number;
  best_utc_ms?: number;
  moon_illum: number;
  moon_sep_deg?: number;
  in_path?: boolean; // satellite transit: the site is inside the narrow ground path
  night?: DarkWindow; // per-event night context for the detail altitude chart
}

export interface EventsQueryEcho {
  from_utc_ms: number;
  to_utc_ms: number;
  location: SkyLocation;
  equipment: SkyEquipment;
  twilight: string;
  limits: { naked_eye: number; binocular: number; telescope: number };
}

export interface EventsResponse {
  query: EventsQueryEcho;
  count: number;
  events: SkyEvent[];
  warnings?: string[];
}

// --- GoTo alignment helper (/goto) ---

// GotoReason is one structured pick justification (mirrors align.Reason): the UI renders
// t(`goto.reasons.${code}`) with the params below (only the ones the code uses are present).
export interface GotoReason {
  code:
    | "very_bright"
    | "bright"
    | "naked_eye"
    | "low"
    | "high_overhead"
    | "well_placed"
    | "spread";
  mag?: number;
  alt?: number;
  deg?: number;
}

// GotoWarning is one structured soft plan warning (mirrors align.Warning): the UI renders
// t(`goto.warnings.${code}`); every param is optional — each code carries exactly what it uses.
export interface GotoWarning {
  code: "few_stars" | "calib_same_side";
  available?: number;
  requested?: number;
  min_alt?: number;
  max_alt?: number;
  side?: "east" | "west";
  count?: number;
}

export interface GotoStar {
  name: string;
  constellation: string;
  ra_deg: number;
  dec_deg: number;
  mag: number;
  alt_deg: number;
  az_deg: number;
  compass: string; // 16-point direction of the azimuth (e.g. "SE")
  hour_angle_deg: number;
  meridian_side: "east" | "west";
  order: number;
  status: "accepted" | "recommended" | "upcoming";
  phase?: "align" | "calibration"; // two-phase routines (Celestron EQ); absent on single-phase
  hc_name?: string; // the exact hand-controller label (profiles with a star list)
  suitability: number;
  reasons: GotoReason[];
}

// GotoProfile mirrors the backend align.Profile registry entry (GET /api/sky/align/profiles):
// count bounds, phase structure and the hand-controller star-list key. Labels stay i18n-side.
export interface GotoProfile {
  key: string;
  label: string;
  mount_type: "eq" | "altaz";
  min_alt_deg: number;
  max_alt_deg: number;
  default_stars: number;
  min_stars: number;
  max_stars: number;
  mag_limit: number;
  same_meridian_side: boolean;
  avoid_meridian_deg: number;
  zenith_bias: number;
  star_list?: string;
  align_stars?: number;
  calib_opposite_side?: boolean;
  note: string;
}

// SkyBody is the Moon or a naked-eye planet currently above the horizon, a landmark for the sky map.
export interface SkyBody {
  name: string; // lowercase key: "moon" | "venus" | … (i18n goto.sky.bodies.*)
  kind: "moon" | "planet";
  ra_deg: number;
  dec_deg: number;
  alt_deg: number;
  az_deg: number;
  mag: number;
  phase?: number; // Moon illuminated fraction 0..1
}

export interface GotoResult {
  hemisphere: "north" | "south";
  profile: string;
  mount_type: "eq" | "altaz";
  meridian_side: "east" | "west" | "any";
  stars: GotoStar[];
  quality_score: number;
  warnings: GotoWarning[];
  // Moon + naked-eye planets currently up, for the sky map's landmarks.
  sky_bodies?: SkyBody[];
}

export interface GotoQueryEcho {
  at_utc_ms: number;
  at_local: string;
  location: SkyLocation;
  profile: string;
  count: number;
}

export interface GotoResponse {
  query: GotoQueryEcho;
  result: GotoResult;
}

// --- Astronomy weather (/tonight) ---

export interface WeatherHour {
  t_ms: number;
  cloud_pct: number;
  cloud_low: number;
  cloud_mid: number;
  cloud_high: number;
  seeing_arcsec: number; // 0 = unknown
  transparency: number; // 0..1 (1 = pristine); 0 = unknown
  humidity_pct: number;
  dew_point_c: number;
  temp_c: number;
  dew_spread_c: number;
  dew_risk: "low" | "moderate" | "high";
  wind_kmh: number;
  gust_kmh: number;
  jet300_kmh: number;
  cape: number;
  lifted_index: number;
  visibility_m: number;
  precip_pct: number;
  aod: number; // 0 = unknown
  verdict: number; // 0..100 observability
}

export interface WeatherWindow {
  start_ms: number;
  end_ms: number;
  verdict: number;
}

export interface WeatherKp {
  now: number;
  max: number;
  aurora: "unlikely" | "possible" | "likely";
  issued_ms: number;
}

export interface SiteForecast {
  lat: number;
  lon: number;
  issued_ms: number;
  hours: WeatherHour[];
  best?: WeatherWindow | null;
  kp?: WeatherKp | null;
  sources: string[];
}

export interface WeatherResponse {
  query: { at_utc_ms: number; at_local: string; location: SkyLocation };
  forecast: SiteForecast;
  warning?: string;
}

export interface WeatherGrid {
  bbox: [number, number, number, number]; // [west, south, east, north]
  nx: number;
  ny: number;
  timesteps: number[]; // epoch ms per frame
  layers: Record<string, number[][]>; // layer → [timestep][cell] (row-major, ny rows × nx cols)
  issued_ms: number;
}

export interface WeatherGridResponse {
  grid: WeatherGrid;
  warning?: string;
}

// WeatherFrames is the animated cube's time axis + coverage WITHOUT the float data — the small payload the
// map fetches to drive the scrubber and build server-rendered tile URLs (the tiles carry the heavy data).
export interface WeatherFrames {
  bbox: [number, number, number, number]; // [west, south, east, north]
  timesteps: number[]; // epoch ms per frame
  issued_ms: number;
  warning?: string;
}

// ---- Mosaic planner (mirrors internal/mosaicplan + /api/mosaic wire shapes) ----

export interface MosaicTile {
  index: number; // row*cols+col — stable identity for capture-status keys
  row: number;
  col: number;
  order: number; // 1-based serpentine capture order
  folder: string; // "p01"… — the panel-subfolder convention
  ra_deg: number;
  dec_deg: number;
  corners: [number, number][]; // [ra,dec] × TL,TR,BR,BL in frame orientation
  alt_deg: number;
  az_deg: number;
  transit_utc_ms: number;
  meridian_side: "east" | "west";
}

export interface MosaicGrid {
  rows: number;
  cols: number;
  tile_w_deg: number;
  tile_h_deg: number;
  step_w_deg: number;
  step_h_deg: number;
  camera_pa_deg: number;
  overlap_frac: number;
}

export interface MosaicQueryEcho {
  target?: string;
  ra_deg: number;
  dec_deg: number;
  size_arcmin: number;
  size_minor_arcmin?: number;
  object_pa_deg?: number;
  center_ra_deg: number; // effective grid centre (the object unless hand-framed)
  center_dec_deg: number;
  center_moved?: boolean;
  fov_w_deg: number;
  fov_h_deg: number;
  image_scale_arcsec_px: number;
  margin_arcmin: number;
  lat: number;
  lon: number;
  at_utc_ms: number;
}

export interface MosaicPreview {
  query: MosaicQueryEcho;
  grid: MosaicGrid;
  tiles: MosaicTile[];
  warnings?: string[];
}

// SkySearchResult is one hit from GET /api/sky/search — free-text lookup over the WHOLE merged
// deep-sky catalogue (not just tonight's visible list). Optional fields are absent when the
// catalogue has no value, so "unknown size" is distinguishable from a genuine 0.
export interface SkySearchResult {
  name: string;
  ra_deg: number;
  dec_deg: number;
  type?: string;
  source?: string;
  size_arcmin?: number;
  size_minor_arcmin?: number;
  position_angle_deg?: number;
  mag?: number;
  morphology?: string;
  common_names?: string[];
  aliases?: string[];
}

// MosaicRequestBody is what the UI sends (preview + plan create/update). Absent fields fall back
// to catalogue values (for a named target) or the engine's configured rig/site.
export interface MosaicRequestBody {
  target_name?: string;
  ra_deg?: number;
  dec_deg?: number;
  center_ra_deg?: number; // hand-framed grid centre (dragged on the map); both or neither
  center_dec_deg?: number;
  size_arcmin?: number;
  size_minor_arcmin?: number;
  object_pa_deg?: number;
  optics?: {
    focal_mm?: number;
    aperture_mm?: number;
    pixel_um?: number;
    sensor_w_px?: number;
    sensor_h_px?: number;
    barlow_x?: number;
    reducer_x?: number;
  };
  overlap_frac?: number;
  margin_arcmin?: number;
  camera_pa_deg?: number;
  rows_override?: number;
  cols_override?: number;
  lat?: number;
  lon?: number;
  at?: string; // RFC3339
}

// MosaicPlanRequest is the server's resolved snapshot stored on a saved plan (Go mosaicplan.Request).
export interface MosaicPlanRequest {
  ra_deg: number;
  dec_deg: number;
  size_arcmin: number;
  size_minor_arcmin: number;
  object_pa_deg: number;
  has_object_pa: boolean;
  center_ra_deg: number;
  center_dec_deg: number;
  has_center: boolean;
  optics: {
    focal_mm: number;
    aperture_mm: number;
    pixel_um: number;
    sensor_w_px: number;
    sensor_h_px: number;
    barlow_x: number;
    reducer_x: number;
  };
  overlap_frac: number;
  margin_arcmin: number;
  camera_pa_deg: number;
  rows_override: number;
  cols_override: number;
  lat: number;
  lon: number;
  at: string;
}

export type MosaicTileStatus = "pending" | "captured" | "skipped";

// What one panel+filter actually holds on disk, reconciled from the frames themselves
// (POST /api/mosaic/plans/{id}/reconcile → internal/mosaic/progress.go).
export interface MosaicFilterProgress {
  frames: number;
  seconds: number;
  last_ms?: number;
  nights?: number;
}

// panel folder ("p01") → filter ("L") → tally.
export type MosaicTileProgress = Record<
  string,
  Record<string, MosaicFilterProgress>
>;

// The per-filter goal for every tile: what makes a tile "done" without the user ticking a box.
export interface MosaicCaptureTarget {
  filter: string;
  frames: number;
  exposure_ms?: number;
  gain?: number;
  offset?: number;
  bin?: number;
  dither?: number;
}

export interface MosaicPlanRow {
  id: number;
  name: string;
  object_name: string;
  request: MosaicPlanRequest;
  grid: MosaicGrid;
  tiles: MosaicTile[];
  tile_status: Record<string, MosaicTileStatus>;
  capture_targets: MosaicCaptureTarget[];
  tile_progress: MosaicTileProgress;
  capture_root?: string;
  reconciled_at?: number;
  orientation_done: boolean;
  created_at: number;
  updated_at: number;
}

export interface StarfieldStar {
  ra_deg: number;
  dec_deg: number;
  mag: number;
}

// --- Capture subsystem (camera / filter wheel / mount + the auto-run sequencer) --------------------

// DeviceInfo is one entry of GET /api/device/devices: a device a driver can actually see. The
// simulator contributes one of each unconditionally; a real device appears only once its driver
// found it, which is what makes "is anything actually plugged in?" answerable from the UI.
export interface DeviceInfo {
  id: string;
  name: string;
  driver: string;
  kind: string;
}

// DeviceStatus is GET /api/device/status: whether the separate device-server process is up.
export interface DeviceStatus {
  running: boolean;
  addr: string;
  error?: string;
  health?: {
    ok: boolean;
    drivers: {
      name: string;
      kind: string;
      available: boolean;
      detail?: string;
    }[];
    connected: Record<string, boolean>;
  };
}

// DeviceControl is one camera parameter as the DRIVER reports it — ranges come from the hardware,
// never from hardcoded UI constants.
export interface DeviceControl {
  name: string;
  label: string;
  min: number;
  max: number;
  default: number;
  value: number;
  writable: boolean;
  auto_supported: boolean;
  auto: boolean;
  unit?: string;
  description?: string;
  scale_divisor?: number;
}

export interface DeviceCameraCaps {
  id: string;
  name: string;
  driver: string;
  kind: string;
  max_width: number;
  max_height: number;
  pixel_size_um: number;
  bit_depth: number;
  is_color: boolean;
  has_cooler: boolean;
  has_shutter: boolean;
  bins: number[];
  image_types: string[];
  serial_number?: string;
  sdk_version?: string;
  min_exposure_us: number;
  max_exposure_us: number;
}

export interface DeviceROI {
  x: number;
  y: number;
  width: number;
  height: number;
  bin: number;
  format: string;
}

export interface DeviceCameraState {
  connected: boolean;
  caps?: DeviceCameraCaps;
  controls?: DeviceControl[];
  roi?: DeviceROI;
  exposure?: string;
  streaming?: boolean;
  dropped?: number;
}

export interface DeviceWheelState {
  connected: boolean;
  wheel?: {
    id: string;
    name: string;
    // Which driver is behind it — "efw" for real hardware, "sim" for the simulator. The device
    // server has always sent this; it was missing here, so the UI could not tell a simulated wheel
    // from a real one.
    driver?: string;
    kind?: string;
    slots: number;
    position: number;
    moving: boolean;
    names?: string[];
  };
}

// MountLinkHealth is what the serial link knows about itself. It is absent for the simulator, which
// has no link — reporting invented latency there would make the panel lie in exactly the situation
// where somebody is trying to tell simulation from hardware.
export interface MountLinkHealth {
  connected: boolean;
  reconnecting: boolean;
  path: string;
  model?: string;
  firmware?: string;
  uptime_ms: number;
  last_reply_ago_ms: number;
  commands: number;
  errors: number;
  retries: number;
  resyncs: number;
  desyncs: number;
  reconnects: number;
  unrecovered: number;
  latency_p50_ms: number;
  latency_p99_ms: number;
  latency_max_ms: number;
  last_error?: string;
}

export interface MountDiagnosis {
  verdict: string;
  detail: string;
  chip?: string;
  ports: { path: string; label: string; likely: boolean }[];
  scan_error?: string;
}

// What is actually stored inside the mount, read back over the serial link.
//
// The split into hand controller and motor controllers is not cosmetic. Site, clock, tracking mode
// and the alignment model live in the hand controller; the periodic-error table and the autoguide
// rates live on the motor boards, one per axis, and survive a hand controller being swapped. That is
// why a "factory reset" clears different things depending on which firmware is asked.
export interface MountAudit {
  at_ms: number;
  port?: string;
  identity: {
    model: string;
    model_code: number;
    firmware: string;
    ra_motor_firmware?: string;
    dec_motor_firmware?: string;
    motor_err?: string;
  };
  site: { read: boolean; lat_deg: number; lon_deg: number; err?: string };
  clock: {
    read: boolean;
    utc: string;
    offset_hours: number;
    dst: boolean;
    skew_sec: number;
    err?: string;
  };
  drive: {
    read: boolean;
    tracking: boolean;
    tracking_rate: string;
    aligned: boolean;
    pier_side?: string;
    err?: string;
  };
  guide: {
    read: boolean;
    ra_units: number;
    dec_units: number;
    ra_fraction: number;
    dec_fraction: number;
    // Whether the declination motor could be read separately at all — the simulator, and any driver
    // exposing only the single-axis rate, answers for right ascension alone.
    both_axes: boolean;
    mismatch: boolean;
    err?: string;
  };
  pec: {
    supported: boolean;
    read: boolean;
    err?: string;
    bins: number;
    worm_period_sec: number;
    bin_sec: number;
    lsb_arcsec_per_sec: number;
    indexed: boolean;
    current_bin: number;
    curve?: number[];
    all_zero: boolean;
    peak_units: number;
    peak_rate_arcsec_per_sec: number;
    swing_arcsec: number;
    net_arcsec_per_rev: number;
    // What the driver last COMMANDED, not a reading: the protocol cannot be asked whether the mount
    // is replaying its table.
    playback_commanded: boolean;
  };
  link?: MountLinkHealth;
  notes?: string[];
}

export interface MountRestoreAction {
  item: string;
  detail: string;
  applied: boolean;
  err?: string;
}

export interface MountRestoreResult {
  dry_run: boolean;
  backup_path?: string;
  before: MountAudit;
  after?: MountAudit;
  actions: MountRestoreAction[];
}

export interface DeviceMountState {
  connected: boolean;
  reconnecting?: boolean;
  cached?: boolean;
  error?: string;
  link?: MountLinkHealth;
  mount?: {
    id: string;
    name: string;
    // Which driver is answering — "sim" or "nexstar". The panels branch on it: the simulator has no
    // site or clock to set, so being connected is not the same as being connected to a telescope.
    driver?: string;
    ra_deg: number;
    dec_deg: number;
    alt_deg: number;
    az_deg: number;
    slewing: boolean;
    tracking: boolean;
    tracking_rate?: string;
    aligned: boolean;
    pier_side?: string;
    firmware?: string;
    model?: string;
  };
}

// LiveStats is one live frame's summary — the numbers next to the image, computed server-side so
// the browser receives a few hundred bytes instead of 32 MB.
export interface LiveStats {
  min: number;
  max: number;
  mean: number;
  median: number;
  std_dev: number;
  saturated_pct: number;
  histogram: number[];
  bins: number;
  auto_lo: number;
  auto_hi: number;
  width: number;
  height: number;
  exposure_us: number;
  gain: number;
  temp_milli_c: number;
  has_temp: boolean;
  focus?: LiveFocus;
}

// LiveFocus is the focus meter's verdict: how sharp, how far out, and which way to turn.
export interface LiveFocus {
  score: number;
  hfd_px: number;
  hfd_arcsec?: number;
  stars: number;
  saturated: boolean;
  reliable: boolean;
  distance_um?: number;
  turns?: number;
  advice?: string;
  best_hfd_px?: number;
  tilt_corners?: number[];
}

// CaptureStep is one block of the auto-run: N frames through one filter at one setting.
export interface CaptureStep {
  filter: string;
  slot?: number;
  count: number;
  exposure_us: number;
  gain?: number;
  offset?: number;
  bin?: number;
  type?: string;
  dither_n?: number;
  dither_px?: number;
}

export interface CaptureSequence {
  name?: string;
  steps: CaptureStep[];
  interleave?: boolean;
  repeat_block?: number;
}

export interface CaptureProgress {
  session_id: number;
  status: "idle" | "running" | "paused" | "completed" | "aborted" | "failed";
  step_index: number;
  frame_index: number;
  total_frames: number;
  current_filter?: string;
  exposure_us?: number;
  exposure_ends?: string;
  last_path?: string;
  message?: string;
  error?: string;
  started_at?: string;
  eta_seconds?: number;
  captured?: Record<string, number>;
}

// ConditionStat is the spread of one weather metric over a session. n is how many samples actually
// carried a value, so a median drawn from two readings out of twelve is not shown as a solid one.
export interface ConditionStat {
  min: number;
  median: number;
  max: number;
  n: number;
}

// ConditionsSummary is the rolled-up sky record stored on the session (Go: skylog.Summary).
export interface ConditionsSummary {
  samples: number;
  first_ms: number;
  last_ms: number;
  cloud_pct: ConditionStat;
  cloud_low: ConditionStat;
  cloud_mid: ConditionStat;
  cloud_high: ConditionStat;
  seeing_arcsec: ConditionStat;
  transparency: ConditionStat;
  humidity_pct: ConditionStat;
  dew_spread_c: ConditionStat;
  temp_c: ConditionStat;
  wind_kmh: ConditionStat;
  gust_kmh: ConditionStat;
  precip_pct: ConditionStat;
  aod: ConditionStat;
  verdict: ConditionStat;
  moon_illum_max: number;
  moon_alt_max_deg: number;
  moon_up: boolean;
  moon_sep_min_deg: number;
  moon_phase_angle_deg: number;
  target_valid: boolean;
  target_alt_min_deg: number;
  target_alt_max_deg: number;
  target_airmass_min: number;
  sqm: number;
  bortle: number;
  dew_risk_worst: string;
  kp_max: number;
  aurora_max: string;
  source_counts: Record<string, number>;
}

// CaptureConditionRow is one hourly observation of the sky over a running session. Zero means "the
// feed did not supply it" — the same contract the weather API uses — except for `source`, which says
// whether there was any weather at all.
export interface CaptureConditionRow {
  id: number;
  session_id: number;
  at_ms: number;
  session_status: string;
  cloud_pct: number;
  cloud_low: number;
  cloud_mid: number;
  cloud_high: number;
  seeing_arcsec: number;
  transparency: number;
  humidity_pct: number;
  dew_point_c: number;
  temp_c: number;
  dew_spread_c: number;
  dew_risk: string;
  wind_kmh: number;
  gust_kmh: number;
  jet300_kmh: number;
  cape: number;
  lifted_index: number;
  visibility_m: number;
  precip_pct: number;
  aod: number;
  verdict: number;
  kp_now: number;
  kp_max: number;
  aurora: string;
  moon_illum: number;
  moon_alt_deg: number;
  moon_az_deg: number;
  moon_phase_angle_deg: number;
  moon_sep_deg: number;
  target_alt_deg: number;
  target_az_deg: number;
  target_airmass: number;
  target_valid: boolean;
  sqm: number;
  bortle: number;
  forecast_age_ms: number;
  source: "live" | "cached" | "unavailable" | string;
  created_at: number;
}

// CaptureForecastRow is the whole hourly forecast as it stood at one end of the session, kept so
// "what was forecast vs what actually happened" stays answerable.
export interface CaptureForecastRow {
  kind: "start" | "end" | string;
  at_ms: number;
  payload: SiteForecast;
}

// CaptureFrame is one written exposure, as recorded by the sequencer. sequence_no is the order it
// was taken in — which is what makes "did the L set finish before the cloud came in?" answerable.
export interface CaptureFrame {
  id: number;
  session_id: number;
  path: string;
  filter: string;
  frame_type: string;
  exposure_us: number;
  gain: number;
  frame_offset: number;
  bin: number;
  temp_milli_c: number;
  panel: string;
  sequence_no: number;
  started_at: number;
  created_at: number;
}

// CaptureFrameStat is one filter/type bucket of a session's frames, aggregated by the engine.
export interface CaptureFrameStat {
  filter: string;
  frame_type: string;
  frames: number;
  total_exposure_us: number;
  min_exposure_us: number;
  max_exposure_us: number;
  min_gain: number;
  max_gain: number;
  min_bin: number;
  max_bin: number;
  min_temp_milli_c: number;
  max_temp_milli_c: number;
  avg_temp_milli_c: number;
  first_ms: number;
  last_ms: number;
}

export interface CaptureSessionRow {
  id: number;
  object: string;
  root: string;
  panel: string;
  mosaic_plan_id: number;
  tile_index: number;
  sequence: CaptureSequence;
  status: string;
  progress: CaptureProgress;
  total_frames: number;
  frames_done: number;
  started_at: number;
  ended_at: number;
  // Where the telescope stood, and the sky it stood under. Both are 0 / empty for sessions captured
  // before the logbook shipped — they cannot be backfilled, because the weather feeds have no archive.
  site_lat: number;
  site_lon: number;
  site_elevation_m: number;
  conditions_summary: ConditionsSummary | Record<string, never>;
}

// --- Polar alignment from the live camera (/api/capture/polar) ---------------------------------
//
// Mirrors internal/capture.PolarState. The reticle types above (PolarResult) are the OPEN-LOOP
// calculation for a polar scope; these are the measured ones, from frames the telescope actually took.

export type PolarCamPhase =
  | "idle"
  | "measuring"
  | "solved"
  | "adjusting"
  | "failed";

export interface PolarCamSample {
  index: number;
  ra_deg: number;
  dec_deg: number;
  at: string;
  scale_arcsec_px: number;
}

export interface PolarCamAxis {
  alt_deg: number;
  az_deg: number;
  radius_deg: number;
  arc_deg: number;
  residual_arcsec: number;
  /** One-sigma uncertainty in the axis's worst-constrained direction. */
  sigma_arcsec: number;
  samples: number;
  warnings?: string[];
}

export interface PolarCamCorrection {
  /** Positive means the polar axis points too HIGH. */
  alt_error_deg: number;
  /** Positive means it lies EAST of the pole's meridian, as a true angle on the sky. */
  az_error_deg: number;
  /** The same error as the AZIMUTH the adjuster turns through — bigger by 1/cos(altitude). */
  az_knob_deg: number;
  total_arcmin: number;
  /** "raise" | "lower" | "ok" — which way to turn the altitude adjuster. */
  alt_move: string;
  /** "east" | "west" | "ok" — which way to move the axis. */
  az_move: string;
  /** "excellent" | "good" | "fair" | "poor" */
  quality: string;
}

export interface PolarCamTarget {
  ra_deg: number;
  dec_deg: number;
  /** Sensor pixels, in the FITS axis frame. */
  x: number;
  y: number;
  /** The same point as a fraction of the frame, which is what an overlay needs. */
  nx: number;
  ny: number;
  offset_px: number;
  /** True when the marker falls outside the image — normal on a first measurement. */
  off_frame: boolean;
  offset_arcmin: number;
}

export interface PolarCamLive {
  target: PolarCamTarget;
  remaining_arcmin: number;
  quality: string;
  /** The mount appears to have been moved, so the measurement behind the marker is stale. */
  suspect?: boolean;
}

export interface PolarCamPoleView {
  /** Where the celestial pole falls on the frame — where the telescope has to be aimed. */
  pole: PolarCamTarget;
  /** Polaris, or σ Octantis below the equator: what the eye is looking for. */
  star: PolarCamTarget;
  star_name: string;
  star_visible: boolean;
}

export interface PolarCamState {
  phase: PolarCamPhase;
  step: number;
  points: number;
  step_arc_deg: number;
  samples: PolarCamSample[];
  axis?: PolarCamAxis;
  correction?: PolarCamCorrection;
  live?: PolarCamLive;
  pole?: PolarCamPoleView;
  /** "measured" from a rotation (arcminute class) or "rough" from one frame (polar-scope class). */
  mode?: "measured" | "rough";
  /** Codes the UI translates, never English. */
  warnings?: string[];
  error?: string;
  busy: boolean;
  tracking: boolean;
}

// --- the solar system (GET /api/solarsystem/*) ---------------------------------------------------
// Mirrors internal/solarsystem. The browser propagates these elements itself so the time scrubber
// costs no round trips; utils/solarsystem.ts is the propagator and solarsystem.spec.ts pins it to
// the engine's own numbers.

export type SolarKind = "star" | "planet" | "moon" | "dwarf" | "comet";

/** How a body's POSITION is obtained. Its physical facts are measurements regardless. */
export type SolarTier = "fitted" | "mean" | "sampled";

export interface SolarLibration {
  arg0_deg: number;
  arg_dot: number;
  ra_amp_deg: number;
  dec_amp_deg: number;
  w_amp_deg: number;
}

/** IAU rotational elements: where the north pole points, and how far the prime meridian has turned. */
export interface SolarPole {
  ra0_deg: number;
  ra_dot?: number;
  dec0_deg: number;
  dec_dot?: number;
  w0_deg: number;
  /** Degrees per day; negative for a retrograde rotator. */
  w_dot: number;
  libration?: SolarLibration;
}

export interface SolarRing {
  inner_km: number;
  outer_km: number;
  texture?: string;
  faint: boolean;
  source: string;
}

/** One orbit, as elements plus per-day rates from an epoch. Distances are AU, angles degrees. */
export interface SolarOrbitSpec {
  centre: string;
  frame: "ecliptic" | "laplace";
  pole_ra_deg?: number;
  pole_dec_deg?: number;
  epoch_jd: number;
  a_au: number;
  a_dot?: number;
  e: number;
  e_dot?: number;
  i_deg: number;
  i_dot?: number;
  node_deg: number;
  node_dot?: number;
  peri_deg: number;
  peri_dot?: number;
  m_deg: number;
  n_deg: number;
  period_days: number;
}

export interface SolarBody {
  key: string;
  kind: SolarKind;
  parent?: string;
  radius_km: number;
  polar_radius_km?: number;
  mass_kg?: number;
  albedo?: number;
  colour: string;
  pole: SolarPole;
  ring?: SolarRing;
  orbit?: SolarOrbitSpec;
  /** A built-in analytic model, for bodies no fixed element set describes well (the Moon). */
  series?: string;
  texture?: string;
  tier: SolarTier;
  source: string;
}

export interface SolarSource {
  name: string;
  covers: string;
  licence: string;
  url?: string;
}

export interface SolarManifest {
  version: number;
  engine: string;
  range_from: number;
  range_to: number;
  au_per_km: number;
  bodies: SolarBody[];
  /** Texture keys this engine actually has on disk; anything absent is shaded procedurally. */
  textures: string[];
  sources: SolarSource[];
}

/** One body at one instant, computed by the engine — the numbers the info panel prints. */
export interface SolarBodyState {
  key: string;
  kind: SolarKind;
  helio_au: [number, number, number];
  local_au?: [number, number, number];
  helio_dist_au: number;
  geo_dist_au: number;
  ra_deg: number;
  dec_deg: number;
  alt_deg: number;
  az_deg: number;
  up: boolean;
  airmass?: number;
  magnitude: number;
  angular_diameter_arcsec: number;
  phase_angle_deg: number;
  illum_fraction: number;
  elongation_deg: number;
  orientation: { pole_ra_deg: number; pole_dec_deg: number; w_deg: number };
  axial_tilt_deg: number;
  /** Ring-plane tilt toward Earth; 0 is edge-on. Saturn only, for now. */
  ring_open_deg?: number;
}

export interface SolarSnapshot {
  time_ms: number;
  jd: number;
  site: { lat_deg: number; lon_deg: number; elevation_m?: number };
  bodies: SolarBodyState[];
}
