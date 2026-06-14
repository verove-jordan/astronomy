// API response types mirroring the Go engine's JSON.

export interface Frame {
  path: string
  type: string
  filter?: string
  exposure_ms: number
  gain: number
  offset: number
  temp_milli_c: number
  has_temp: boolean
  width: number
  height: number
  object?: string
  class_source: string
}

export interface SetKey {
  type: string
  object?: string
  filter?: string
  exposure_ms: number
  gain: number
  offset: number
  temp_bucket_c: number
  bin: number
}

export interface FrameSet {
  key: SetKey
  count: number
  total_integration_ms: number
}

export interface Inventory {
  root: string
  frames: Frame[]
  sets: FrameSet[]
  videos: Frame[]
  warnings: string[]
}

export interface Master {
  type: string
  filter?: string
  exposure_ms: number
  gain: number
  offset: number
  temp_milli_c: number
  bin: number
  frame_count: number
  path: string
}

export interface GradeMetric {
  index: number
  path: string
  fwhm: number
  wfwhm: number
  roundness: number
  star_count: number
  background: number
  trail_detected: boolean
  trail_score: number
  rejected: boolean
  reject_reason?: string
}

export interface Selection {
  dark?: Master
  flat?: Master
  bias?: Master
  notes?: string[]
}

export interface ChannelResult {
  object: string
  filter: string
  exposure_ms: number
  input_frames: number
  stacked_frames: number
  output_path?: string
  selection: Selection
  metrics?: GradeMetric[]
  error?: string
}

export interface FinalResult {
  mode: string
  channels: string[]
  outputs: string[]
  notes?: string[]
}

export interface RunResult {
  input_dir: string
  output_dir: string
  masters: Master[]
  channels: ChannelResult[]
  final?: FinalResult
  warnings: string[]
}

export interface Job {
  id: number
  session_id: number
  kind: string
  status: string
  progress: number
  current_step: string
  error: string
  result: RunResult
  created_at: number
  updated_at: number
}

export interface BrowseEntry {
  name: string
  path: string
  is_dir: boolean
}
