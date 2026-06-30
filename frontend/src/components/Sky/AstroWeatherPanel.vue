<script setup lang="ts">
import { computed, onMounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useWeatherStore } from "@/stores/weather";
import { useSkyStore } from "@/stores/sky";
import { tzForLocation, fmtClock } from "@/utils/tz";
import {
  WEATHER_METRICS,
  verdictColor,
  auroraColor,
  type WeatherMetric,
} from "@/utils/weather";
import { card } from "@/constants/styles";
import Spinner from "@/components/Common/Spinner.vue";
import IconCloud from "@/components/Icons/IconCloud.vue";
import IconEye from "@/components/Icons/IconEye.vue";
import IconTransparency from "@/components/Icons/IconTransparency.vue";
import IconDroplet from "@/components/Icons/IconDroplet.vue";
import IconWind from "@/components/Icons/IconWind.vue";
import IconHaze from "@/components/Icons/IconHaze.vue";
import type { WeatherHour } from "@/types";

const { t } = useI18n();
const wx = useWeatherStore();
const sky = useSkyStore();

const ICONS: Record<string, unknown> = {
  cloud: IconCloud,
  seeing: IconEye,
  transparency: IconTransparency,
  droplet: IconDroplet,
  wind: IconWind,
  haze: IconHaze,
};

onMounted(() => wx.fetch());
watch(
  () => sky.query?.location,
  (loc, prev) => {
    if (loc && prev && (loc.lat !== prev.lat || loc.lon !== prev.lon))
      wx.fetch(true);
  },
);

const tz = computed(() => {
  const l = sky.query?.location;
  return l
    ? tzForLocation(l.lat, l.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone;
});

// Show the dark-window chart range (dusk−½h → dawn+½h, ±1 h slack); fall back to the next 18 h.
const hours = computed<WeatherHour[]>(() => {
  const all = wx.forecast?.hours ?? [];
  const d = sky.darkWindow;
  if (d?.night_start_ms && d?.night_end_ms) {
    const win = all.filter(
      (h) =>
        h.t_ms >= d.night_start_ms - 3.6e6 && h.t_ms <= d.night_end_ms + 3.6e6,
    );
    if (win.length) return win;
  }
  const now = Date.now();
  return all.filter((h) => h.t_ms >= now - 3.6e6).slice(0, 18);
});

const metrics = computed(() =>
  WEATHER_METRICS.filter((m) => hours.value.some((h) => m.has(h))),
);

function isBest(h: WeatherHour): boolean {
  const b = wx.best;
  return !!b && h.t_ms >= b.start_ms && h.t_ms <= b.end_ms;
}
function cell(m: WeatherMetric, h: WeatherHour) {
  return m.has(h)
    ? { text: m.text(h), style: { backgroundColor: m.color(h) } }
    : { text: "·", style: { backgroundColor: "transparent" } };
}

const verdictPct = computed(() => Math.round(wx.best?.verdict ?? 0));
const bestLabel = computed(() => {
  const b = wx.best;
  if (!b) return t("tonight.weather.noWindow");
  return `${fmtClock(b.start_ms, tz.value)}–${fmtClock(b.end_ms, tz.value)}`;
});
</script>

<template>
  <div :class="card">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("tonight.weather.title") }}
      </h3>
      <div v-if="wx.best" class="flex items-center gap-2 text-xs">
        <span class="text-slate-400">{{ t("tonight.weather.best") }}</span>
        <span class="font-medium text-slate-700 dark:text-slate-200">{{
          bestLabel
        }}</span>
        <span
          class="rounded-full px-2 py-0.5 font-semibold text-white"
          :style="{ backgroundColor: verdictColor(verdictPct) }"
          >{{ verdictPct }}</span
        >
      </div>
    </div>
    <p class="mt-0.5 text-xs text-slate-400">
      {{ t("tonight.weather.subtitle") }}
    </p>

    <div v-if="wx.loading && !hours.length" class="flex justify-center py-8">
      <Spinner />
    </div>
    <p
      v-else-if="!hours.length"
      class="py-6 text-center text-sm text-slate-400"
    >
      {{ t("tonight.weather.unavailable") }}
    </p>

    <div v-else class="mt-3 overflow-x-auto">
      <table class="border-separate border-spacing-0.5 text-xs tabular-nums">
        <thead>
          <tr>
            <th
              class="sticky left-0 z-10 bg-white px-2 dark:bg-surface-raised"
            />
            <th
              v-for="h in hours"
              :key="h.t_ms"
              class="px-1 text-center font-medium"
              :class="
                isBest(h)
                  ? 'text-brand-600 dark:text-brand-300'
                  : 'text-slate-400'
              "
            >
              {{ fmtClock(h.t_ms, tz) }}
            </th>
          </tr>
        </thead>
        <tbody>
          <!-- per-hour observability verdict -->
          <tr>
            <td
              class="sticky left-0 z-10 bg-white py-0.5 pr-2 text-slate-500 dark:bg-surface-raised dark:text-slate-300"
            >
              {{ t("tonight.weather.verdict") }}
            </td>
            <td
              v-for="h in hours"
              :key="h.t_ms"
              class="h-5 rounded text-center font-semibold text-white"
              :class="isBest(h) ? 'ring-2 ring-brand-400' : ''"
              :style="{ backgroundColor: verdictColor(h.verdict) }"
            >
              {{ Math.round(h.verdict) }}
            </td>
          </tr>
          <!-- one row per metric -->
          <tr v-for="m in metrics" :key="m.key">
            <td
              class="sticky left-0 z-10 bg-white py-0.5 pr-2 text-slate-500 dark:bg-surface-raised dark:text-slate-300"
            >
              <span class="flex items-center gap-1.5">
                <component :is="ICONS[m.icon]" class="text-slate-400" />
                {{ t(m.labelKey) }}
              </span>
            </td>
            <td
              v-for="h in hours"
              :key="h.t_ms"
              class="h-5 rounded text-center text-white"
              :style="cell(m, h).style"
            >
              {{ cell(m, h).text }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- aurora + attribution -->
    <div
      class="mt-2 flex flex-wrap items-center justify-between gap-2 text-[11px] text-slate-400"
    >
      <span v-if="wx.kp" class="flex items-center gap-1.5">
        <span
          class="inline-block h-2.5 w-2.5 rounded-full"
          :style="{ backgroundColor: auroraColor(wx.kp.aurora) }"
        />
        {{ t("tonight.weather.aurora.label") }} · Kp
        {{ wx.kp.now.toFixed(1) }} ·
        {{ t(`tonight.weather.aurora.${wx.kp.aurora}`) }}
      </span>
      <span v-if="wx.sources.length"
        >{{ t("tonight.weather.sources") }}: {{ wx.sources.join(" · ") }}</span
      >
    </div>
    <p
      v-if="wx.warning"
      class="mt-1 text-[11px] text-amber-600 dark:text-amber-400"
    >
      {{ wx.warning }}
    </p>
  </div>
</template>
