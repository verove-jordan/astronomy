<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import { use } from "echarts/core";
import { ScatterChart } from "echarts/charts";
import { PolarComponent, TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import VChart from "vue-echarts";
import { useI18n } from "vue-i18n";
import { useSkyStore } from "@/stores/sky";
import { scoreTier } from "@/constants/styles";
import {
  SCORE_TIER_HEX,
  CHART_AXIS,
  CHART_GRID,
  MAP_SELECTED,
} from "@/constants/colors";
import { altAzAt } from "@/utils/altaz";
import { tzForLocation, fmtClock } from "@/utils/tz";
import IconPlay from "@/components/Icons/IconPlay.vue";
import IconPause from "@/components/Icons/IconPause.vue";
import type { SkyTarget } from "@/types";

use([ScatterChart, PolarComponent, TooltipComponent, CanvasRenderer]);

const { t } = useI18n();
const store = useSkyStore();

interface SkyPoint {
  name: string;
  value: [number, number]; // [radius = 90 − altitude, azimuth]
  altNow: number;
  azNow: number;
  below?: boolean;
  itemStyle?: { color: string };
}

// ----- Night playbar: scrub the map through the selected night (positions recomputed per step). ---
// scrubMs == null → "live": plot the payload's alt_now/az_now exactly as before. A scrubbed instant
// recomputes each target's alt/az client-side (utils/altaz mirrors the backend math), so the dots
// wheel around the pole as the night advances.
const SCRUB_STEP_MS = 15 * 60 * 1000;
const PLAY_TICK_MS = 500;

const scrubMs = ref<number | null>(null);
const playing = ref(false);
let playTimer: ReturnType<typeof setInterval> | null = null;

const nightStartMs = computed(() => store.darkWindow?.night_start_ms ?? 0);
const nightEndMs = computed(() => store.darkWindow?.night_end_ms ?? 0);
const site = computed(() => store.query?.location ?? null);
const playbarReady = computed(
  () =>
    !!site.value &&
    nightStartMs.value > 0 &&
    nightEndMs.value > nightStartMs.value,
);
const tz = computed(() => {
  const l = site.value;
  return l
    ? tzForLocation(l.lat, l.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone;
});
const scrubLabel = computed(() =>
  scrubMs.value == null
    ? t("tonight.map.playbar.live")
    : fmtClock(scrubMs.value, tz.value),
);

function posOf(tg: SkyTarget): { alt: number; az: number } {
  const l = site.value;
  if (scrubMs.value == null || !l)
    return { alt: tg.alt_now_deg, az: tg.az_now_deg };
  const p = altAzAt(tg.ra_deg, tg.dec_deg, l.lat, l.lon, scrubMs.value);
  return { alt: p.altDeg, az: p.azDeg };
}

function stopPlaying() {
  playing.value = false;
  if (playTimer) {
    clearInterval(playTimer);
    playTimer = null;
  }
}
function togglePlay() {
  if (playing.value) {
    stopPlaying();
    return;
  }
  if (!playbarReady.value) return;
  if (scrubMs.value == null) scrubMs.value = nightStartMs.value;
  playing.value = true;
  playTimer = setInterval(() => {
    const next = (scrubMs.value ?? nightStartMs.value) + SCRUB_STEP_MS;
    scrubMs.value = next > nightEndMs.value ? nightStartMs.value : next;
  }, PLAY_TICK_MS);
}
function onSlider(e: Event) {
  stopPlaying();
  scrubMs.value = Number((e.target as HTMLInputElement).value);
}
function resetLive() {
  stopPlaying();
  scrubMs.value = null;
}
onBeforeUnmount(stopPlaying);

// Only targets above the horizon (at the plotted instant) can be drawn on the sky.
const points = computed<SkyPoint[]>(() =>
  store.targets
    .map((tg) => ({ tg, p: posOf(tg) }))
    .filter(({ p }) => p.alt > 0)
    .map(({ tg, p }) => ({
      name: tg.name,
      value: [90 - p.alt, p.az] as [number, number],
      altNow: Math.round(p.alt),
      azNow: Math.round(p.az),
      itemStyle: { color: SCORE_TIER_HEX[scoreTier(tg.score)] },
    })),
);

// The selected target, highlighted with a bright ring + name label so it stands out among the dots.
// When it is below the horizon it cannot be plotted, so a rim marker at its azimuth keeps the selection
// visible instead of silently vanishing.
const selectedOverlay = computed<SkyPoint[]>(() => {
  const tg = store.selected;
  if (!tg) return [];
  const p = posOf(tg);
  if (p.alt <= 0) return [];
  return [
    {
      name: tg.name,
      value: [90 - p.alt, p.az],
      altNow: Math.round(p.alt),
      azNow: Math.round(p.az),
    },
  ];
});

const belowHorizonHint = computed<SkyPoint[]>(() => {
  const tg = store.selected;
  if (!tg) return [];
  const p = posOf(tg);
  if (p.alt > 0) return [];
  return [
    {
      name: tg.name,
      value: [89, p.az], // pinned just inside the horizon rim at its azimuth
      altNow: Math.round(p.alt),
      azNow: Math.round(p.az),
      below: true,
    },
  ];
});

const compass: Record<number, string> = { 0: "N", 90: "E", 180: "S", 270: "W" };

// ECharts callback params are loosely typed; `data` carries our SkyPoint shape.
/* eslint-disable @typescript-eslint/no-explicit-any */
const option = computed(() => ({
  tooltip: {
    formatter: (p: any) =>
      p.data.below
        ? `${p.data.name}<br/>${t("tonight.map.belowHorizon")}`
        : `${p.data.name}<br/>${t("tonight.cols.altNow")}: ${p.data.altNow}° · ${p.data.azNow}°`,
  },
  polar: { center: ["50%", "52%"], radius: "78%" },
  angleAxis: {
    type: "value",
    min: 0,
    max: 360,
    startAngle: 90, // 0° (North) at the top
    clockwise: true, // azimuth increases toward the East
    interval: 90,
    axisLabel: {
      color: CHART_AXIS,
      formatter: (v: number) => compass[v] ?? "",
    },
    axisLine: { lineStyle: { color: CHART_GRID } },
    splitLine: { lineStyle: { color: CHART_GRID } },
  },
  radiusAxis: {
    type: "value",
    min: 0,
    max: 90, // center = zenith (alt 90°), rim = horizon (alt 0°)
    interval: 30,
    axisLabel: { color: CHART_AXIS, formatter: (v: number) => `${90 - v}°` },
    axisLine: { show: false },
    splitLine: { lineStyle: { color: CHART_GRID } },
  },
  series: [
    {
      type: "scatter",
      coordinateSystem: "polar",
      symbolSize: 10,
      data: points.value,
      encode: { radius: 0, angle: 1 },
    },
    {
      // Selected-target ring + label, drawn on top of the base dot.
      type: "scatter",
      coordinateSystem: "polar",
      symbolSize: 20,
      z: 10,
      silent: true,
      data: selectedOverlay.value,
      itemStyle: {
        color: "transparent",
        borderColor: MAP_SELECTED,
        borderWidth: 3,
      },
      label: {
        show: true,
        formatter: (p: any) => p.data.name,
        position: "top",
        color: MAP_SELECTED,
        fontWeight: "bold",
      },
      encode: { radius: 0, angle: 1 },
    },
    {
      // Below-horizon rim marker for the selected target (keeps the selection visible when it is not up).
      type: "scatter",
      coordinateSystem: "polar",
      symbol: "pin",
      symbolSize: 22,
      z: 9,
      data: belowHorizonHint.value,
      itemStyle: { color: MAP_SELECTED, opacity: 0.7 },
      label: {
        show: true,
        formatter: (p: any) => `${p.data.name} ↓`,
        position: "bottom",
        color: MAP_SELECTED,
      },
      encode: { radius: 0, angle: 1 },
    },
  ],
}));

function onClick(p: any) {
  if (p?.data?.name) store.select(String(p.data.name));
}
/* eslint-enable @typescript-eslint/no-explicit-any */
</script>

<template>
  <div>
    <VChart :option="option" autoresize class="h-96 w-full" @click="onClick" />
    <!-- Night playbar: scrub target positions across the selected night. -->
    <div
      v-if="playbarReady"
      class="mt-1 flex items-center gap-2 px-2 text-xs text-slate-400"
    >
      <button
        type="button"
        class="flex h-7 w-7 items-center justify-center rounded-full bg-slate-700/60 text-white hover:bg-slate-600"
        :aria-label="
          playing
            ? t('tonight.map.playbar.pause')
            : t('tonight.map.playbar.play')
        "
        @click="togglePlay"
      >
        <IconPause v-if="playing" color="currentColor" />
        <IconPlay v-else color="currentColor" />
      </button>
      <input
        type="range"
        class="flex-1 accent-brand-500"
        :min="nightStartMs"
        :max="nightEndMs"
        :step="SCRUB_STEP_MS"
        :value="scrubMs ?? nightStartMs"
        :aria-label="t('tonight.map.playbar.scrub')"
        @input="onSlider"
      />
      <span class="w-14 text-center font-medium tabular-nums text-slate-300">
        {{ scrubLabel }}
      </span>
      <button
        v-if="scrubMs != null"
        type="button"
        class="rounded-full bg-slate-700/60 px-2 py-0.5 text-[11px] font-medium text-white hover:bg-slate-600"
        @click="resetLive"
      >
        {{ t("tonight.map.playbar.live") }}
      </button>
    </div>
  </div>
</template>
