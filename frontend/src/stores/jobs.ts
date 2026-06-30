import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, apiPost } from "@/services/api";
import type { Inventory, Job, ReusePreview, RunSummary } from "@/types";

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
  // Live stacking (mode "livestack"): which source to watch and the per-sub exposure.
  live?: {
    sourceKind: "local" | "s3";
    bucket?: string;
    prefix?: string;
    exposureSec?: number;
  };
}

// Runs gallery page size (paginated so a large output dir loads fast).
const RUNS_PAGE = 12;

export const useJobsStore = defineStore("jobs", () => {
  const jobs = ref<Job[]>([]);
  const current = ref<Job | null>(null);
  const runs = ref<RunSummary[]>([]);
  const loading = ref(false);
  const error = ref("");
  // Inventory stashed at create-time so JobView can show the capture summary while processing.
  const captureByJob = ref<Record<number, Inventory>>({});

  async function list() {
    loading.value = true;
    error.value = "";
    try {
      const data = await apiGet<{ jobs: Job[] }>("/api/jobs");
      jobs.value = data.jobs || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
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
    if (opts.darkDir) body.dark_dir = opts.darkDir;
    if (opts.flatDir) body.flat_dir = opts.flatDir;
    if (opts.biasDir) body.bias_dir = opts.biasDir;
    if (opts.reuseDisabled) body.reuse_disabled = true;
    if (opts.reuseSessions && opts.reuseSessions.length)
      body.reuse_sessions = opts.reuseSessions;
    if (opts.live)
      body.live = {
        source_kind: opts.live.sourceKind,
        bucket: opts.live.bucket,
        prefix: opts.live.prefix,
        exposure_sec: opts.live.exposureSec,
      };
    const data = await apiPost<{ id: number }>("/api/jobs", body);
    if (opts.inventory) captureByJob.value[data.id] = opts.inventory;
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
        `/api/runs?offset=${runs.value.length}&limit=${RUNS_PAGE}`,
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
    loadingMore,
    loading,
    error,
    captureByJob,
    list,
    get,
    create,
    previewReuse,
    captureFor,
    inspectCapture,
    cancel,
    listRuns,
  };
});
