import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet } from "@/services/api";
import { useSkyStore } from "@/stores/sky";
import type { PolarResult, PolarQueryEcho, PolarResponse } from "@/types";

// PolarQuery overrides the polar-alignment site/time; lat/lon default to the sky store's location.
export interface PolarQuery {
  lat?: number;
  lon?: number;
  at?: string; // RFC3339 instant; omitted = server "now"
}

const ORIENT_KEY = "astrostack.polar.orientation";

function loadOrientation(): { invert: boolean; mirror: boolean } {
  try {
    const raw = localStorage.getItem(ORIENT_KEY);
    if (raw) return JSON.parse(raw) as { invert: boolean; mirror: boolean };
  } catch {
    // ignore parse / private-mode errors
  }
  return { invert: true, mirror: false }; // most polar scopes invert (straight-through refractor)
}

function queryString(q: Record<string, unknown>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

function norm360(d: number): number {
  return ((d % 360) + 360) % 360;
}

export const usePolarStore = defineStore("polar", () => {
  const result = ref<PolarResult | null>(null);
  const query = ref<PolarQueryEcho | null>(null);
  const loading = ref(false);
  const error = ref("");
  const params = ref<PolarQuery>({});

  // UI-only display state (not fetched), shared by the reticle + panel so they stay in sync.
  const persisted = loadOrientation();
  const invert = ref(persisted.invert); // inverting polar scope (default)
  const mirror = ref(persisted.mirror); // extra mirror (e.g. via a star diagonal)

  let lastKey = "";
  let inflight: Promise<void> | null = null;
  let controller: AbortController | null = null;

  function persistOrientation() {
    try {
      localStorage.setItem(
        ORIENT_KEY,
        JSON.stringify({ invert: invert.value, mirror: mirror.value }),
      );
    } catch {
      // ignore quota / private-mode errors
    }
  }

  async function fetch(next?: PolarQuery, force = false): Promise<void> {
    const sky = useSkyStore();
    if (!sky.query) await sky.fetch(); // hydrate the location dependency
    if (next) params.value = { ...params.value, ...next };
    const eff = {
      lat: params.value.lat ?? sky.query?.location.lat,
      lon: params.value.lon ?? sky.query?.location.lon,
      at: params.value.at,
    };
    const key = queryString(eff);
    if (!force && key === lastKey && result.value) return; // cache hit
    if (inflight && key === lastKey) return inflight; // in-flight dedup
    controller?.abort();
    controller = new AbortController();
    const signal = controller.signal;
    lastKey = key;
    loading.value = true;
    error.value = "";
    inflight = (async () => {
      try {
        const data = await apiGet<PolarResponse>(
          `/api/sky/polar${key}`,
          signal,
        );
        result.value = data.result;
        query.value = data.query;
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

  const refresh = () => fetch(undefined, true);
  const setTime = (iso?: string) => fetch({ at: iso }, true);
  const setLocation = (lat?: number, lon?: number) => fetch({ lat, lon }, true);

  function setInvert(v: boolean) {
    invert.value = v;
    persistOrientation();
  }
  function setMirror(v: boolean) {
    mirror.value = v;
    persistOrientation();
  }

  // displayAngle maps a reticle position angle (clockwise from 12 o'clock, inverting straight-through)
  // to the angle for the chosen scope orientation — instant, no refetch.
  function displayAngle(pa: number): number {
    let a = pa;
    if (!invert.value) a += 180; // erect view undoes the default inversion
    if (mirror.value) a = 360 - a; // a star diagonal mirrors left↔right
    return norm360(a);
  }

  // positionAngleForRA gives the reticle position angle of any star near the pole (for the constellation
  // direction guides), using the same convention as the pole star.
  function positionAngleForRA(raDeg: number): number {
    const r = result.value;
    if (!r) return 0;
    const ha = norm360(r.lst_deg - raDeg);
    const sign = r.hemisphere === "north" ? -1 : 1;
    return norm360(180 + sign * ha);
  }

  const displayAngleDeg = computed(() =>
    result.value ? displayAngle(result.value.position_angle_deg) : 0,
  );
  const displayClockHour = computed(
    () => (((displayAngleDeg.value / 30) % 12) + 12) % 12,
  );

  return {
    result,
    query,
    loading,
    error,
    params,
    invert,
    mirror,
    fetch,
    refresh,
    setTime,
    setLocation,
    setInvert,
    setMirror,
    displayAngle,
    positionAngleForRA,
    displayAngleDeg,
    displayClockHour,
  };
});
