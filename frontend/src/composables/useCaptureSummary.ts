import { computed, toValue, type MaybeRefOrGetter } from "vue";
import type { ChannelResult, Inventory } from "@/types";

export interface FilterSummary {
  filter: string;
  count: number;
  integrationMs: number;
}

export interface CaptureSummary {
  hasData: boolean;
  objects: string[];
  lightCount: number;
  totalIntegrationMs: number;
  filters: FilterSummary[];
  frameTypeCounts: Record<string, number>;
}

const empty: CaptureSummary = {
  hasData: false,
  objects: [],
  lightCount: 0,
  totalIntegrationMs: 0,
  filters: [],
  frameTypeCounts: {},
};

// useCaptureSummary derives the per-capture overview (counts, integration, per-filter) from a
// scanned Inventory. Reused by ImportView and (when an inventory is available) JobView.
export function useCaptureSummary(
  inv: MaybeRefOrGetter<Inventory | null | undefined>,
) {
  return computed<CaptureSummary>(() => summaryFromInventory(toValue(inv)));
}

export function summaryFromInventory(
  v: Inventory | null | undefined,
): CaptureSummary {
  const frames = v?.frames ?? [];
  if (frames.length === 0) return empty;
  const frameTypeCounts: Record<string, number> = {};
  const filterMap = new Map<string, FilterSummary>();
  const objects = new Set<string>();
  let lightCount = 0;
  let totalIntegrationMs = 0;
  for (const f of frames) {
    frameTypeCounts[f.type] = (frameTypeCounts[f.type] || 0) + 1;
    if (f.type !== "LIGHT") continue;
    lightCount++;
    totalIntegrationMs += f.exposure_ms;
    if (f.object) objects.add(f.object);
    const key = f.filter || "—";
    const fs = filterMap.get(key) || {
      filter: key,
      count: 0,
      integrationMs: 0,
    };
    fs.count++;
    fs.integrationMs += f.exposure_ms;
    filterMap.set(key, fs);
  }
  return {
    hasData: true,
    objects: [...objects],
    lightCount,
    totalIntegrationMs,
    filters: [...filterMap.values()],
    frameTypeCounts,
  };
}

// summaryFromChannels builds an equivalent overview from a finished run's channels (no inventory),
// so an old run reopened from disk still shows a capture summary.
export function summaryFromChannels(
  object: string,
  channels: ChannelResult[],
): CaptureSummary {
  if (!channels.length) return empty;
  let lightCount = 0;
  let totalIntegrationMs = 0;
  const filters: FilterSummary[] = channels.map((c) => {
    lightCount += c.input_frames;
    const integ = c.exposure_ms * c.input_frames;
    totalIntegrationMs += integ;
    return {
      filter: c.filter || "—",
      count: c.input_frames,
      integrationMs: integ,
    };
  });
  return {
    hasData: true,
    objects: object ? [object] : [],
    lightCount,
    totalIntegrationMs,
    filters,
    frameTypeCounts: { LIGHT: lightCount },
  };
}
