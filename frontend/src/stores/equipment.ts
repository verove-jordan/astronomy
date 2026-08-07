import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { apiDelete, apiGet, apiPost, apiPut } from "@/services/api";
import type { EquipmentSetup, EquipmentSetupRow } from "@/types";

// Named telescope + camera rigs, backed by the engine (table equipment_setups) instead of
// localStorage: a mosaic is planned on the desktop and executed from the phone at the scope, and the
// two devices MUST agree on the optics or every tile lands in the wrong place.
//
// The store exposes the legacy `EquipmentSetup` shape (string id, sensor_w/sensor_h/barlow) so every
// existing Tonight caller keeps working unchanged; the row↔setup projection lives here, once.

const LEGACY_KEY = "astrostack.sky.equipmentSetups";
const MIGRATED_KEY = "astrostack.equipment.migrated";

function setupOfRow(row: EquipmentSetupRow): EquipmentSetup {
  return {
    id: String(row.id),
    name: row.name,
    focal_mm: row.focal_mm || undefined,
    aperture_mm: row.aperture_mm || undefined,
    barlow: row.barlow_x || undefined,
    reducer: row.reducer_x || undefined,
    pixel_um: row.pixel_um || undefined,
    sensor_w: row.sensor_w_px || undefined,
    sensor_h: row.sensor_h_px || undefined,
    camera_name: row.camera_name || undefined,
    eyepieces: row.eyepieces ?? [],
  };
}

function bodyOfSetup(setup: Omit<EquipmentSetup, "id">) {
  return {
    name: setup.name,
    focal_mm: setup.focal_mm ?? 0,
    aperture_mm: setup.aperture_mm ?? 0,
    pixel_um: setup.pixel_um ?? 0,
    sensor_w_px: setup.sensor_w ?? 0,
    sensor_h_px: setup.sensor_h ?? 0,
    barlow_x: setup.barlow ?? 0,
    reducer_x: setup.reducer ?? 0,
    camera_name: setup.camera_name ?? "",
    eyepieces: setup.eyepieces ?? [],
  };
}

function readLegacySetups(): EquipmentSetup[] {
  try {
    const raw = localStorage.getItem(LEGACY_KEY);
    return raw ? (JSON.parse(raw) as EquipmentSetup[]) : [];
  } catch {
    return [];
  }
}

export const useEquipmentStore = defineStore("equipment", () => {
  const setups = ref<EquipmentSetup[]>([]);
  const loaded = ref(false);
  const error = ref("");
  let inflight: Promise<void> | null = null;

  async function load(force = false): Promise<void> {
    if (loaded.value && !force) return;
    if (inflight) return inflight;
    inflight = (async () => {
      try {
        const res = await apiGet<{ setups: EquipmentSetupRow[] }>(
          "/api/equipment",
        );
        setups.value = (res.setups ?? []).map(setupOfRow);
        loaded.value = true;
        error.value = "";
        await migrateLegacy();
      } catch (e) {
        error.value = e instanceof Error ? e.message : String(e);
      } finally {
        inflight = null;
      }
    })();
    return inflight;
  }

  // migrateLegacy imports the browser's old localStorage rigs exactly once. It never deletes the
  // legacy copy (it stays as a manual fallback) and never re-runs, so deleting an imported rig on
  // the server doesn't resurrect it on the next load.
  async function migrateLegacy(): Promise<void> {
    let done = false;
    try {
      done = localStorage.getItem(MIGRATED_KEY) === "1";
    } catch {
      return; // no storage access at all — nothing to migrate
    }
    if (done) return;
    const legacy = readLegacySetups();
    const known = new Set(setups.value.map((s) => s.name.toLowerCase()));
    for (const old of legacy) {
      if (!old.name || known.has(old.name.toLowerCase())) continue;
      try {
        await save(old);
      } catch {
        return; // leave the flag unset so the import is retried on the next load
      }
    }
    try {
      localStorage.setItem(MIGRATED_KEY, "1");
    } catch {
      // private mode — the import simply runs again next time, which is idempotent by name
    }
  }

  // save upserts by NAME (the server's unique LOWER(name) index), matching the behaviour the
  // browser-local version had: re-saving a tweaked rig overwrites instead of duplicating.
  async function save(setup: Omit<EquipmentSetup, "id">): Promise<string> {
    const name = setup.name.trim();
    if (!name) return "";
    const res = await apiPost<{ setup: EquipmentSetupRow }>("/api/equipment", {
      ...bodyOfSetup(setup),
      name,
    });
    const saved = setupOfRow(res.setup);
    const i = setups.value.findIndex((s) => s.id === saved.id);
    if (i >= 0) setups.value[i] = saved;
    else setups.value.push(saved);
    return saved.id;
  }

  async function update(
    id: string,
    patch: Partial<Omit<EquipmentSetup, "id">>,
  ): Promise<void> {
    const current = setups.value.find((s) => s.id === id);
    if (!current) return;
    const merged = { ...current, ...patch };
    const res = await apiPut<{ setup: EquipmentSetupRow }>(
      `/api/equipment/${id}`,
      bodyOfSetup(merged),
    );
    const i = setups.value.findIndex((s) => s.id === id);
    if (i >= 0) setups.value[i] = setupOfRow(res.setup);
  }

  async function rename(id: string, name: string): Promise<void> {
    if (!name.trim()) return;
    await update(id, { name: name.trim() });
  }

  async function remove(id: string): Promise<void> {
    await apiDelete(`/api/equipment/${id}`);
    setups.value = setups.value.filter((s) => s.id !== id);
  }

  function byId(id: string): EquipmentSetup | undefined {
    return setups.value.find((s) => s.id === id);
  }

  const names = computed(() => setups.value.map((s) => s.name));

  return {
    setups,
    loaded,
    error,
    names,
    load,
    save,
    update,
    rename,
    remove,
    byId,
  };
});
