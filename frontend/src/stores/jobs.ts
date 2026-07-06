import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, apiPost, health, withS3 } from "@/services/api";
import type {
  Inventory,
  Job,
  ReusePreview,
  CalibPreview,
  RunSummary,
} from "@/types";

export interface CreateOpts {
  filterMap?: Record<string, string>;
  dropWheelTransition?: boolean;
  colorCalibration?: boolean;
  denoise?: boolean;
  haExcludeStars?: boolean;
  supervise?: boolean; // opt-in: drive the local AI agent to auto-tune the finish
  sequential?: boolean; // queue into the single-worker sequential lane (chained "Add to queue")
  look?: string; // milkyway render style: natural | iphone | deepsky
  brightness?: string; // milkyway sky brightness: darker | balanced | brighter
  orientation?: string; // milkyway final orientation override: auto | none | cw | ccw | 180 (+ "-flip")
  darkDir?: string; // milkyway: optional dark calibration frames folder
  flatDir?: string; // milkyway: optional flat calibration frames folder
  biasDir?: string; // milkyway: optional bias/offset calibration frames folder
  inventory?: Inventory | null;
  // Multi-folder selection: the capture folders to merge into one session. The `path` argument is the
  // primary (first) dir; `paths` (when length > 1) tells the backend to merge them. Single folder → omit.
  paths?: string[];
  // Cross-session reuse: disable entirely, or restrict folded-in prior data to chosen session ids.
  reuseDisabled?: boolean;
  reuseSessions?: number[];
  // Library calibration the user unchecked in the Calibration panel (calib.SuggestID keys to skip).
  calibExclude?: string[];
  // Storage mode: "local" (default — keep files) or "s3" (pull inputs from S3, process locally, push
  // inputs+results back to S3, then free the local copies — verified). s3 carries the target bucket/prefix.
  storageMode?: "local" | "s3";
  s3?: { bucket: string; prefix: string };
  // Live stacking (mode "livestack"): which source to watch and the per-sub exposure.
  live?: {
    sourceKind: "local" | "s3";
    bucket?: string;
    prefix?: string;
    exposureSec?: number;
  };
  // Advanced AI parameters: a free-text objective the agent carries for the run, fine tunable-knob
  // overrides (same whitelist/clamps as the supervisor), its re-entry ceiling and iteration cap.
  goal?: string;
  params?: Record<string, unknown>;
  tier?: "A" | "B" | "C";
  maxIters?: number;
  // Agent improvement series to link the job to (0/absent = none).
  seriesId?: number;
}

// RefineOpts tunes an AI-supervised re-finish of a completed run (POST /api/jobs/{id}/refine).
export interface RefineOpts {
  maxIters?: number;
  tier?: "A" | "B" | "C"; // how far the agent may reach: composite | +finish prep | +re-stack
  allowRestack?: boolean; // permit Tier-C re-stack from the original raw frames
  params?: Record<string, unknown>; // fine knob overrides seeded onto the preset before the loop
}

// Runs gallery page size (paginated so a large output dir loads fast).
const RUNS_PAGE = 12;

// Tasks page size (paginated, newest first, so a long job history never loads all at once).
const JOBS_PAGE = 20;

export const useJobsStore = defineStore("jobs", () => {
  const jobs = ref<Job[]>([]);
  const current = ref<Job | null>(null);
  const runs = ref<RunSummary[]>([]);
  const loading = ref(false);
  const error = ref("");
  const jobsTotal = ref(0);
  const jobsHasMore = computed(() => jobs.value.length < jobsTotal.value);
  // Inventory stashed at create-time so JobView can show the capture summary while processing.
  const captureByJob = ref<Record<number, Inventory>>({});
  // Conversation turn id stashed at create/refine-time (supervised jobs only) so JobView can open the
  // live steerable conversation for the run it just started.
  const turnByJob = ref<Record<number, string>>({});

  // list refreshes the currently-loaded window (newest first). It re-fetches from offset 0 with a limit of
  // however many are already shown (min one page), so the Tasks poll updates live status without discarding
  // "load more" pages — and a fresh visit loads just the first page.
  async function list() {
    const limit = Math.max(JOBS_PAGE, jobs.value.length);
    loading.value = jobs.value.length === 0;
    error.value = "";
    try {
      const data = await apiGet<{ jobs: Job[]; total: number }>(
        `/api/jobs?offset=0&limit=${limit}`,
      );
      jobs.value = data.jobs || [];
      jobsTotal.value = data.total ?? jobs.value.length;
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  // loadMoreJobs appends the next older page.
  async function loadMoreJobs() {
    loadingMore.value = true;
    error.value = "";
    try {
      const data = await apiGet<{ jobs: Job[]; total: number }>(
        `/api/jobs?offset=${jobs.value.length}&limit=${JOBS_PAGE}`,
      );
      jobs.value = [...jobs.value, ...(data.jobs || [])];
      jobsTotal.value = data.total ?? jobs.value.length;
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loadingMore.value = false;
    }
  }

  async function get(id: number) {
    loading.value = true;
    error.value = "";
    try {
      current.value = await apiGet<Job>(`/api/jobs/${id}`);
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  async function create(
    path: string,
    mode: string,
    format: string,
    opts: CreateOpts = {},
  ): Promise<number> {
    const body: Record<string, unknown> = { path, mode, format };
    if (opts.paths && opts.paths.length > 1) body.paths = opts.paths;
    if (opts.filterMap && Object.keys(opts.filterMap).length)
      body.filter_map = opts.filterMap;
    if (opts.dropWheelTransition !== undefined)
      body.drop_wheel_transition = opts.dropWheelTransition;
    if (opts.colorCalibration !== undefined)
      body.color_calibration = opts.colorCalibration;
    if (opts.denoise !== undefined) body.denoise = opts.denoise;
    if (opts.haExcludeStars !== undefined)
      body.ha_exclude_stars = opts.haExcludeStars;
    if (opts.supervise) body.supervise = true;
    if (opts.sequential) body.sequential = true;
    if (opts.look) body.look = opts.look;
    if (opts.brightness) body.brightness = opts.brightness;
    if (opts.orientation) body.orientation = opts.orientation;
    if (opts.darkDir) body.dark_dir = opts.darkDir;
    if (opts.flatDir) body.flat_dir = opts.flatDir;
    if (opts.biasDir) body.bias_dir = opts.biasDir;
    if (opts.reuseDisabled) body.reuse_disabled = true;
    if (opts.reuseSessions && opts.reuseSessions.length)
      body.reuse_sessions = opts.reuseSessions;
    if (opts.calibExclude && opts.calibExclude.length)
      body.calib_exclude = opts.calibExclude;
    if (opts.live)
      body.live = {
        source_kind: opts.live.sourceKind,
        bucket: opts.live.bucket,
        prefix: opts.live.prefix,
        exposure_sec: opts.live.exposureSec,
      };
    if (opts.storageMode === "s3" && opts.s3?.bucket) {
      body.storage_mode = "s3";
      body.s3 = { bucket: opts.s3.bucket, prefix: opts.s3.prefix };
    }
    if (opts.goal) body.goal = opts.goal;
    if (opts.params && Object.keys(opts.params).length)
      body.params = opts.params;
    if (opts.tier) body.tier = opts.tier;
    if (opts.maxIters) body.max_iters = opts.maxIters;
    if (opts.seriesId) body.series_id = opts.seriesId;
    const data = await apiPost<{ id: number; turn_id?: string }>(
      "/api/jobs",
      body,
    );
    if (opts.inventory) captureByJob.value[data.id] = opts.inventory;
    if (data.turn_id) turnByJob.value[data.id] = data.turn_id;
    return data.id;
  }

  // previewReuse asks the backend what prior light sessions a run over these folders would fold in.
  async function previewReuse(paths: string[]): Promise<ReusePreview | null> {
    try {
      return await apiPost<ReusePreview>("/api/reuse/preview", { paths });
    } catch {
      return null;
    }
  }

  // previewCalibration asks which library master dark/flat/bias would calibrate each inspected channel.
  async function previewCalibration(
    paths: string[],
  ): Promise<CalibPreview | null> {
    try {
      return await apiPost<CalibPreview>("/api/calib/preview", { paths });
    } catch {
      return null;
    }
  }

  function captureFor(id: number): Inventory | null {
    return captureByJob.value[id] ?? null;
  }

  // inspectCapture re-scans a path so a hard-reloaded running job can still show its capture summary
  // (when the create-time inventory was lost). Returns null on failure rather than throwing.
  async function inspectCapture(path: string): Promise<Inventory | null> {
    try {
      return await apiPost<Inventory>("/api/inspect", { path });
    } catch {
      return null;
    }
  }

  async function cancel(id: number): Promise<boolean> {
    const data = await apiPost<{ cancelled: boolean }>(
      `/api/jobs/${id}/cancel`,
    );
    return data.cancelled;
  }

  // restart re-runs a finished (failed/cancelled) job as a brand-new job with the same parameters,
  // returning the new job id so the caller can navigate to it.
  async function restart(id: number): Promise<number> {
    const data = await apiPost<{ id: number }>(`/api/jobs/${id}/restart`);
    return data.id;
  }

  // refine re-finishes a completed run under the AI supervisor (no re-stack unless allowRestack) as a
  // new job, returning its id so the caller can navigate to the live iteration stream.
  async function refine(id: number, opts: RefineOpts = {}): Promise<number> {
    const body: Record<string, unknown> = {};
    if (opts.maxIters) body.max_iters = opts.maxIters;
    if (opts.tier) body.tier = opts.tier;
    if (opts.allowRestack) body.allow_restack = true;
    if (opts.params && Object.keys(opts.params).length)
      body.params = opts.params;
    const data = await apiPost<{ id: number; turn_id?: string }>(
      `/api/jobs/${id}/refine`,
      body,
    );
    if (data.turn_id) turnByJob.value[data.id] = data.turn_id;
    return data.id;
  }

  // turnFor returns the conversation turn id stashed for a supervised/refine job (empty when none).
  function turnFor(id: number): string {
    return turnByJob.value[id] ?? "";
  }

  // Engine identity of the CURRENTLY-serving build (GET /api/health), fetched once and cached so run
  // cards/results can flag images produced by an older build. "" until known; "dev" = un-stamped.
  const engineVersion = ref("");
  let healthInflight: Promise<void> | null = null;
  async function fetchHealth(): Promise<void> {
    if (engineVersion.value) return;
    if (healthInflight) return healthInflight;
    healthInflight = (async () => {
      try {
        const h = await health();
        engineVersion.value = h.engine?.version || "";
      } catch {
        // soft-fail: engine chips simply skip stale detection
      } finally {
        healthInflight = null;
      }
    })();
    return healthInflight;
  }

  // Durable on-disk run records (independent of the DB) for the Runs gallery, paginated so a large
  // output dir stays fast. listRuns(true) loads the first page; listRuns(false) appends the next.
  const runsTotal = ref(0);
  const loadingMore = ref(false);
  const runsHasMore = computed(() => runs.value.length < runsTotal.value);
  async function listRuns(reset = true) {
    if (reset) {
      runs.value = [];
      runsTotal.value = 0;
      loading.value = true;
    } else {
      loadingMore.value = true;
    }
    error.value = "";
    try {
      const data = await apiGet<{ runs: RunSummary[]; total: number }>(
        withS3(`/api/runs?offset=${runs.value.length}&limit=${RUNS_PAGE}`),
      );
      runs.value = [...runs.value, ...(data.runs || [])];
      runsTotal.value = data.total ?? runs.value.length;
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
      loadingMore.value = false;
    }
  }

  return {
    jobs,
    current,
    runs,
    runsTotal,
    runsHasMore,
    jobsTotal,
    jobsHasMore,
    loadMoreJobs,
    loadingMore,
    loading,
    error,
    captureByJob,
    list,
    get,
    create,
    previewReuse,
    previewCalibration,
    captureFor,
    turnFor,
    inspectCapture,
    cancel,
    restart,
    refine,
    listRuns,
    engineVersion,
    fetchHealth,
  };
});
