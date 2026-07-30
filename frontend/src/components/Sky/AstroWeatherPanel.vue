<script setup lang="ts">
import { computed, onMounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useWeatherStore } from "@/stores/weather";
import { useSkyStore } from "@/stores/sky";
import { tzForLocation, fmtClock } from "@/utils/tz";
import {
  WEATHER_METRICS,
  verdictColor,
  verdictLabel,
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
import IconThermometer from "@/components/Icons/IconThermometer.vue";
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
  thermo: IconThermometer,
};

onMounted(() => wx.fetch());
watch(
  () => sky.query?.location,
  (loc, prev) => {
    if (loc && prev && (loc.lat !== prev.lat || loc.lon !== prev.lon))
      wx.fetch(true);
  },
);
// A custom planning time re-anchors the forecast night: refetch (the store's dedup key includes
// `at`, so a same-night refresh is a no-op while a night change really reloads).
watch(
  () => sky.query?.at_utc_ms,
  (at, prev) => {
    if (at && prev && at !== prev) wx.fetch();
  },
);

const tz = computed(() => {
  const l = sky.query?.location;
  return l
    ? tzForLocation(l.lat, l.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone;
});

// Show the SELECTED night's dark-window range (dusk−½h → dawn+½h, ±1 h slack). When the page plans a
// night the forecast horizon does not reach, show nothing rather than silently falling back to
// "now" hours that belong to a different night; the fallback only serves the no-dark-window case.
const hours = computed<WeatherHour[]>(() => {
  const all = wx.forecast?.hours ?? [];
  const d = sky.darkWindow;
  if (d?.night_start_ms && d?.night_end_ms) {
    return all.filter(
      (h) =>
        h.t_ms >= d.night_start_ms - 3.6e6 && h.t_ms <= d.night_end_ms + 3.6e6,
    );
  }
  const now = Date.now();
  return all.filter((h) => h.t_ms >= now - 3.6e6).slice(0, 18);
});

// The planned night exists and the forecast loaded, but its horizon stops short of that night —
// the honest "no forecast for this night yet" state (vs. a feed being down entirely).
const outOfHorizon = computed(
  () =>
    !hours.value.length &&
    !!sky.darkWindow?.night_start_ms &&
    (wx.forecast?.hours?.length ?? 0) > 0,
);

// Badge label: "Tonight" for the live night, "Night of <date>" when planning another night.
const nightBadgeLabel = computed(() => {
  const start = sky.darkWindow?.night_start_ms ?? 0;
  if (!start || Math.abs(start - Date.now()) < 12 * 3.6e6)
    return t("tonight.weather.tonightLabel");
  const date = new Intl.DateTimeFormat(undefined, {
    day: "2-digit",
    month: "2-digit",
    timeZone: tz.value,
  }).format(new Date(start));
  return t("tonight.weather.nightOf", { date });
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

// nightVerdict is the at-a-glance rating for tonight: the best clear window's score when there is one,
// else the mean of the shown hours (a fully-clouded night has no window but should still read "poor").
const nightVerdict = computed(() => {
  const b = wx.best;
  if (b) return Math.round(b.verdict);
  const hs = hours.value;
  if (!hs.length) return 0;
  return Math.round(hs.reduce((s, h) => s + h.verdict, 0) / hs.length);
});
const nightLabel = computed(() => verdictLabel(nightVerdict.value));
</script>

<template>
  <div :class="card">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex items-center gap-2">
        <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
          {{ t("tonight.weather.title") }}
        </h3>
        <span
          v-if="hours.length"
          class="rounded-full px-2 py-0.5 text-xs font-semibold text-white"
          :style="{ backgroundColor: verdictColor(nightVerdict) }"
          :title="t('tonight.weather.verdict')"
        >
          {{ nightBadgeLabel }} ·
          {{ t(`tonight.weather.verdictLabel.${nightLabel}`) }}
        </span>
      </div>
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
      {{
        outOfHorizon
          ? t("tonight.weather.noForecastNight")
          : t("tonight.weather.unavailable")
      }}
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
