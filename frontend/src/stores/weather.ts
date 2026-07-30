import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, BASE } from "@/services/api";
import { useSkyStore } from "@/stores/sky";
import type {
  SiteForecast,
  WeatherHour,
  WeatherFrames,
  WeatherResponse,
} from "@/types";

// The frames index carries only the time axis + coverage (no float data) — the animated weather overlays
// are server-rendered PNG tiles the browser just composites, so a metric list is no longer sent here (each
// enabled metric fetches its own tiles). Fetching frames also warms the backend's cube cache the tiles reuse.
const FRAME_MS = 700; // animation cadence per timestep

// RainViewer live observation layers: a tiny keyless public JSON listing recent + nowcast tile frames.
// Both products are consumed: radar (past + nowcast) and the satellite IR frames, which drive the
// "satellite" overlay — REAL observed clouds, zero forecast-API quota. Either list may be empty on the
// free tier; an empty product simply leaves its layer blank.
const RV_MAPS_URL = "https://api.rainviewer.com/public/weather-maps.json";
const RADAR_TOL_MS = 15 * 60 * 1000; // a radar frame only paints within ±15 min of the playhead…
const SAT_TOL_MS = 30 * 60 * 1000; //  …satellite IR is coarser, so a wider match window.

interface RvFrame {
  t_ms: number;
  path: string;
}
interface RvSlot {
  time: number;
  path: string;
}
interface RvMaps {
  host: string;
  radar?: { past?: RvSlot[]; nowcast?: RvSlot[] };
  satellite?: { infrared?: RvSlot[] };
}

function queryString(q: Record<string, unknown>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

// nearestIndex returns the index of the array value closest to v (0 for an empty array).
function nearestIndex(arr: number[], v: number): number {
  if (!arr.length) return 0;
  let best = 0;
  for (let i = 1; i < arr.length; i++)
    if (Math.abs(arr[i] - v) < Math.abs(arr[best] - v)) best = i;
  return best;
}

// framesUsable reports whether a fetched frames index actually carries a time axis. A soft-failed upstream
// (Open-Meteo rate-limit / daily-quota / outage) returns a SHAPED-but-empty payload — bbox set, timesteps
// empty — which must never replace a good axis on screen (that would blank the scrubber and the tiles).
function framesUsable(f: WeatherFrames | null | undefined): boolean {
  return !!f && Array.isArray(f.timesteps) && f.timesteps.length > 0;
}

// After a degraded/failed frames fetch, suppress AUTOMATIC refetches for this long so a flurry of site
// changes doesn't keep re-requesting a down/rate-limited upstream (which both storms the network and burns
// the daily API budget). An explicit force still tries immediately.
const GRID_FAIL_COOLDOWN_MS = 60 * 1000;

export const useWeatherStore = defineStore("weather", () => {
  const forecast = ref<SiteForecast | null>(null);
  const framesMeta = ref<WeatherFrames | null>(null); // the animated overlay's time axis + coverage (no floats)
  const warning = ref("");
  const loading = ref(false);
  const error = ref("");
  const playhead = ref(0); // current animation time (epoch ms)
  const playing = ref(false);

  // RainViewer live frames (radar past+nowcast, satellite IR) — loaded only while a live layer is on.
  const rvHost = ref("");
  const rvRadar = ref<RvFrame[]>([]);
  const rvSatellite = ref<RvFrame[]>([]);

  // Forecast (per-site; drives the badge/panel) and frames (the animated overlay's time axis) fetch
  // independently — each with its own dedup key + abort controller so one never cancels the other.
  let fcKey = "";
  let fcInflight: Promise<void> | null = null;
  let fcController: AbortController | null = null;
  let framesReqKey = "";
  let framesInflight: Promise<void> | null = null;
  let framesController: AbortController | null = null;
  let framesFailUntil = 0; // epoch ms until which auto-refetch is suppressed after a degraded fetch
  let rvLoaded = false;
  let rvInflight: Promise<void> | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;

  const timesteps = computed(() => framesMeta.value?.timesteps ?? []);

  // frames is the scrubber timeline — the union of the forecast-grid hourly steps and any loaded
  // RainViewer live frames (sorted, deduped). Grid-only → equals timesteps (unchanged behaviour);
  // live-only → RainViewer supplies the timeline; both on → dense live steps interleave with the hours.
  const frames = computed<number[]>(() => {
    if (!rvRadar.value.length && !rvSatellite.value.length)
      return timesteps.value;
    const set = new Set<number>(timesteps.value);
    for (const f of rvRadar.value) set.add(f.t_ms);
    for (const f of rvSatellite.value) set.add(f.t_ms);
    return [...set].sort((a, b) => a - b);
  });
  const rangeStart = computed(() => frames.value[0] ?? 0);
  const rangeEnd = computed(() => frames.value[frames.value.length - 1] ?? 0);
  // cursor is the frames index nearest the playhead (drives the scrubber thumb + play/step).
  const cursor = computed(() => nearestIndex(frames.value, playhead.value));

  // frameIndex is the grid frame nearest the playhead (the frame the map's forecast overlay paints).
  const frameIndex = computed(() =>
    nearestIndex(timesteps.value, playhead.value),
  );

  // weatherFrameTimeMs is the epoch-ms timestamp the server-rendered tiles should paint (the timestep
  // nearest the playhead), or null when there is no time axis yet. It is the `{time}` in the tile URL.
  const weatherFrameTimeMs = computed<number | null>(() => {
    const ts = timesteps.value;
    return ts.length ? ts[frameIndex.value] : null;
  });

  // weatherTileUrl builds the Leaflet template URL for one metric at the current frame (z/x/y left as
  // Leaflet placeholders). Empty while no frame is loaded, so the caller skips adding the layer.
  function weatherTileUrl(metric: string): string {
    const t = weatherFrameTimeMs.value;
    return t == null
      ? ""
      : `${BASE}/api/sky/weather/tiles/${metric}/${t}/{z}/{x}/{y}`;
  }

  // radarFrame/satelliteFrame resolve the live tile nearest the playhead, or null when the playhead is
  // outside that feed's observed window (so live layers vanish over the forecast future instead of
  // showing a stale "now" frame mislabelled as tomorrow).
  function nearestLive(
    list: RvFrame[],
    t: number,
    tol: number,
  ): { host: string; path: string } | null {
    if (!list.length || !rvHost.value) return null;
    let best = list[0];
    for (const f of list)
      if (Math.abs(f.t_ms - t) < Math.abs(best.t_ms - t)) best = f;
    return Math.abs(best.t_ms - t) <= tol
      ? { host: rvHost.value, path: best.path }
      : null;
  }
  const radarFrame = computed(() =>
    nearestLive(rvRadar.value, playhead.value, RADAR_TOL_MS),
  );
  const satelliteFrame = computed(() =>
    nearestLive(rvSatellite.value, playhead.value, SAT_TOL_MS),
  );

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
    const ts = frames.value;
    if (!ts.length) return;
    const now = Date.now();
    playhead.value = Math.min(Math.max(now, ts[0]), ts[ts.length - 1]);
  }

  // fetch loads the per-site point forecast (conditions badge + weather panel), keyed to the observing
  // site. The animated map grid is fetched separately by fetchGrid so it can follow the map viewport.
  async function fetch(force = false): Promise<void> {
    const sky = useSkyStore();
    if (!sky.query) await sky.fetch();
    const lat = sky.query?.location.lat;
    const lon = sky.query?.location.lon;
    const at = sky.params?.at;
    if (lat == null || lon == null) return;

    const key = `${lat.toFixed(3)},${lon.toFixed(3)},${at ?? ""}`;
    if (!force && key === fcKey && forecast.value) return; // cache hit
    if (fcInflight && key === fcKey) return fcInflight; // in-flight dedup
    fcController?.abort();
    fcController = new AbortController();
    const signal = fcController.signal;
    fcKey = key;
    loading.value = true;
    error.value = "";
    fcInflight = (async () => {
      try {
        const fc = await apiGet<WeatherResponse>(
          `/api/sky/weather${queryString({ lat, lon, at })}`,
          signal,
        );
        forecast.value = fc.forecast;
        if (fc.warning) warning.value = fc.warning;
      } catch (e) {
        if ((e as Error).name !== "AbortError")
          error.value = (e as Error).message;
      } finally {
        loading.value = false;
        fcInflight = null;
      }
    })();
    return fcInflight;
  }

  // fetchFrames loads the animated overlay's lightweight time axis (bbox + timesteps + issued_ms, no float
  // data) for the map centre at the map's zoom. The backend anchors the request to the SAME tile-block
  // region the tile handler uses, so this fetch warms exactly the cube every subsequent tile request
  // reads (one upstream fetch serves the scrubber and all metrics). The tiles themselves carry the heavy
  // data and are fetched by Leaflet per viewport; this runs on load / site change / zoom-scale change,
  // NOT on every pan.
  async function fetchFrames(
    lat: number,
    lon: number,
    zoom = 8,
    force = false,
  ): Promise<void> {
    if (lat == null || lon == null) return;
    // While upstream is degraded, keep the last good axis rather than hammering it: an automatic refetch is
    // skipped during the cooldown; an explicit force (site change) tries.
    if (!force && framesMeta.value && Date.now() < framesFailUntil) return;
    const key = `${lat.toFixed(2)},${lon.toFixed(2)},z${Math.round(zoom)}`;
    if (!force && key === framesReqKey && framesMeta.value) return; // cache hit
    if (framesInflight && key === framesReqKey) return framesInflight; // in-flight dedup
    framesController?.abort();
    framesController = new AbortController();
    const signal = framesController.signal;
    framesReqKey = key;
    framesInflight = (async () => {
      try {
        const fr = await apiGet<WeatherFrames>(
          `/api/sky/weather/grid/frames${queryString({ lat, lon, z: Math.round(zoom) })}`,
          signal,
        );
        if (framesUsable(fr)) {
          framesMeta.value = fr;
          warning.value = fr.warning ?? "";
          framesFailUntil = 0;
          resetPlayheadToNow();
        } else {
          // Degraded/empty upstream: DON'T drop the on-screen axis. Surface the warning and back off so a
          // burst of moves stops re-requesting a down upstream. "stale, not gone" when we still have frames.
          warning.value = framesMeta.value
            ? "live cloud map temporarily unavailable — showing the last loaded frames"
            : fr.warning || "cloud map currently unavailable";
          framesReqKey = ""; // this didn't really load — let a later fetch retry it
          framesFailUntil = Date.now() + GRID_FAIL_COOLDOWN_MS;
        }
      } catch (e) {
        if ((e as Error).name !== "AbortError") {
          error.value = (e as Error).message;
          framesReqKey = ""; // network error — allow a retry
          framesFailUntil = Date.now() + GRID_FAIL_COOLDOWN_MS;
        }
      } finally {
        framesInflight = null;
      }
    })();
    return framesInflight;
  }

  // fetchRainviewer loads the RainViewer live-frame index (radar past+nowcast, satellite IR). It is a
  // best-effort bonus overlay: a failure just leaves the layers empty, never surfaces an error, and the
  // caller polls it (force=true) every few minutes to stay current. `at`-independent (global product).
  async function fetchRainviewer(force = false): Promise<void> {
    if (!force && rvLoaded) return; // already have frames; the refresh interval forces a reload
    if (rvInflight) return rvInflight;
    rvInflight = (async () => {
      try {
        const r = await window.fetch(RV_MAPS_URL, { cache: "no-store" });
        if (!r.ok) throw new Error(`rainviewer ${r.status}`);
        const j = (await r.json()) as RvMaps;
        rvHost.value = j.host ?? "";
        const radar = [...(j.radar?.past ?? []), ...(j.radar?.nowcast ?? [])];
        rvRadar.value = radar.map((f) => ({
          t_ms: f.time * 1000,
          path: f.path,
        }));
        rvSatellite.value = (j.satellite?.infrared ?? []).map((f) => ({
          t_ms: f.time * 1000,
          path: f.path,
        }));
        rvLoaded = true;
        if (!playhead.value) resetPlayheadToNow(); // live-only: seed the scrubber on "now"
      } catch {
        // live radar is a bonus overlay — leave it empty on failure (no error surfaced to the page).
      } finally {
        rvInflight = null;
      }
    })();
    return rvInflight;
  }

  function setPlayhead(ms: number) {
    pause();
    playhead.value = ms;
  }

  function step(dir: number) {
    const ts = frames.value;
    if (!ts.length) return;
    const next = Math.min(Math.max(cursor.value + dir, 0), ts.length - 1);
    playhead.value = ts[next];
  }

  function play() {
    const ts = frames.value;
    if (ts.length < 2 || playing.value) return;
    playing.value = true;
    timer = setInterval(() => {
      const cur = cursor.value;
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
    framesMeta,
    warning,
    loading,
    error,
    playhead,
    playing,
    timesteps,
    frames,
    rangeStart,
    rangeEnd,
    frameIndex,
    weatherFrameTimeMs,
    weatherTileUrl,
    cursor,
    radarFrame,
    satelliteFrame,
    rvRadar,
    rvSatellite,
    nowHour,
    best,
    kp,
    sources,
    fetch,
    fetchFrames,
    fetchRainviewer,
    setPlayhead,
    step,
    play,
    pause,
    togglePlay,
  };
});
