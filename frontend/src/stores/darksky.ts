import { defineStore } from "pinia";
import { computed, ref } from "vue";
import { apiGet } from "@/services/api";
import type {
  DarkSite,
  DarkSitesResult,
  DarkSkyNight,
  SkyNight,
  SkyNightsResult,
} from "@/types";

// Query for the dark-sky finder: a map area, a Bortle ceiling, whether to score horizon openness, and
// which night to rank for.
export interface DarkSiteQuery {
  minLat: number;
  minLon: number;
  maxLat: number;
  maxLon: number;
  maxBortle: number;
  horizon: boolean;
  lat?: number; // observer location, for the distance column
  lon?: number;
  limit?: number;
  night?: number; // 0 = tonight
  weather?: boolean; // fold the night's forecast into the ranking (default on)
  weatherWeight?: number; // 0..0.8; 0 = the server's configured default
}

// useDarkSkyStore holds the dark-sky finder results and the list of nights the picker offers. The API
// calls live here; the view supplies the drawn area + options and reads back the ranked candidates.
export const useDarkSkyStore = defineStore("darksky", () => {
  const candidates = ref<DarkSite[]>([]);
  const cellsScanned = ref(0);
  const night = ref<DarkSkyNight | null>(null);
  const weatherWeight = ref(0);
  const warnings = ref<string[]>([]);
  const loading = ref(false);
  const error = ref("");
  const searched = ref(false);

  const nights = ref<SkyNight[]>([]);
  const nightsLoaded = ref(false);

  // The forecast half of the ranking is the part that can be missing, so tell them apart: a result
  // with no weather at all is a terrain-only answer and the UI must say so rather than imply a
  // forecast was consulted.
  const weatherAvailable = computed(
    () =>
      weatherWeight.value > 0 &&
      candidates.value.some((c) => c.weather && c.weather.sample_hours > 0),
  );

  let inflight: AbortController | null = null;

  async function find(q: DarkSiteQuery): Promise<void> {
    inflight?.abort(); // a re-search supersedes whatever is still in the air
    const ctrl = new AbortController();
    inflight = ctrl;

    loading.value = true;
    error.value = "";
    searched.value = false;
    const params = new URLSearchParams({
      min_lat: String(q.minLat),
      min_lon: String(q.minLon),
      max_lat: String(q.maxLat),
      max_lon: String(q.maxLon),
      max_bortle: String(q.maxBortle),
    });
    if (q.horizon) params.set("horizon", "1");
    if (q.lat !== undefined) params.set("lat", String(q.lat));
    if (q.lon !== undefined) params.set("lon", String(q.lon));
    if (q.limit) params.set("limit", String(q.limit));
    if (q.night) params.set("night", String(q.night));
    if (q.weather === false) params.set("weather", "0");
    if (q.weatherWeight)
      params.set("weather_weight", q.weatherWeight.toFixed(2));
    try {
      const data = await apiGet<DarkSitesResult>(
        `/api/sky/darksites?${params.toString()}`,
        ctrl.signal,
      );
      candidates.value = data.candidates ?? [];
      cellsScanned.value = data.cells_scanned ?? 0;
      night.value = data.night ?? null;
      weatherWeight.value = data.weather_weight ?? 0;
      warnings.value = data.warnings ?? [];
    } catch (e) {
      if ((e as Error).name === "AbortError") return;
      error.value = (e as Error).message;
      candidates.value = [];
    } finally {
      if (inflight === ctrl) {
        inflight = null;
        loading.value = false;
        searched.value = true;
      }
    }
  }

  // loadNights fills the night picker. Twilight and moonrise are pure computation server-side, so this
  // is cheap and cached for the session — the list only changes when the observer moves.
  async function loadNights(lat?: number, lon?: number): Promise<void> {
    const params = new URLSearchParams();
    if (lat !== undefined) params.set("lat", String(lat));
    if (lon !== undefined) params.set("lon", String(lon));
    const key = params.toString();
    if (nightsLoaded.value && key === lastNightsKey) return;
    try {
      const data = await apiGet<SkyNightsResult>(
        `/api/sky/nights${key ? `?${key}` : ""}`,
      );
      nights.value = data.nights ?? [];
      nightsLoaded.value = true;
      lastNightsKey = key;
    } catch {
      // The picker falls back to plain day offsets; a missing night list must not block a search.
      nights.value = [];
    }
  }
  let lastNightsKey = "";

  function reset(): void {
    candidates.value = [];
    cellsScanned.value = 0;
    night.value = null;
    weatherWeight.value = 0;
    warnings.value = [];
    error.value = "";
    searched.value = false;
  }

  return {
    candidates,
    cellsScanned,
    night,
    weatherWeight,
    weatherAvailable,
    nights,
    warnings,
    loading,
    error,
    searched,
    find,
    loadNights,
    reset,
  };
});
