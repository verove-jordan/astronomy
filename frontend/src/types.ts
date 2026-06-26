// API response types mirroring the Go engine's JSON.

export interface Frame {
  path: string;
  type: string;
  filter?: string;
  exposure_ms: number;
  gain: number;
  offset: number;
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

export interface FinalResult {
  mode: string;
  channels: string[];
  outputs: string[];
  notes?: string[];
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
  created_at: number;
  updated_at: number;
}

export interface BrowseEntry {
  name: string;
  path: string;
  is_dir: boolean;
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
