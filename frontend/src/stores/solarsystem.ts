import { computed, ref } from "vue";
import { defineStore } from "pinia";

import { apiGet, ApiError } from "@/services/api";
import { SOLAR_MANIFEST_VERSION } from "@/utils/solarsystem";
import type { SolarBody, SolarManifest, SolarSnapshot } from "@/types";

/**
 * The solar-system model and the engine's readout of one instant.
 *
 * The manifest is fetched once and kept: it is the whole model, and the page animates from it
 * without asking again. The snapshot is the opposite — one instant, fetched only when a number has
 * to be printed, and deliberately NOT on every frame.
 */
export const useSolarSystemStore = defineStore("solarsystem", () => {
  const manifest = ref<SolarManifest | null>(null);
  const snapshot = ref<SolarSnapshot | null>(null);
  const loading = ref(false);
  const error = ref<string | null>(null);
  /** Set when the engine and this page disagree about the model's shape. */
  const versionMismatch = ref(false);

  let manifestFlight: Promise<void> | null = null;
  let stateController: AbortController | null = null;

  const byKey = computed(() => {
    const m = new Map<string, SolarBody>();
    for (const b of manifest.value?.bodies ?? []) m.set(b.key, b);
    return m;
  });

  /** Bodies grouped for the picker: each planet followed by its own moons, as the engine ordered them. */
  const bodies = computed(() => manifest.value?.bodies ?? []);

  const textures = computed(() => new Set(manifest.value?.textures ?? []));

  function hasTexture(key: string | undefined): boolean {
    return !!key && textures.value.has(key);
  }

  async function fetchManifest(force = false): Promise<void> {
    if (!force && manifest.value) return;
    if (manifestFlight) return manifestFlight;

    loading.value = true;
    error.value = null;
    manifestFlight = (async () => {
      try {
        manifest.value = await apiGet<SolarManifest>(
          `/api/solarsystem/bodies?v=${SOLAR_MANIFEST_VERSION}`,
        );
        versionMismatch.value = false;
      } catch (e) {
        // A 409 is the engine telling us the page is out of date. It is not a network failure and
        // must not be reported as one — the fix is a reload, and the UI says so.
        if (e instanceof ApiError && e.status === 409)
          versionMismatch.value = true;
        else error.value = (e as Error).message;
      } finally {
        loading.value = false;
        manifestFlight = null;
      }
    })();
    return manifestFlight;
  }

  /**
   * fetchState asks the engine what is true at one instant. Every call supersedes the one before —
   * scrubbing the timeline fires these as fast as the pointer moves, and only the last answer is
   * wanted.
   */
  async function fetchState(
    timeMs: number,
    site?: { lat: number; lon: number },
  ): Promise<void> {
    stateController?.abort();
    stateController = new AbortController();
    const params = new URLSearchParams({ t: String(Math.round(timeMs)) });
    if (site) {
      params.set("lat", String(site.lat));
      params.set("lon", String(site.lon));
    }
    try {
      snapshot.value = await apiGet<SolarSnapshot>(
        `/api/solarsystem/state?${params}`,
        stateController.signal,
      );
    } catch (e) {
      if ((e as Error).name !== "AbortError")
        error.value = (e as Error).message;
    }
  }

  function stateFor(key: string) {
    return snapshot.value?.bodies.find((b) => b.key === key) ?? null;
  }

  return {
    manifest,
    snapshot,
    loading,
    error,
    versionMismatch,
    bodies,
    byKey,
    textures,
    hasTexture,
    fetchManifest,
    fetchState,
    stateFor,
  };
});
