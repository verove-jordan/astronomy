import { computed, ref, toValue, watch, type MaybeRefOrGetter } from "vue";
import type { Inventory } from "@/types";

// Channels the user can assign a detected filter to. "ignore" excludes those frames from stacking.
export const CHANNEL_TARGETS = [
  "L",
  "R",
  "G",
  "B",
  "Ha",
  "OIII",
  "SII",
  "ignore",
] as const;

// useChannelMapping exposes the distinct detected/known filters and a user-editable mapping from
// each to a target channel. `overrides` is the compact diff to POST as filter_map (identity entries
// are dropped). It self-resets to identity whenever the detected filters change.
export function useChannelMapping(
  inv: MaybeRefOrGetter<Inventory | null | undefined>,
) {
  const detectedFilters = computed<string[]>(() => {
    const v = toValue(inv);
    const det = v?.channel_detection;
    if (det && det.runs.length) {
      return [...new Set(det.runs.map((r) => r.filter).filter(Boolean))];
    }
    const seen = new Set<string>();
    for (const s of v?.sets ?? []) {
      if (s.key.type === "LIGHT" && s.key.filter) seen.add(s.key.filter);
    }
    return [...seen];
  });

  const mapping = ref<Record<string, string>>({});

  function reset() {
    const m: Record<string, string> = {};
    for (const f of detectedFilters.value) m[f] = f;
    mapping.value = m;
  }
  watch(detectedFilters, reset, { immediate: true });

  const overrides = computed<Record<string, string>>(() => {
    const out: Record<string, string> = {};
    for (const [k, v] of Object.entries(mapping.value)) {
      if (v !== k) out[k] = v;
    }
    return out;
  });

  return { detectedFilters, mapping, overrides, reset };
}
