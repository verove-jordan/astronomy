<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
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

// --- night stepper -------------------------------------------------------------------------------
// Which night is on screen, in whole nights from the one happening now. The offset is held here
// rather than derived back out of `at`, because "which night does this instant belong to" is a
// question only the engine can answer (it depends on the site's dusk and dawn) and asking it of a
// timestamp we ourselves just wrote would be a second, disagreeing implementation.
//
// Stepping moves the planning instant by whole 24 h from NOW, and never builds a wall-clock time in
// the site's timezone: an instant 24 h from now is inside the next night whatever the hour, whatever
// the longitude, and across a DST change — the engine then resolves it to that night's real dusk and
// dawn. Composing "23:00 local on day N" here instead would put a timezone calculation in the
// browser that the server already does properly.
const nightOffset = ref(0);
// Open-Meteo's usable horizon. Past it the panel already has an honest "no forecast for this night
// yet" state, so this only stops the arrow inviting a click that cannot answer.
const MAX_NIGHTS_AHEAD = 6;

// Backwards stops at the night in progress: the forecast feeds carry no history, so an earlier night
// can only ever render empty. The button is disabled rather than hidden, so it is clear the control
// has an end rather than having silently lost half of itself.
const canGoBack = computed(() => nightOffset.value > 0);
const canGoForward = computed(() => nightOffset.value < MAX_NIGHTS_AHEAD);

function stepNight(delta: number): void {
  const next = Math.min(
    MAX_NIGHTS_AHEAD,
    Math.max(0, nightOffset.value + delta),
  );
  if (next === nightOffset.value) return;
  nightOffset.value = next;
  // `at` omitted entirely for the live night, so the page goes back to tracking real time instead of
  // pinning itself to the instant the user happened to press the button.
  void sky.fetch({
    at:
      next === 0
        ? undefined
        : new Date(Date.now() + next * 864e5).toISOString(),
  });
}

// Badge label: "Tonight" for the night in progress, "Night of <date>" for any other.
//
// The offset decides it, not a distance in hours. The old test — night_start within 12 h of now —
// disagreed with the stepper for part of every day: at 06:00 tonight's dusk is still 14 h away, so
// the badge called the CURRENT night "night of the 3rd" while the arrows read it as offset 0. The
// stepper is the thing the user is operating, so it is the thing that gets to say.
const nightBadgeLabel = computed(() => {
  const start = sky.darkWindow?.night_start_ms ?? 0;
  if (!start || nightOffset.value === 0)
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
// The window runs to the END of its last hour. BestWindow reports end_ms as that hour's own
// timestamp — right for the highlight test in isBest, which asks "is this hour in the window" —
// but printing it verbatim states a span one hour shorter than the one found, and a single-hour
// window comes out as "23:00–23:00", which reads as no time at all. The hours are hourly samples,
// so the window closes an hour after the last of them starts.
const bestLabel = computed(() => {
  const b = wx.best;
  if (!b) return t("tonight.weather.noWindow");
  return `${fmtClock(b.start_ms, tz.value)}–${fmtClock(b.end_ms + 3.6e6, tz.value)}`;
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
  <div data-demo="tonight-weather" :class="card">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex items-center gap-2">
        <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
          {{ t("tonight.weather.title") }}
        </h3>
        <!-- Night stepper: the badge is the readout, the arrows either side of it move the whole
             page (targets, chart and forecast) to the neighbouring night. -->
        <div class="flex items-center gap-1" data-demo="tonight-weather-night">
          <button
            type="button"
            class="grid h-6 w-6 place-items-center rounded-full text-slate-400 transition hover:bg-slate-200/70 hover:text-slate-700 disabled:cursor-not-allowed disabled:opacity-30 disabled:hover:bg-transparent dark:hover:bg-brand-900/40 dark:hover:text-slate-100"
            :disabled="!canGoBack || wx.loading"
            :title="
              canGoBack
                ? t('tonight.weather.prevNight')
                : t('tonight.weather.noPastForecast')
            "
            :aria-label="t('tonight.weather.prevNight')"
            @click="stepNight(-1)"
          >
            <span aria-hidden="true">‹</span>
          </button>
          <span
            v-if="hours.length"
            class="rounded-full px-2 py-0.5 text-xs font-semibold text-white"
            :style="{ backgroundColor: verdictColor(nightVerdict) }"
            :title="t('tonight.weather.verdict')"
          >
            {{ nightBadgeLabel }} ·
            {{ t(`tonight.weather.verdictLabel.${nightLabel}`) }}
          </span>
          <button
            type="button"
            class="grid h-6 w-6 place-items-center rounded-full text-slate-400 transition hover:bg-slate-200/70 hover:text-slate-700 disabled:cursor-not-allowed disabled:opacity-30 disabled:hover:bg-transparent dark:hover:bg-brand-900/40 dark:hover:text-slate-100"
            :disabled="!canGoForward || wx.loading"
            :title="
              canGoForward
                ? t('tonight.weather.nextNight')
                : t('tonight.weather.beyondForecast')
            "
            :aria-label="t('tonight.weather.nextNight')"
            @click="stepNight(1)"
          >
            <span aria-hidden="true">›</span>
          </button>
          <button
            v-if="nightOffset > 0"
            type="button"
            class="rounded-full px-2 py-0.5 text-xs text-slate-500 underline-offset-2 transition hover:text-slate-800 hover:underline dark:text-slate-400 dark:hover:text-slate-100"
            @click="stepNight(-nightOffset)"
          >
            {{ t("tonight.weather.backToTonight") }}
          </button>
        </div>
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
