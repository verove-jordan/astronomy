import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet, apiPost } from "@/services/api";
import type { Job, Series } from "@/types";

// SeriesDetail is one series with its attempts (GET /api/series/{id}) — the jobs are ordinary Job
// rows linked by series_id, oldest first.
export interface SeriesDetail {
  series: Series;
  jobs: Job[];
}

// Agent improvement series: durable "keep making this target better" campaigns. List + per-id detail
// (cached, deduplicated) plus the continue/stop actions the timeline buttons drive.
export const useSeriesStore = defineStore("series", () => {
  const series = ref<Series[]>([]);
  const details = ref<Record<number, SeriesDetail>>({});
  const loading = ref(false);
  const error = ref("");
  // Per-series continue/stop in flight, so each timeline disables only its own buttons.
  const acting = ref<Record<number, boolean>>({});

  async function list(limit = 50): Promise<void> {
    loading.value = series.value.length === 0;
    error.value = "";
    try {
      const data = await apiGet<{ series: Series[] }>(
        `/api/series?limit=${limit}`,
      );
      series.value = data.series || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  // get loads one series + its attempt jobs; cached by id (force re-fetches), concurrent callers
  // share one request.
  const detailInflight = new Map<number, Promise<void>>();
  async function get(id: number, force = false): Promise<void> {
    if (details.value[id] && !force) return;
    const running = detailInflight.get(id);
    if (running) return running;
    const p = (async () => {
      loading.value = !details.value[id];
      error.value = "";
      try {
        details.value[id] = await apiGet<SeriesDetail>(`/api/series/${id}`);
      } catch (e) {
        error.value = (e as Error).message;
      } finally {
        loading.value = false;
        detailInflight.delete(id);
      }
    })();
    detailInflight.set(id, p);
    return p;
  }

  // setStatus posts continue (→ active) or stop (→ stopped), then re-fetches the detail so the
  // status pill and buttons reflect the new state.
  async function setStatus(id: number, op: "continue" | "stop"): Promise<void> {
    acting.value[id] = true;
    error.value = "";
    try {
      await apiPost(`/api/series/${id}/${op}`);
      await get(id, true);
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      acting.value[id] = false;
    }
  }
  const continueSeries = (id: number) => setStatus(id, "continue");
  const stopSeries = (id: number) => setStatus(id, "stop");

  return {
    series,
    details,
    loading,
    error,
    acting,
    list,
    get,
    continueSeries,
    stopSeries,
  };
});
