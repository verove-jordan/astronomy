import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet } from "@/services/api";
import type { DarkSite, DarkSitesResult } from "@/types";

// Query for the dark-sky finder: a map area, a Bortle ceiling, and whether to score horizon openness.
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
}

// useDarkSkyStore holds the dark-sky finder results. The single API call lives here; the view supplies
// the drawn area + threshold and reads back the ranked candidates.
export const useDarkSkyStore = defineStore("darksky", () => {
  const candidates = ref<DarkSite[]>([]);
  const cellsScanned = ref(0);
  const warnings = ref<string[]>([]);
  const loading = ref(false);
  const error = ref("");
  const searched = ref(false);

  async function find(q: DarkSiteQuery): Promise<void> {
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
    try {
      const data = await apiGet<DarkSitesResult>(
        `/api/sky/darksites?${params.toString()}`,
      );
      candidates.value = data.candidates ?? [];
      cellsScanned.value = data.cells_scanned ?? 0;
      warnings.value = data.warnings ?? [];
    } catch (e) {
      error.value = (e as Error).message;
      candidates.value = [];
    } finally {
      loading.value = false;
      searched.value = true;
    }
  }

  function reset(): void {
    candidates.value = [];
    cellsScanned.value = 0;
    warnings.value = [];
    error.value = "";
    searched.value = false;
  }

  return {
    candidates,
    cellsScanned,
    warnings,
    loading,
    error,
    searched,
    find,
    reset,
  };
});
