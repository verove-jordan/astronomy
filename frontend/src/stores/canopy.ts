import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, apiPost } from "@/services/api";
import type { AtlasBuildState, AtlasBuildRequest } from "@/types";

// Manages the OFFLINE tree-canopy-height atlas for the dark-sky finder's tree-aware horizon: what area is
// installed, and downloading a new area on demand (the server fetches the ETH canopy-height COGs via gdal,
// then the finder folds nearby forests into the horizon). Same shape as the light-pollution atlas store.
export const useCanopyStore = defineStore("canopy", () => {
  const state = ref<AtlasBuildState | null>(null);
  const error = ref("");

  const coverage = computed(() => state.value?.coverage ?? null);
  const present = computed(() => coverage.value?.present ?? false);
  const building = computed(() => state.value?.status === "building");
  // Bumped when a new atlas is installed — the finder watches it to re-score with tree data.
  const builtAtMs = computed(() =>
    present.value ? (coverage.value?.built_at_ms ?? 0) : 0,
  );
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
      state.value = await apiGet<AtlasBuildState>("/api/sky/canopy/atlas");
    } catch (e) {
      error.value = (e as Error).message;
    }
  }

  async function build(req: AtlasBuildRequest) {
    error.value = "";
    try {
      state.value = await apiPost<AtlasBuildState>(
        "/api/sky/canopy/atlas",
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
