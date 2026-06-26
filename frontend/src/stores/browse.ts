import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet, apiPost } from "@/services/api";
import type { BrowseEntry, Inventory } from "@/types";

// All API calls for directory browsing and inspection live here; components dispatch actions.
export const useBrowseStore = defineStore("browse", () => {
  const path = ref("");
  const entries = ref<BrowseEntry[]>([]);
  const inventory = ref<Inventory | null>(null);
  const loading = ref(false);
  const error = ref("");

  async function browse(p?: string) {
    loading.value = true;
    error.value = "";
    try {
      const query = p ? `?path=${encodeURIComponent(p)}` : "";
      const data = await apiGet<{ path: string; entries: BrowseEntry[] }>(
        `/api/browse${query}`,
      );
      path.value = data.path;
      entries.value = data.entries || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  async function inspect(p: string) {
    loading.value = true;
    error.value = "";
    inventory.value = null;
    try {
      inventory.value = await apiPost<Inventory>("/api/inspect", { path: p });
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  return { path, entries, inventory, loading, error, browse, inspect };
});
