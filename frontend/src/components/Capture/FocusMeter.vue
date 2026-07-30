<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, input } from "@/constants/styles";
import { apiPost } from "@/services/api";
import { useCaptureStore } from "@/stores/capture";

// The focus meter: one number from 0 to 100, how far the focuser is out, and — honestly — whether
// the last thing you did helped.
//
// It does NOT claim to know which way to turn from a single frame, because for a refractor that is
// not knowable: inside and outside focus look identical. So the instruction is "turn it a little,
// either way", and the meter reports whether that was an improvement. A sparkline of recent
// readings makes the trend obvious at a glance in the dark.
const { t } = useI18n();
const store = useCaptureStore();

const umPerTurn = ref(500);
const history = ref<number[]>([]);

const focus = computed(() => store.liveStats?.focus ?? null);

// Keep a short trail of readings for the sparkline: enough to see a trend, short enough to react.
watch(
  () => focus.value?.hfd_px,
  (hfd) => {
    if (!hfd || !focus.value?.reliable) return;
    history.value.push(hfd);
    if (history.value.length > 40) history.value.shift();
  },
);

const scoreColor = computed(() => {
  const s = focus.value?.score ?? 0;
  if (!focus.value?.reliable) return "text-slate-400";
  if (s >= 85) return "text-success";
  if (s >= 60) return "text-amber-500";
  return "text-danger-500";
});

// The distance is reported in turns when the focuser has been calibrated, because "a third of a
// turn" is actionable at the eyepiece in a way that "170 µm" is not.
const distanceLabel = computed(() => {
  const f = focus.value;
  if (!f?.distance_um || f.distance_um < 5) return "";
  if (umPerTurn.value > 0) {
    const turns = f.distance_um / umPerTurn.value;
    return t("capture.focus.turns", {
      turns: turns.toFixed(2),
      um: Math.round(f.distance_um),
    });
  }
  return t("capture.focus.microns", { um: Math.round(f.distance_um) });
});

const sparkPath = computed(() => {
  const h = history.value;
  if (h.length < 2) return "";
  const max = Math.max(...h);
  const min = Math.min(...h);
  const span = Math.max(1e-6, max - min);
  return h
    .map((v, i) => {
      const x = (i / (h.length - 1)) * 100;
      const y = 24 - ((v - min) / span) * 22;
      return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
});

// Tilt: the four corner HFDs. Equal means the sensor is square to the optical axis; a spread means
// tilt, which focusing cannot fix and which is invisible from a centre reading alone.
const tiltSpread = computed(() => {
  const c = focus.value?.tilt_corners;
  if (!c || c.length < 4) return null;
  const max = Math.max(...c);
  const min = Math.min(...c);
  return { min, max, pct: min > 0 ? ((max - min) / min) * 100 : 0 };
});

async function reset() {
  history.value = [];
  await apiPost("/api/device/live/focus/reset", {});
}
</script>

<template>
  <div class="space-y-2">
    <p v-if="!store.liveRunning" class="text-sm text-slate-400">
      {{ t("capture.focus.needsLive") }}
    </p>

    <template v-else-if="focus">
      <div class="flex items-baseline gap-3">
        <span class="text-3xl font-semibold tabular-nums" :class="scoreColor">{{
          focus.reliable ? Math.round(focus.score) : "–"
        }}</span>
        <div class="text-xs text-slate-500 dark:text-slate-400">
          <div v-if="focus.hfd_px">
            {{ t("capture.focus.hfd") }}
            <span class="font-mono">{{ focus.hfd_px.toFixed(2) }} px</span>
            <span v-if="focus.hfd_arcsec">
              · {{ focus.hfd_arcsec.toFixed(2) }}″</span
            >
          </div>
          <div>{{ t("capture.focus.stars", { n: focus.stars }) }}</div>
        </div>
      </div>

      <p
        v-if="!focus.reliable"
        class="text-xs text-amber-600 dark:text-amber-400"
      >
        {{
          focus.saturated
            ? t("capture.focus.saturated")
            : t("capture.focus.tooFewStars")
        }}
      </p>

      <template v-else>
        <p class="text-sm text-slate-600 dark:text-slate-300">
          {{ t(`capture.focus.advice_${focus.advice}`) }}
        </p>
        <p
          v-if="distanceLabel"
          class="text-xs text-slate-500 dark:text-slate-400"
        >
          {{ distanceLabel }}
        </p>
      </template>

      <svg
        v-if="sparkPath"
        viewBox="0 0 100 26"
        class="h-8 w-full"
        preserveAspectRatio="none"
      >
        <path
          :d="sparkPath"
          fill="none"
          stroke="currentColor"
          stroke-width="1.2"
          class="text-brand-500"
        />
      </svg>

      <p
        v-if="tiltSpread && tiltSpread.pct > 25"
        class="text-xs text-amber-600 dark:text-amber-400"
      >
        {{ t("capture.focus.tilt", { pct: Math.round(tiltSpread.pct) }) }}
      </p>

      <div class="flex flex-wrap items-center gap-2 text-xs">
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          {{ t("capture.focus.umPerTurn") }}
          <input
            v-model.number="umPerTurn"
            type="number"
            min="0"
            step="10"
            :class="input"
            class="w-20"
          />
        </label>
        <button :class="btnGhost" class="!px-2 !py-1" @click="reset">
          {{ t("capture.focus.reset") }}
        </button>
      </div>
      <p class="text-[11px] text-slate-400">{{ t("capture.focus.hint") }}</p>
    </template>

    <p v-else class="text-sm text-slate-400">
      {{ t("capture.focus.waiting") }}
    </p>
  </div>
</template>
