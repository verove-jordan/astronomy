import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet } from "@/services/api";
import { useSkyStore } from "@/stores/sky";
import type { GotoResult, GotoQueryEcho, GotoResponse } from "@/types";

// GotoQuery overrides the GoTo-alignment site/time/mount; lat/lon default to the sky store's location
// so the chosen observing site stays consistent across the app.
export interface GotoQuery {
  lat?: number;
  lon?: number;
  at?: string; // RFC3339 instant; omitted = server "now"
  profile?: string; // mount/routine preset key
  count?: number; // number of alignment stars requested
}

const PREF_KEY = "astrostack.goto.prefs";

// loadPrefs restores the mount profile + star count (the only settings worth surviving a reload; the
// in-progress accepted/rejected sequence is intentionally per-session).
function loadPrefs(): { profile: string; count: number } {
  try {
    const raw = localStorage.getItem(PREF_KEY);
    if (raw) return JSON.parse(raw) as { profile: string; count: number };
  } catch {
    // ignore parse / private-mode errors
  }
  return { profile: "eq-generic", count: 3 };
}

function queryString(q: Record<string, unknown>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const useGotoStore = defineStore("goto", () => {
  const result = ref<GotoResult | null>(null);
  const query = ref<GotoQueryEcho | null>(null);
  const loading = ref(false);
  const error = ref("");

  const prefs = loadPrefs();
  const params = ref<GotoQuery>({ profile: prefs.profile, count: prefs.count });

  // The alignment sequence the user is working through (per session, not persisted): stars they have
  // centered (locked) and stars they have skipped (excluded + replaced). The server re-plans around
  // these on every change, so the helper always returns a full, well-spread ordered set.
  const accepted = ref<string[]>([]);
  const rejected = ref<string[]>([]);

  let lastKey = "";
  let inflight: Promise<void> | null = null;
  let controller: AbortController | null = null;

  function persistPrefs() {
    try {
      localStorage.setItem(
        PREF_KEY,
        JSON.stringify({
          profile: params.value.profile,
          count: params.value.count,
        }),
      );
    } catch {
      // ignore quota / private-mode errors
    }
  }

  async function fetch(next?: GotoQuery, force = false): Promise<void> {
    const sky = useSkyStore();
    if (!sky.query) await sky.fetch(); // hydrate the shared location dependency
    if (next) params.value = { ...params.value, ...next };
    const eff = {
      lat: params.value.lat ?? sky.query?.location.lat,
      lon: params.value.lon ?? sky.query?.location.lon,
      at: params.value.at,
      profile: params.value.profile,
      count: params.value.count,
      accepted: accepted.value.join(","),
      rejected: rejected.value.join(","),
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
        const data = await apiGet<GotoResponse>(`/api/sky/align${key}`, signal);
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

  // setProfile switches the mount/routine; the geometry rules change, so the in-progress sequence is
  // cleared to avoid mixing stars chosen under different constraints.
  function setProfile(profile: string): Promise<void> {
    accepted.value = [];
    rejected.value = [];
    params.value.profile = profile;
    persistPrefs();
    return fetch({ profile }, true);
  }

  function setCount(count: number): Promise<void> {
    params.value.count = count;
    persistPrefs();
    return fetch({ count }, true);
  }

  // accept locks the current star (the user centered it) and advances the sequence.
  function accept(name: string): Promise<void> {
    if (!accepted.value.includes(name)) accepted.value.push(name);
    rejected.value = rejected.value.filter((n) => n !== name);
    return fetch(undefined, true);
  }

  // skip excludes a blocked star; the server pulls in the next-best replacement that keeps the spread.
  function skip(name: string): Promise<void> {
    if (!rejected.value.includes(name)) rejected.value.push(name);
    accepted.value = accepted.value.filter((n) => n !== name);
    return fetch(undefined, true);
  }

  // undo steps back over the most recently centered star.
  function undo(): Promise<void> {
    accepted.value.pop();
    return fetch(undefined, true);
  }

  function resetSequence(): Promise<void> {
    accepted.value = [];
    rejected.value = [];
    return fetch(undefined, true);
  }

  const stars = computed(() => result.value?.stars ?? []);
  const quality = computed(() => result.value?.quality_score ?? 0);
  const warnings = computed(() => result.value?.warnings ?? []);
  const recommended = computed(
    () => stars.value.find((s) => s.status === "recommended") ?? null,
  );

  return {
    result,
    query,
    loading,
    error,
    params,
    accepted,
    rejected,
    stars,
    quality,
    warnings,
    recommended,
    fetch,
    refresh,
    setTime,
    setLocation,
    setProfile,
    setCount,
    accept,
    skip,
    undo,
    resetSequence,
  };
});
