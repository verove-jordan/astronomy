import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, apiPost, apiPut, apiDelete } from "@/services/api";
import type { PresetItem, PresetPayload } from "@/types";

// Processing presets: the built-in "best params per situation" catalog (served read-only from the engine)
// plus the user's saved presets (persisted in Postgres). A preset is a partial /api/jobs body the Import
// view re-applies to the launch form. All API calls live here (Vue convention); components read state and
// dispatch actions. The list is fetched once and shared (cache + in-flight dedup), refreshed after a write.

const CATEGORY_ORDER = [
  "deepsky",
  "nebula",
  "narrowband",
  "solar",
  "comet",
  "milkyway",
];

export const usePresetsStore = defineStore("presets", () => {
  const presets = ref<PresetItem[]>([]);
  const loading = ref(false);
  const error = ref("");
  let loaded = false;
  let inflight: Promise<void> | null = null;

  async function list(force = false): Promise<void> {
    if (!force && loaded) return; // cache hit
    if (inflight) return inflight; // in-flight dedup
    loading.value = true;
    error.value = "";
    inflight = (async () => {
      try {
        const d = await apiGet<{ presets: PresetItem[] }>("/api/presets");
        presets.value = d.presets || [];
        loaded = true;
      } catch (e) {
        error.value = (e as Error).message;
      } finally {
        loading.value = false;
        inflight = null;
      }
    })();
    return inflight;
  }

  // save upserts a user preset by name (re-saving the same name overwrites), then refreshes the list.
  async function save(name: string, payload: PresetPayload): Promise<void> {
    await apiPost("/api/presets", { name, payload });
    await list(true);
  }
  async function rename(id: number, name: string): Promise<void> {
    await apiPut(`/api/presets/${id}`, { name });
    await list(true);
  }
  async function remove(id: number): Promise<void> {
    await apiDelete(`/api/presets/${id}`);
    await list(true);
  }

  const builtins = computed(() => presets.value.filter((p) => p.builtin));
  const userPresets = computed(() => presets.value.filter((p) => !p.builtin));

  // byCategory groups the picker: built-ins by category in a stable order, then the user's own presets
  // under a synthetic "mine" group (only when non-empty).
  const byCategory = computed(() => {
    const groups: { key: string; items: PresetItem[] }[] = [];
    for (const cat of CATEGORY_ORDER) {
      const items = builtins.value.filter((p) => p.category === cat);
      if (items.length) groups.push({ key: cat, items });
    }
    if (userPresets.value.length) {
      groups.push({ key: "mine", items: userPresets.value });
    }
    return groups;
  });

  return {
    presets,
    loading,
    error,
    list,
    save,
    rename,
    remove,
    builtins,
    userPresets,
    byCategory,
  };
});
