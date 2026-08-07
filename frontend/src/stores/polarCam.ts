import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { apiGet, apiPost } from "@/services/api";
import type { PolarCamState } from "@/types";
import { useSkyStore } from "@/stores/sky";

// Polar alignment from the live camera.
//
// This is the measured counterpart to `stores/polar.ts`, which computes where Polaris ought to sit on
// a polar-scope reticle. That one needs a clock; this one needs the telescope, and answers the
// question the reticle cannot: how far off are you actually?
//
// The session lives on the engine — it owns the camera and the plate solver — so this store is thin:
// stream the state, post the four buttons, and drive the refresh timer during the adjustment. The
// timer is here rather than on the engine on purpose: closing the panel then stops the exposures,
// instead of leaving them running against a page nobody is watching.

// REFRESH_MS is the pause between frames while the user is turning a bolt. The exposure and the plate
// solve dominate it — this only keeps a fast solve from asking for frames faster than a hand moves.
const REFRESH_MS = 1500;

export const usePolarCamStore = defineStore("polarCam", () => {
  const state = ref<PolarCamState | null>(null);
  const error = ref("");
  const starting = ref(false);

  let source: EventSource | null = null;
  let refreshTimer = 0;
  let refreshing = false;

  const phase = computed(() => state.value?.phase ?? "idle");
  const running = computed(
    () => phase.value !== "idle" && phase.value !== "failed",
  );
  const measuring = computed(() => phase.value === "measuring");
  const solved = computed(() => phase.value === "solved");
  const adjusting = computed(() => phase.value === "adjusting");

  /** The marker the live view draws, or null when there is nothing to aim at yet. */
  const target = computed(() => state.value?.live?.target ?? null);

  /** Where the pole and its guide star fall on the last solved frame, for the finder overlay. */
  const pole = computed(() => state.value?.pole ?? null);

  /** True when the answer on offer came from one frame rather than from a measured rotation. */
  const isRough = computed(() => state.value?.mode === "rough");

  /** Warnings from the fit plus anything the live phase has to say, as codes for the UI to translate. */
  const warnings = computed(() => state.value?.warnings ?? []);

  async function refreshStatus(): Promise<void> {
    state.value = (
      await apiGet<{ state: PolarCamState }>("/api/capture/polar")
    ).state;
  }

  // watch streams the session. The engine sends a snapshot first, so re-opening the page part way
  // through an alignment shows the truth immediately rather than an empty panel.
  function watch(): void {
    if (source) return;
    source = new EventSource("/api/capture/polar/events");
    source.onmessage = (e) => {
      try {
        state.value = JSON.parse(e.data) as PolarCamState;
      } catch {
        // A malformed frame is not worth tearing the panel down for; another is moments away.
      }
    };
    source.onerror = () => {
      // EventSource reconnects on its own, and the engine being briefly unreachable during a hot
      // reload is not something to put a warning on screen for.
    };
  }

  function unwatch(): void {
    source?.close();
    source = null;
    stopRefreshing();
  }

  // post is the one door every button goes through, so the busy flag, the error and the optimistic
  // state update are impossible to get inconsistent between them.
  async function post(path: string): Promise<void> {
    error.value = "";
    try {
      const res = await apiPost<{ state: PolarCamState }>(
        `/api/capture/polar/${path}`,
      );
      state.value = res.state;
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e);
      throw e;
    }
  }

  async function start(): Promise<void> {
    const sky = useSkyStore();
    starting.value = true;
    error.value = "";
    try {
      // The site the user picked on the sky page wins over the engine's configured one, exactly as it
      // does for a capture session — people travel to dark sites more often than they edit .env.
      const res = await apiPost<{ state: PolarCamState }>(
        "/api/capture/polar/start",
        {
          lat_deg: sky.query?.location?.lat,
          lon_deg: sky.query?.location?.lon,
        },
      );
      state.value = res.state;
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e);
      throw e;
    } finally {
      starting.value = false;
    }
  }

  /** Answer from a single frame, assuming the telescope looks down the right-ascension axis. */
  async function rough(): Promise<void> {
    const sky = useSkyStore();
    starting.value = true;
    error.value = "";
    try {
      const res = await apiPost<{ state: PolarCamState }>(
        "/api/capture/polar/rough",
        {
          lat_deg: sky.query?.location?.lat,
          lon_deg: sky.query?.location?.lon,
        },
      );
      state.value = res.state;
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e);
      throw e;
    } finally {
      starting.value = false;
    }
  }

  /** The user has turned the right-ascension axis; take the next frame. */
  const next = () => post("next");

  /** Move to the live phase and start asking for frames. */
  async function adjust(): Promise<void> {
    await post("adjust");
    startRefreshing();
  }

  async function stop(): Promise<void> {
    stopRefreshing();
    await post("stop");
  }

  // startRefreshing drives the adjust loop from the browser. It chains with setTimeout rather than
  // setInterval so a slow solve can never stack requests behind itself.
  function startRefreshing(): void {
    stopRefreshing();
    const tick = async () => {
      if (!adjusting.value) return;
      if (!refreshing) {
        refreshing = true;
        try {
          await post("refresh");
        } catch {
          // A frame that would not solve is weather; the session keeps the last good marker and the
          // next tick tries again.
        } finally {
          refreshing = false;
        }
      }
      if (adjusting.value) refreshTimer = window.setTimeout(tick, REFRESH_MS);
    };
    refreshTimer = window.setTimeout(tick, REFRESH_MS);
  }

  function stopRefreshing(): void {
    window.clearTimeout(refreshTimer);
    refreshTimer = 0;
  }

  return {
    state,
    error,
    starting,
    phase,
    running,
    measuring,
    solved,
    adjusting,
    target,
    pole,
    isRough,
    warnings,
    refreshStatus,
    watch,
    unwatch,
    start,
    rough,
    next,
    adjust,
    stop,
  };
});
