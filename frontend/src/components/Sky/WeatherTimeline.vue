<script setup lang="ts">
import { computed, onBeforeUnmount } from "vue";
import { useI18n } from "vue-i18n";
import { useWeatherStore } from "@/stores/weather";
import { useSkyStore } from "@/stores/sky";
import { tzForLocation, fmtClock } from "@/utils/tz";
import IconPlay from "@/components/Icons/IconPlay.vue";
import IconPause from "@/components/Icons/IconPause.vue";

const { t } = useI18n();
const wx = useWeatherStore();
const sky = useSkyStore();

const tz = computed(() => {
  const l = sky.query?.location;
  return l
    ? tzForLocation(l.lat, l.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone;
});

const count = computed(() => wx.frames.length);

// The scrubber index ↔ the store playhead (dragging pauses playback; play advances the thumb). `frames`
// is the union timeline of forecast-grid steps + live radar frames, so one scrubber drives both overlays.
const idx = computed({
  get: () => wx.cursor,
  set: (v: number) => wx.setPlayhead(wx.frames[v] ?? wx.playhead),
});

function frac(ms: number): number {
  const span = wx.rangeEnd - wx.rangeStart;
  if (span <= 0) return 0;
  return Math.min(1, Math.max(0, (ms - wx.rangeStart) / span));
}

const band = computed(() => {
  const d = sky.darkWindow;
  if (!d || !d.dusk_utc_ms || !d.dawn_utc_ms) return null;
  const a = frac(d.dusk_utc_ms);
  const b = frac(d.dawn_utc_ms);
  return { left: `${a * 100}%`, width: `${Math.max(0, b - a) * 100}%` };
});
const nowLeft = computed(() => `${frac(Date.now()) * 100}%`);
const playheadLabel = computed(() =>
  wx.playhead ? fmtClock(wx.playhead, tz.value) : "",
);

// Stop playback if the scrubber is torn down mid-animation (e.g. all animated layers toggled off), so the
// store's frame interval never keeps ticking without a visible control. The map owner also pauses on leave.
onBeforeUnmount(() => wx.pause());
</script>

<template>
  <div v-if="count > 1" class="mt-2 space-y-1.5">
    <div class="flex items-center gap-2">
      <button
        class="shrink-0 rounded-md bg-brand-600 p-1.5 text-white transition-colors hover:bg-brand-500"
        :aria-label="t('tonight.weather.timeline.play')"
        @click="wx.togglePlay()"
      >
        <IconPause v-if="wx.playing" />
        <IconPlay v-else />
      </button>
      <!-- context bar: the dark-window band + a 'now' marker behind the scrubber -->
      <div
        class="relative h-2 grow rounded-full bg-slate-200 dark:bg-slate-700"
      >
        <div
          v-if="band"
          class="absolute inset-y-0 rounded-full bg-brand-500/30"
          :style="band"
          :title="t('tonight.weather.timeline.darkWindow')"
        />
        <div
          class="absolute -top-1 h-4 w-0.5 bg-rose-500"
          :style="{ left: nowLeft }"
          :title="t('tonight.weather.timeline.now')"
        />
      </div>
      <span
        class="w-12 shrink-0 text-right text-xs tabular-nums text-slate-500 dark:text-slate-400"
        >{{ playheadLabel }}</span
      >
    </div>
    <input
      v-model.number="idx"
      type="range"
      min="0"
      :max="count - 1"
      class="w-full accent-brand-500"
      :aria-label="t('tonight.weather.timeline.scrub')"
    />
  </div>
</template>
