import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet } from "@/services/api";
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
  mode?: "camera" | "visual"; // "visual" → score for the eye through the eyepiece kit
  eyepieces?: string; // encoded kit "focal:afov:label,…" (visual mode)
  at?: string; // RFC3339 instant to plan for; omitted = real-time (server "now")
}

const STORAGE_KEY = "astrostack.sky.query";
const EP_KEY = "astrostack.sky.eyepieces";

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

const SETUP_KEY = "astrostack.sky.equipmentSetups";

function loadSetups(): EquipmentSetup[] {
  try {
    const raw = localStorage.getItem(SETUP_KEY);
    return raw ? (JSON.parse(raw) as EquipmentSetup[]) : [];
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
  const equipmentSetups = ref<EquipmentSetup[]>(loadSetups()); // saved telescope/camera/eyepiece rigs

  let lastKey = "";
  let inflight: Promise<void> | null = null;
  let controller: AbortController | null = null;

  const selected = computed(
    () => targets.value.find((t) => t.name === selectedName.value) ?? null,
  );

  async function fetch(next?: SkyQuery, force = false): Promise<void> {
    if (next) params.value = { ...params.value, ...next };
    const key = queryString(params.value);
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
  function persistSetups() {
    try {
      localStorage.setItem(SETUP_KEY, JSON.stringify(equipmentSetups.value));
    } catch {
      // ignore quota / private-mode errors
    }
  }
  // saveEquipmentSetup adds a named rig, or updates the one with the same (case-insensitive) name so
  // re-saving after a tweak overwrites rather than duplicates. Returns its id.
  function saveEquipmentSetup(setup: Omit<EquipmentSetup, "id">): string {
    const name = setup.name.trim();
    if (!name) return "";
    const existing = equipmentSetups.value.find(
      (s) => s.name.toLowerCase() === name.toLowerCase(),
    );
    if (existing) {
      Object.assign(existing, setup, { name, id: existing.id });
      persistSetups();
      return existing.id;
    }
    const id = `eq${Date.now().toString(36)}`;
    equipmentSetups.value.push({ ...setup, name, id });
    persistSetups();
    return id;
  }
  function removeEquipmentSetup(id: string) {
    equipmentSetups.value = equipmentSetups.value.filter((s) => s.id !== id);
    persistSetups();
  }
  function renameEquipmentSetup(id: string, name: string) {
    const s = equipmentSetups.value.find((x) => x.id === id);
    if (!s || !name.trim()) return;
    s.name = name.trim();
    persistSetups();
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
