import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet } from "@/services/api";
import type { Environment } from "@/types";

// Environment health (GET /api/environment): deep tool probes + plate-solve catalogue presence, with
// human-readable run-impacting warnings — so the UI can warn BEFORE a run instead of the user
// diagnosing a silently-degraded image afterwards. Cached here (the backend caches ~5 min itself);
// refresh() re-asks and un-dismisses.
export const useEnvironmentStore = defineStore("environment", () => {
  const report = ref<Environment | null>(null);
  const loading = ref(false);
  const error = ref("");
  // Session-wide dismissal: once the user waves the warnings away they stay hidden everywhere until
  // a reload or an explicit refresh().
  const dismissed = ref(false);

  const warnings = computed<string[]>(() => report.value?.warnings ?? []);

  let inflight: Promise<void> | null = null;
  async function load(force = false): Promise<void> {
    if (report.value && !force) return;
    if (inflight) return inflight;
    inflight = (async () => {
      loading.value = true;
      error.value = "";
      try {
        report.value = await apiGet<Environment>("/api/environment");
      } catch (e) {
        error.value = (e as Error).message;
      } finally {
        loading.value = false;
        inflight = null;
      }
    })();
    return inflight;
  }

  async function refresh(): Promise<void> {
    dismissed.value = false;
    return load(true);
  }

  return { report, warnings, loading, error, dismissed, load, refresh };
});
