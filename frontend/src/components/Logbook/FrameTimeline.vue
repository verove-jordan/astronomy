<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import FilterChip from "@/components/Common/FilterChip.vue";
import { FILTER_HEX } from "@/constants/colors";
import { fmtClock } from "@/utils/tz";
import { isLight } from "@/utils/logbook";
import type { CaptureFrame } from "@/types";

// The ORDER the night was shot in, as one band per filter across the session's clock.
//
// This is the question the per-filter totals cannot answer: "did the L set finish before the cloud
// came in, or is half of it from the bad hour?" Lined up under the conditions chart, the gap in a
// band and the spike in the cloud line are the same moment.
const props = defineProps<{ frames: CaptureFrame[]; tz: string }>();
const { t } = useI18n();

const lights = computed(() =>
  props.frames.filter((f) => isLight(f.frame_type) && f.started_at > 0),
);

const span = computed(() => {
  const times = lights.value.map((f) => f.started_at);
  if (!times.length) return { start: 0, end: 0, ms: 0 };
  const start = Math.min(...times);
  // Include the last frame's own exposure, or a one-frame run would have zero width.
  const last = lights.value.reduce((a, b) =>
    a.started_at > b.started_at ? a : b,
  );
  const end = last.started_at + last.exposure_us / 1000;
  return { start, end, ms: Math.max(1, end - start) };
});

interface Band {
  filter: string;
  frames: number;
  marks: { leftPct: number; widthPct: number; title: string }[];
}

const bands = computed<Band[]>(() => {
  const byFilter = new Map<string, CaptureFrame[]>();
  for (const f of lights.value) {
    const key = f.filter || "—";
    byFilter.set(key, [...(byFilter.get(key) ?? []), f]);
  }
  return [...byFilter.entries()].map(([filter, frames]) => ({
    filter,
    frames: frames.length,
    marks: frames.map((f) => {
      const leftPct = ((f.started_at - span.value.start) / span.value.ms) * 100;
      // A 30 s sub inside a 6 h night is 0.14 % wide and would vanish; a floor keeps every frame
      // visible while the relative spacing still carries the real story.
      const widthPct = Math.max(
        0.4,
        (f.exposure_us / 1000 / span.value.ms) * 100,
      );
      return {
        leftPct,
        widthPct: Math.min(widthPct, 100 - leftPct),
        title: `#${f.sequence_no} · ${fmtClock(f.started_at, props.tz)}`,
      };
    }),
  }));
});

const hex = (filter: string) => FILTER_HEX[filter] ?? "#94a3b8";
</script>

<template>
  <div v-if="bands.length" class="space-y-2">
    <div class="flex items-center justify-between text-[11px] text-slate-400">
      <span>{{ fmtClock(span.start, tz) }}</span>
      <span>{{ fmtClock(span.end, tz) }}</span>
    </div>
    <div v-for="b in bands" :key="b.filter" class="flex items-center gap-2">
      <span class="w-16 shrink-0">
        <FilterChip :filter="b.filter" />
      </span>
      <span
        class="relative h-4 grow overflow-hidden rounded bg-slate-100 dark:bg-slate-800"
      >
        <span
          v-for="(m, i) in b.marks"
          :key="i"
          class="absolute inset-y-0 rounded-[1px]"
          :style="{
            left: `${m.leftPct}%`,
            width: `${m.widthPct}%`,
            backgroundColor: hex(b.filter),
          }"
          :title="m.title"
        />
      </span>
      <span
        class="w-10 shrink-0 text-right font-mono text-[11px] tabular-nums text-slate-500"
      >
        {{ b.frames }}
      </span>
    </div>
  </div>
  <p v-else class="text-sm text-slate-400">{{ t("logbook.timeline.empty") }}</p>
</template>
