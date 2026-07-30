import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet } from "@/services/api";
import { useEquipmentStore } from "@/stores/equipment";
import type {
  SkyTarget,
  SkyEyepiece,
  DarkWindow,
  SkyQueryEcho,
  SkyTargetsResponse,
  SiteQuality,
  LocationFavorite,
  EquipmentSetup,
} from "@/types";

// SkyQuery holds the server-side scoring inputs the user can override; changing any triggers a
// refetch. Anything omitted falls back to the engine's configured defaults.
export interface SkyQuery {
  lat?: number;
  lon?: number;
  elevation_m?: number;
  focal_mm?: number;
  aperture_mm?: number;
  pixel_um?: number;
  sensor_w?: number;
  sensor_h?: number;
  barlow?: number; // Barlow factor; omitted = no Barlow (×1)
  min_alt?: number;
  twilight?: "astro" | "nautical";
  limit?: number;
  max_mag?: number; // limiting magnitude: hide objects fainter than this (≥ MAG_ALL = off)
  mode?: "camera" | "visual"; // "visual" → score for the eye through the eyepiece kit
  eyepieces?: string; // encoded kit "focal:afov:label,…" (visual mode)
  at?: string; // RFC3339 instant to plan for; omitted = real-time (server "now")
}

const STORAGE_KEY = "astrostack.sky.query";
const EP_KEY = "astrostack.sky.eyepieces";

// TARGET_LIMIT is how many score-ranked rows every fetch asks for. The backend's own default (50)
// is what used to hide galaxies: fainter objects rank below the cut under moonlight/light
// pollution, so the magnitude slider needs a deep pool to reveal anything. Scoring all ~12k
// records happens server-side regardless — the limit only bounds the payload.
const TARGET_LIMIT = 400;
// MAG_ALL is the slider's right end: at or beyond it no max_mag is sent (show everything).
export const MAG_ALL = 16;

// DEFAULT_KIT mirrors the engine's default eyepiece set (a sane FC-100 visual kit) so the editor is
// populated out of the box before the user customizes it.
const DEFAULT_KIT: SkyEyepiece[] = [
  { label: "30mm", focal_mm: 30, afov_deg: 68 },
  { label: "18mm", focal_mm: 18, afov_deg: 65 },
  { label: "10mm", focal_mm: 10, afov_deg: 60 },
  { label: "6mm", focal_mm: 6, afov_deg: 60 },
];

function loadKit(): SkyEyepiece[] {
  try {
    const raw = localStorage.getItem(EP_KEY);
    if (raw) return JSON.parse(raw) as SkyEyepiece[];
  } catch {
    // ignore parse / private-mode errors
  }
  return DEFAULT_KIT.map((e) => ({ ...e }));
}

// encodeKit serializes the kit to the query form the engine parses (focal:afov:label, comma-joined),
// dropping incomplete rows.
function encodeKit(list: SkyEyepiece[]): string {
  return list
    .filter((e) => e.focal_mm > 0 && e.afov_deg > 0)
    .map((e) => `${e.focal_mm}:${e.afov_deg}:${e.label || `${e.focal_mm}mm`}`)
    .join(",");
}

function loadPersisted(): SkyQuery {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as SkyQuery) : {};
  } catch {
    return {};
  }
}

function persist(q: SkyQuery) {
  try {
    const { at: _at, ...rest } = q; // never persist a specific time — reopen in real-time
    localStorage.setItem(STORAGE_KEY, JSON.stringify(rest));
  } catch {
    // ignore quota / private-mode errors
  }
}

const FAV_KEY = "astrostack.sky.favorites";

function loadFavorites(): Set<string> {
  try {
    const raw = localStorage.getItem(FAV_KEY);
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}

const LOC_FAV_KEY = "astrostack.sky.locationFavorites";

function loadLocationFavorites(): LocationFavorite[] {
  try {
    const raw = localStorage.getItem(LOC_FAV_KEY);
    return raw ? (JSON.parse(raw) as LocationFavorite[]) : [];
  } catch {
    return [];
  }
}

function queryString(q: SkyQuery): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const useSkyStore = defineStore("sky", () => {
  const targets = ref<SkyTarget[]>([]);
  const query = ref<SkyQueryEcho | null>(null); // effective setup echoed by the server
  const darkWindow = ref<DarkWindow | null>(null);
  const warnings = ref<string[]>([]);
  const site = ref<SiteQuality | null>(null); // light pollution at the current site (badge + scoring)
  const loading = ref(false);
  const error = ref("");
  const selectedName = ref<string | null>(null);
  const params = ref<SkyQuery>(loadPersisted());
  const favorites = ref<Set<string>>(loadFavorites()); // object names, persisted locally
  const locationFavorites = ref<LocationFavorite[]>(loadLocationFavorites()); // saved sites, persisted
  const eyepieceKit = ref<SkyEyepiece[]>(loadKit()); // visual-mode kit, persisted locally

  // Saved telescope/camera rigs now live server-side (stores/equipment.ts) so the desktop planner and
  // the phone at the scope share them; this store keeps the old surface so existing callers are
  // unchanged. The equipment store imports any legacy localStorage rigs on its first load.
  const equipment = useEquipmentStore();
  void equipment.load();
  const equipmentSetups = computed<EquipmentSetup[]>(() => equipment.setups);

  let lastKey = "";
  let inflight: Promise<void> | null = null;
  let controller: AbortController | null = null;

  const selected = computed(
    () => targets.value.find((t) => t.name === selectedName.value) ?? null,
  );

  async function fetch(next?: SkyQuery, force = false): Promise<void> {
    if (next) params.value = { ...params.value, ...next };
    const effective: SkyQuery = { limit: TARGET_LIMIT, ...params.value };
    if ((effective.max_mag ?? MAG_ALL) >= MAG_ALL) delete effective.max_mag;
    const key = queryString(effective);
    if (!force && key === lastKey && targets.value.length) return; // cache hit
    if (inflight && key === lastKey) return inflight; // in-flight dedup
    controller?.abort();
    controller = new AbortController();
    const signal = controller.signal;
    lastKey = key;
    loading.value = true;
    error.value = "";
    inflight = (async () => {
      try {
        const data = await apiGet<SkyTargetsResponse>(
          `/api/sky/targets${key}`,
          signal,
        );
        targets.value = data.targets ?? [];
        query.value = data.query;
        darkWindow.value = data.darkness;
        warnings.value = data.warnings ?? [];
        site.value = data.site ?? null;
        persist(params.value);
        // Keep the current selection if it survived; otherwise default to the top target.
        if (!targets.value.some((t) => t.name === selectedName.value)) {
          selectedName.value = targets.value[0]?.name ?? null;
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

  // refresh re-scores with the SAME params (altitudes and "now" advance over time).
  const refresh = () => fetch(undefined, true);

  // setMode switches between the imaging (camera) and visual (eyepiece) planners; visual sends the
  // current kit so the engine can recommend an eyepiece per target.
  function setMode(mode: "camera" | "visual"): Promise<void> {
    const eyepieces =
      mode === "visual" ? encodeKit(eyepieceKit.value) : undefined;
    return fetch({ mode, eyepieces }, true);
  }

  // setEyepieceKit persists the visual kit and, when in visual mode, re-scores with it.
  function setEyepieceKit(list: SkyEyepiece[]) {
    eyepieceKit.value = list;
    try {
      localStorage.setItem(EP_KEY, JSON.stringify(list));
    } catch {
      // ignore quota / private-mode errors
    }
    if (params.value.mode === "visual") {
      fetch({ eyepieces: encodeKit(list) }, true);
    }
  }

  const select = (name: string | null) => {
    selectedName.value = name;
  };

  // setObserver sets just the observing location (lat/lon) and persists it, WITHOUT re-scoring targets —
  // for tabs that need an origin (e.g. the dark-sky finder's distance/route column) but not a fetch. The
  // next targets fetch picks it up; the finder reads params.lat/lon directly.
  function setObserver(lat: number, lon: number): void {
    params.value = { ...params.value, lat, lon };
    persist(params.value);
  }

  // reset clears the user's overrides; the next fetch re-seeds from the server defaults.
  function reset(): Promise<void> {
    params.value = {};
    persist(params.value);
    return fetch(undefined, true);
  }

  function isFavorite(name: string): boolean {
    return favorites.value.has(name);
  }

  function toggleFavorite(name: string) {
    if (favorites.value.has(name)) favorites.value.delete(name);
    else favorites.value.add(name);
    try {
      localStorage.setItem(FAV_KEY, JSON.stringify([...favorites.value]));
    } catch {
      // ignore quota / private-mode errors
    }
  }

  // favLocKey rounds to ~11 m so saving the same spot twice is idempotent and "is the current site
  // saved?" is a cheap lookup. It is the only place the id formula lives.
  function favLocKey(lat: number, lon: number): string {
    return `${lat.toFixed(4)},${lon.toFixed(4)}`;
  }
  function persistLocationFavorites() {
    try {
      localStorage.setItem(
        LOC_FAV_KEY,
        JSON.stringify(locationFavorites.value),
      );
    } catch {
      // ignore quota / private-mode errors
    }
  }
  function isLocationFavorite(lat: number, lon: number): boolean {
    const id = favLocKey(lat, lon);
    return locationFavorites.value.some((f) => f.id === id);
  }
  // toggleLocationFavorite saves the site, or removes it when the same coordinates are already saved.
  function toggleLocationFavorite(fav: Omit<LocationFavorite, "id">) {
    const id = favLocKey(fav.lat, fav.lon);
    const i = locationFavorites.value.findIndex((f) => f.id === id);
    if (i >= 0) locationFavorites.value.splice(i, 1);
    else locationFavorites.value.push({ ...fav, id });
    persistLocationFavorites();
  }
  function removeLocationFavorite(id: string) {
    locationFavorites.value = locationFavorites.value.filter(
      (f) => f.id !== id,
    );
    persistLocationFavorites();
  }
  function renameLocationFavorite(id: string, label: string) {
    const fav = locationFavorites.value.find((f) => f.id === id);
    if (!fav) return;
    fav.label = label;
    persistLocationFavorites();
  }

  // --- Equipment setups (named telescope + camera + eyepiece rigs) -----------------------------------
  // Thin delegates to the server-backed equipment store: same call signatures the UI already uses,
  // so nothing downstream changed when these moved off localStorage. The server upserts by name, so
  // re-saving a tweaked rig still overwrites rather than duplicating.
  function saveEquipmentSetup(setup: Omit<EquipmentSetup, "id">): string {
    const name = setup.name.trim();
    if (!name) return "";
    void equipment.save(setup);
    return (
      equipment.setups.find((s) => s.name.toLowerCase() === name.toLowerCase())
        ?.id ?? ""
    );
  }
  function removeEquipmentSetup(id: string) {
    void equipment.remove(id);
  }
  function renameEquipmentSetup(id: string, name: string) {
    void equipment.rename(id, name);
  }

  return {
    targets,
    query,
    darkWindow,
    warnings,
    site,
    loading,
    error,
    selectedName,
    selected,
    params,
    fetch,
    refresh,
    select,
    setObserver,
    reset,
    favorites,
    isFavorite,
    toggleFavorite,
    locationFavorites,
    favLocKey,
    isLocationFavorite,
    toggleLocationFavorite,
    removeLocationFavorite,
    renameLocationFavorite,
    eyepieceKit,
    setMode,
    setEyepieceKit,
    equipmentSetups,
    saveEquipmentSetup,
    removeEquipmentSetup,
    renameEquipmentSetup,
  };
});
