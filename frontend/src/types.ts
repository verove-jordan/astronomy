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
  object?: string;
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

// Calibration suggestions (POST /api/calib/preview): per inspected light channel, the library master
// dark/flat/bias that would be applied. `id` is the per-(channel,role) key sent back to exclude one.
export interface CalibSuggestion {
  id: string;
  role: string; // "dark" | "flat" | "bias"
  master: Master;
}
export interface CalibChannel {
  filter: string;
  exposure_ms: number;
  gain: number;
  offset: number;
  temp_bucket_c: number;
  bin: number;
  suggestions: CalibSuggestion[];
  notes?: string[];
}
export interface CalibPreview {
  channels: CalibChannel[];
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
  error?: string;
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
  png_path: string;
}

export interface FinalResult {
  mode: string;
  channels: string[];
  outputs: string[];
  notes?: string[];
  iterations?: IterationRecord[];
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
  // Frozen snapshot of the calibration masters matched at queue time (which darks/flats/bias are included
  // and with what params) — shown on the job page. See CalibrationPanel (readonly).
  calib_plan?: CalibPreview;
  // Fine tunable-knob overrides (same whitelist/clamps as the supervisor) + the free-text objective
  // the agent carries, its re-entry ceiling and iteration cap.
  params?: Record<string, unknown>;
  goal?: string;
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
export interface ProcessedGroup {
  job_id: number;
  kind: string;
  object?: string;
  mode?: string;
  format?: string;
  status: string;
  created_at_ms: number;
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
export interface ProcessingHistoryEntry {
  jobId: number;
  object?: string;
  mode?: string;
  format?: string;
  status: string;
  createdAtMs: number;
  runs: number;
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
  mag_v: number;
  surface_brightness: number;
  fov_fill_pct: number; // NOT clamped: >100 means the target overflows the frame
  moon_sep_deg: number;
  score: number;
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

// A named, reusable telescope + camera + eyepiece rig the user can save and pick from later (persisted
// locally, like LocationFavorite). Numeric fields are optional so a partially-filled rig can still be saved.
export interface EquipmentSetup {
  id: string;
  name: string;
  focal_mm?: number;
  aperture_mm?: number;
  barlow?: number;
  pixel_um?: number;
  sensor_w?: number;
  sensor_h?: number;
  eyepieces: SkyEyepiece[];
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
export interface DarkSite {
  lat: number;
  lon: number;
  sqm: number;
  bortle: number;
  distance_km: number; // straight-line (great-circle) distance
  drive_km?: number; // road distance from the observer (absent = not computed)
  drive_min?: number; // estimated driving time, minutes (absent = not computed)
  score: number;
  elevation_m?: number;
  horizon?: DarkSiteHorizon;
}
export interface DarkSitesResult {
  count: number;
  cells_scanned: number;
  candidates: DarkSite[];
  warnings: string[];
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
  reasons: string[];
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
  warnings: string[];
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
