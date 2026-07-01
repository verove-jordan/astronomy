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
  error?: string;
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
  mode?: string;
  format?: string;
  filter_map?: Record<string, string>;
  drop_wheel_transition?: boolean;
  color_calibration?: boolean;
  denoise?: boolean;
  supervise?: boolean;
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
  log_tail?: string;
  result: RunResult;
  started_at_ms: number; // 0 until the job leaves the queue and starts processing
  finished_at_ms: number; // 0 until the job reaches a terminal state
  created_at: number;
  updated_at: number;
}

export interface BrowseEntry {
  name: string;
  path: string;
  is_dir: boolean;
  local?: boolean; // present on local disk (only set when browsing with an S3 bucket)
  remote?: boolean; // present on the S3 mirror
}

// S3 connection status (GET /api/s3/status). configured = credentials present in the env; reachable +
// buckets are filled when a connection test succeeds.
export interface S3Status {
  configured: boolean;
  endpoint?: string;
  reachable?: boolean;
  buckets?: string[];
  error?: string;
}

// One capture folder of a past processing, with whether it still exists on disk (GET /api/processed).
export interface ProcessedPath {
  path: string;
  exists: boolean;
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
export interface LogLine {
  ts: number | null;
  text: string;
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
  | "dark_nebula"
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
}
export interface DarkSite {
  lat: number;
  lon: number;
  sqm: number;
  bortle: number;
  distance_km: number;
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
  suitability: number;
  reasons: string[];
}

export interface GotoResult {
  hemisphere: "north" | "south";
  profile: string;
  mount_type: "eq" | "altaz";
  meridian_side: "east" | "west" | "any";
  stars: GotoStar[];
  quality_score: number;
  warnings: string[];
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
