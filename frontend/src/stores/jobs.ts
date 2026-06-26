import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet, apiPost } from "@/services/api";
import type { Inventory, Job, ReusePreview, RunSummary } from "@/types";

export interface CreateOpts {
  filterMap?: Record<string, string>;
  dropWheelTransition?: boolean;
  colorCalibration?: boolean;
  denoise?: boolean;
  inventory?: Inventory | null;
  // Cross-session reuse: disable entirely, or restrict folded-in prior data to chosen session ids.
  reuseDisabled?: boolean;
  reuseSessions?: number[];
}

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
    if (opts.filterMap && Object.keys(opts.filterMap).length)
      body.filter_map = opts.filterMap;
    if (opts.dropWheelTransition !== undefined)
      body.drop_wheel_transition = opts.dropWheelTransition;
    if (opts.colorCalibration !== undefined)
      body.color_calibration = opts.colorCalibration;
    if (opts.denoise !== undefined) body.denoise = opts.denoise;
    if (opts.reuseDisabled) body.reuse_disabled = true;
    if (opts.reuseSessions && opts.reuseSessions.length)
      body.reuse_sessions = opts.reuseSessions;
    const data = await apiPost<{ id: number }>("/api/jobs", body);
    if (opts.inventory) captureByJob.value[data.id] = opts.inventory;
    return data.id;
  }

  // previewReuse asks the backend what prior light sessions a run on this path would fold in.
  async function previewReuse(path: string): Promise<ReusePreview | null> {
    try {
      return await apiPost<ReusePreview>("/api/reuse/preview", { path });
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

  // listRuns loads the durable on-disk run records (independent of the DB) for the Runs gallery.
  async function listRuns() {
    loading.value = true;
    error.value = "";
    try {
      const data = await apiGet<{ runs: RunSummary[] }>("/api/runs");
      runs.value = data.runs || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  return {
    jobs,
    current,
    runs,
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
