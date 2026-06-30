import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet } from "@/services/api";
import { useSkyStore } from "@/stores/sky";
import type {
  SiteForecast,
  WeatherHour,
  WeatherGrid,
  WeatherResponse,
  WeatherGridResponse,
} from "@/types";

// The grid is one multi-point Open-Meteo call; fetch every animatable layer at once so toggling them is
// instant.
const GRID_LAYERS = "clouds,humidity,precip";
const FRAME_MS = 700; // animation cadence per timestep

function queryString(q: Record<string, unknown>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const useWeatherStore = defineStore("weather", () => {
  const forecast = ref<SiteForecast | null>(null);
  const grid = ref<WeatherGrid | null>(null);
  const warning = ref("");
  const loading = ref(false);
  const error = ref("");
  const playhead = ref(0); // current animation time (epoch ms)
  const playing = ref(false);

  let lastKey = "";
  let inflight: Promise<void> | null = null;
  let controller: AbortController | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;

  const timesteps = computed(() => grid.value?.timesteps ?? []);
  const rangeStart = computed(() => timesteps.value[0] ?? 0);
  const rangeEnd = computed(
    () => timesteps.value[timesteps.value.length - 1] ?? 0,
  );

  // frameIndex is the grid frame nearest the playhead (the frame the map paints).
  const frameIndex = computed(() => {
    const ts = timesteps.value;
    if (!ts.length) return 0;
    let best = 0;
    for (let i = 1; i < ts.length; i++) {
      if (
        Math.abs(ts[i] - playhead.value) < Math.abs(ts[best] - playhead.value)
      )
        best = i;
    }
    return best;
  });

  // nowHour is the forecast hour nearest the current wall-clock (drives the conditions badge).
  const nowHour = computed<WeatherHour | null>(() => {
    const hrs = forecast.value?.hours ?? [];
    if (!hrs.length) return null;
    const now = Date.now();
    let best = hrs[0];
    for (const h of hrs) {
      if (Math.abs(h.t_ms - now) < Math.abs(best.t_ms - now)) best = h;
    }
    return best;
  });

  const best = computed(() => forecast.value?.best ?? null);
  const kp = computed(() => forecast.value?.kp ?? null);
  const sources = computed(() => forecast.value?.sources ?? []);

  function resetPlayheadToNow() {
    const ts = timesteps.value;
    if (!ts.length) return;
    const now = Date.now();
    playhead.value = Math.min(Math.max(now, ts[0]), ts[ts.length - 1]);
  }

  async function fetch(force = false): Promise<void> {
    const sky = useSkyStore();
    if (!sky.query) await sky.fetch();
    const lat = sky.query?.location.lat;
    const lon = sky.query?.location.lon;
    const at = sky.params?.at;
    if (lat == null || lon == null) return;

    const key = `${lat.toFixed(3)},${lon.toFixed(3)},${at ?? ""}`;
    if (!force && key === lastKey && forecast.value) return; // cache hit
    if (inflight && key === lastKey) return inflight; // in-flight dedup
    controller?.abort();
    controller = new AbortController();
    const signal = controller.signal;
    lastKey = key;
    loading.value = true;
    error.value = "";
    inflight = (async () => {
      try {
        const [fc, gr] = await Promise.all([
          apiGet<WeatherResponse>(
            `/api/sky/weather${queryString({ lat, lon, at })}`,
            signal,
          ),
          apiGet<WeatherGridResponse>(
            `/api/sky/weather/grid${queryString({ lat, lon, layers: GRID_LAYERS })}`,
            signal,
          ),
        ]);
        forecast.value = fc.forecast;
        grid.value = gr.grid;
        warning.value = fc.warning || gr.warning || "";
        resetPlayheadToNow();
      } catch (e) {
        if ((e as Error).name !== "AbortError")
          error.value = (e as Error).message;
      } finally {
        loading.value = false;
        inflight = null;
      }
    })();
    return inflight;
  }

  function setPlayhead(ms: number) {
    pause();
    playhead.value = ms;
  }

  function step(dir: number) {
    const ts = timesteps.value;
    if (!ts.length) return;
    const next = Math.min(Math.max(frameIndex.value + dir, 0), ts.length - 1);
    playhead.value = ts[next];
  }

  function play() {
    const ts = timesteps.value;
    if (ts.length < 2 || playing.value) return;
    playing.value = true;
    timer = setInterval(() => {
      const cur = frameIndex.value;
      const next = cur + 1 >= ts.length ? 0 : cur + 1; // loop
      playhead.value = ts[next];
    }, FRAME_MS);
  }

  function pause() {
    if (timer) clearInterval(timer);
    timer = null;
    playing.value = false;
  }

  function togglePlay() {
    if (playing.value) pause();
    else play();
  }

  return {
    forecast,
    grid,
    warning,
    loading,
    error,
    playhead,
    playing,
    timesteps,
    rangeStart,
    rangeEnd,
    frameIndex,
    nowHour,
    best,
    kp,
    sources,
    fetch,
    setPlayhead,
    step,
    play,
    pause,
    togglePlay,
  };
});
