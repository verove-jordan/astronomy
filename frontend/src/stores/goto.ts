import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet } from "@/services/api";
import { useSkyStore } from "@/stores/sky";
import type {
  GotoProfile,
  GotoResult,
  GotoQueryEcho,
  GotoResponse,
} from "@/types";

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

  // Mount/routine presets from the backend registry (count bounds, phase structure, hand-controller
  // star-list key) — fetched once and cached; concurrent callers share the in-flight request.
  const profiles = ref<GotoProfile[]>([]);
  let profilesInflight: Promise<void> | null = null;
  async function fetchProfiles(): Promise<void> {
    if (profiles.value.length) return; // cache hit
    if (profilesInflight) return profilesInflight;
    profilesInflight = (async () => {
      try {
        const data = await apiGet<{ profiles: GotoProfile[] }>(
          "/api/sky/align/profiles",
        );
        profiles.value = data.profiles ?? [];
      } catch (e) {
        error.value = (e as Error).message;
      } finally {
        profilesInflight = null;
      }
    })();
    return profilesInflight;
  }

  // The selected profile's registry entry (count bounds + phase structure for the controls).
  const currentProfile = computed(
    () => profiles.value.find((p) => p.key === params.value.profile) ?? null,
  );

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

  // applyLocalStatuses re-derives the card statuses from the accepted set, mirroring the backend's
  // buildStars ordering: accepted names → "accepted"; the first non-accepted (in order) → "recommended";
  // the rest → "upcoming". The greedy plan is prefix-stable, so accepting the current recommended star
  // never changes the ordering — advancing locally lets the sequence move instantly while the server
  // reconciles in the background (see accept/undo/skip).
  function applyLocalStatuses() {
    const r = result.value;
    if (!r) return;
    const acc = new Set(accepted.value.map((n) => n.toLowerCase()));
    let gaveRecommended = false;
    r.stars = r.stars.map((s) => {
      if (acc.has(s.name.toLowerCase()))
        return { ...s, status: "accepted" as const };
      if (!gaveRecommended) {
        gaveRecommended = true;
        return { ...s, status: "recommended" as const };
      }
      return { ...s, status: "upcoming" as const };
    });
  }

  // silent (accept/skip/undo) reconciles with the server WITHOUT toggling `loading`, so the optimistic
  // local advance is never interrupted by a spinner; `result`/`query` are still only replaced on success.
  async function fetch(
    next?: GotoQuery,
    force = false,
    silent = false,
  ): Promise<void> {
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
    if (!silent) loading.value = true;
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
        if (!silent) loading.value = false;
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

  // accept locks the current star (the user centered it) and advances the sequence. The advance is
  // applied locally so the UI moves instantly; the server reconciles silently in the background.
  function accept(name: string): Promise<void> {
    if (!accepted.value.includes(name)) accepted.value.push(name);
    rejected.value = rejected.value.filter((n) => n !== name);
    applyLocalStatuses();
    return fetch(undefined, true, true);
  }

  // skip excludes a blocked star; the server pulls in the next-best replacement that keeps the spread.
  // The current list stays visible (no spinner) until the replacement lands.
  function skip(name: string): Promise<void> {
    if (!rejected.value.includes(name)) rejected.value.push(name);
    accepted.value = accepted.value.filter((n) => n !== name);
    return fetch(undefined, true, true);
  }

  // undo steps back over the most recently centered star (instant locally, reconciled in the background).
  function undo(): Promise<void> {
    accepted.value.pop();
    applyLocalStatuses();
    return fetch(undefined, true, true);
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
  // Moon + naked-eye planets currently up, for the sky map's landmarks.
  const bodies = computed(() => result.value?.sky_bodies ?? []);
  // How many stars of the plan are alignment-phase (0 = single-phase profile, no grouping).
  const alignCount = computed(
    () => stars.value.filter((s) => s.phase === "align").length,
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
    bodies,
    alignCount,
    profiles,
    currentProfile,
    fetchProfiles,
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
