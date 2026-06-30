import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet } from "@/services/api";
import type { SkyEvent, EventsQueryEcho, EventsResponse } from "@/types";

// EventsQuery holds the server-side inputs the user can override; changing any triggers a refetch.
// Anything omitted falls back to the engine's configured defaults.
export interface EventsQuery {
  from?: string; // YYYY-MM-DD or RFC3339; omitted = now
  to?: string; // omitted = now + 90d
  lat?: number;
  lon?: number;
  elevation_m?: number;
  focal_mm?: number;
  aperture_mm?: number;
  pixel_um?: number;
  sensor_w?: number;
  sensor_h?: number;
  comets?: number; // 0 to disable the online comet feed
  satellites?: number; // 0 to disable the online TLE feed
}

// CalendarMode: browse a date window, or the next N occurrences of one event type.
export type CalendarMode = "window" | "series";

// SeriesState: the "by type" selection (persisted so the page reopens where you left it).
interface SeriesState {
  mode: CalendarMode;
  kind: string;
  subtype: string;
  count: number;
}

const STORAGE_KEY = "astrostack.events.query";
const SERIES_KEY = "astrostack.events.series";

function loadPersisted(): EventsQuery {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as EventsQuery) : {};
  } catch {
    return {};
  }
}

function persist(q: EventsQuery) {
  try {
    const { from: _f, to: _t, ...rest } = q; // the date window is session-only, not persisted
    localStorage.setItem(STORAGE_KEY, JSON.stringify(rest));
  } catch {
    // ignore quota / private-mode errors
  }
}

function loadSeries(): SeriesState {
  const def: SeriesState = {
    mode: "window",
    kind: "solar_eclipse",
    subtype: "",
    count: 10,
  };
  try {
    const raw = localStorage.getItem(SERIES_KEY);
    return raw ? { ...def, ...(JSON.parse(raw) as SeriesState) } : def;
  } catch {
    return def;
  }
}

function queryString(q: Record<string, unknown>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const useEventsStore = defineStore("events", () => {
  const events = ref<SkyEvent[]>([]);
  const query = ref<EventsQueryEcho | null>(null);
  const warnings = ref<string[]>([]);
  const loading = ref(false);
  const error = ref("");
  const selectedId = ref<string | null>(null);
  const params = ref<EventsQuery>(loadPersisted());

  const sp = loadSeries();
  const mode = ref<CalendarMode>(sp.mode);
  const seriesKind = ref(sp.kind);
  const seriesSubtype = ref(sp.subtype);
  const seriesCount = ref(sp.count);

  let lastKey = "";
  let inflight: Promise<void> | null = null;
  let controller: AbortController | null = null;

  const selected = computed(
    () => events.value.find((e) => e.id === selectedId.value) ?? null,
  );

  function persistSeries() {
    try {
      localStorage.setItem(
        SERIES_KEY,
        JSON.stringify({
          mode: mode.value,
          kind: seriesKind.value,
          subtype: seriesSubtype.value,
          count: seriesCount.value,
        }),
      );
    } catch {
      // ignore quota / private-mode errors
    }
  }

  // run is the shared fetch core (cache by key, in-flight dedup, AbortController, populate state).
  function run(key: string, path: string, force: boolean): Promise<void> {
    if (!force && key === lastKey && events.value.length) return Promise.resolve();
    if (inflight && key === lastKey) return inflight;
    controller?.abort();
    controller = new AbortController();
    const signal = controller.signal;
    lastKey = key;
    loading.value = true;
    error.value = "";
    inflight = (async () => {
      try {
        const data = await apiGet<EventsResponse>(path, signal);
        events.value = data.events ?? [];
        query.value = data.query;
        warnings.value = data.warnings ?? [];
        if (!events.value.some((e) => e.id === selectedId.value)) {
          selectedId.value = events.value[0]?.id ?? null;
        }
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

  // fetch is the date-window query (GET /api/sky/events).
  async function fetch(next?: EventsQuery, force = false): Promise<void> {
    if (next) params.value = { ...params.value, ...next };
    mode.value = "window";
    persist(params.value);
    persistSeries();
    const qs = queryString(params.value);
    return run("w" + qs, `/api/sky/events${qs}`, force);
  }

  // fetchSeries is the "next N of a type" query (GET /api/sky/series), reusing the site/gear params.
  async function fetchSeries(force = false): Promise<void> {
    mode.value = "series";
    persistSeries();
    const q: Record<string, unknown> = {
      lat: params.value.lat,
      lon: params.value.lon,
      elevation_m: params.value.elevation_m,
      focal_mm: params.value.focal_mm,
      aperture_mm: params.value.aperture_mm,
      pixel_um: params.value.pixel_um,
      sensor_w: params.value.sensor_w,
      sensor_h: params.value.sensor_h,
      kind: seriesKind.value,
      subtype: seriesSubtype.value,
      count: seriesCount.value,
    };
    const qs = queryString(q);
    return run("s" + qs, `/api/sky/series${qs}`, force);
  }

  const refresh = () =>
    mode.value === "series" ? fetchSeries(true) : fetch(undefined, true);

  const select = (id: string | null) => {
    selectedId.value = id;
  };

  function reset(): Promise<void> {
    params.value = {};
    persist(params.value);
    return fetch(undefined, true);
  }

  return {
    events,
    query,
    warnings,
    loading,
    error,
    selectedId,
    selected,
    params,
    mode,
    seriesKind,
    seriesSubtype,
    seriesCount,
    fetch,
    fetchSeries,
    refresh,
    select,
    reset,
  };
});
