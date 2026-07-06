import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, apiPost } from "@/services/api";
import type { AtlasBuildState, AtlasBuildRequest } from "@/types";

// Manages the OFFLINE light-pollution atlas: what region is currently installed, and building a new
// region on demand (the server downloads the djlorenz tiles once, then all queries are offline).
export const useLightPollutionStore = defineStore("lightpollution", () => {
  const state = ref<AtlasBuildState | null>(null);
  const error = ref("");

  const coverage = computed(() => state.value?.coverage ?? null);
  const present = computed(() => coverage.value?.present ?? false);
  const building = computed(() => state.value?.status === "building");
  // Bumped whenever a new atlas is installed — map layers append it as a cache-buster so the browser
  // refetches the freshly-rendered overlay tiles.
  const builtAtMs = computed(() =>
    present.value ? (coverage.value?.built_at_ms ?? 0) : 0,
  );
  // A build failure (read-only atlas dir, network, …) surfaced from the async build state or an HTTP
  // error, so the UI shows it instead of silently reverting to "not downloaded for this zone".
  const buildError = computed(
    () =>
      error.value ||
      (state.value?.status === "error"
        ? state.value?.error || "build failed"
        : ""),
  );

  let timer: ReturnType<typeof setInterval> | null = null;
  function stopPolling() {
    if (timer) clearInterval(timer);
    timer = null;
  }

  async function fetchStatus() {
    try {
      state.value = await apiGet<AtlasBuildState>(
        "/api/sky/lightpollution/atlas",
      );
    } catch (e) {
      error.value = (e as Error).message;
    }
  }

  async function build(req: AtlasBuildRequest) {
    error.value = "";
    try {
      state.value = await apiPost<AtlasBuildState>(
        "/api/sky/lightpollution/atlas",
        req,
      );
    } catch (e) {
      error.value = (e as Error).message;
      return;
    }
    stopPolling();
    timer = setInterval(async () => {
      await fetchStatus();
      if (!building.value) stopPolling();
    }, 1500);
  }

  return {
    state,
    error,
    coverage,
    present,
    building,
    builtAtMs,
    buildError,
    fetchStatus,
    build,
  };
});
