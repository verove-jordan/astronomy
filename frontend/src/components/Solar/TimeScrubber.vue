<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";

import {
  MAX_RATE,
  MIN_RATE,
  RATES,
  type SimClock,
} from "@/composables/useSimClock";
import { RANGE_FROM, RANGE_TO } from "@/utils/solarsystem";
import {
  btn,
  btnPrimary,
  input as inputCls,
  segActive,
  segBtn,
  segIdle,
  segWrap,
} from "@/constants/styles";

// The time machine's controls: play, how fast, and exactly when.
//
// The rate slider is logarithmic and SIGNED, so running time backwards is the same control pushed
// the other way rather than a separate mode — which is what makes "watch the retrograde loop, then
// unwind it" one gesture.

const props = defineProps<{ clock: SimClock; timezone?: string }>();

const { t } = useI18n();

const MIN_MS = Date.UTC(RANGE_FROM, 0, 1);
const MAX_MS = Date.UTC(RANGE_TO, 11, 31, 23, 59, 59);

// The slider carries log10 of the rate's magnitude; its sign is kept separately so the control can
// pass through zero (paused) without the logarithm going anywhere near it.
const logRate = computed({
  get: () => Math.log10(Math.max(MIN_RATE, Math.abs(props.clock.rate.value))),
  set: (v: number) => {
    const magnitude = Math.min(MAX_RATE, Math.max(MIN_RATE, 10 ** v));
    props.clock.rate.value = reversed.value ? -magnitude : magnitude;
  },
});

const reversed = computed({
  get: () => props.clock.rate.value < 0,
  set: (v: boolean) => {
    props.clock.rate.value = Math.abs(props.clock.rate.value) * (v ? -1 : 1);
  },
});

/** The scrubber position, as a fraction of the model's validity span. */
const scrub = computed({
  get: () => (props.clock.timeMs.value - MIN_MS) / (MAX_MS - MIN_MS),
  set: (v: number) => props.clock.seek(MIN_MS + v * (MAX_MS - MIN_MS)),
});

/** The datetime-local field wants a local-looking string with no zone suffix. */
const isoLocal = computed({
  get: () => new Date(props.clock.timeMs.value).toISOString().slice(0, 16),
  set: (v: string) => {
    const ms = Date.parse(`${v}Z`);
    if (Number.isFinite(ms)) props.clock.seek(ms);
  },
});

const utcLabel = computed(() =>
  new Date(props.clock.timeMs.value)
    .toISOString()
    .replace("T", " ")
    .slice(0, 19),
);

const siteLabel = computed(() => {
  if (!props.timezone) return "";
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: props.timezone,
      dateStyle: "medium",
      timeStyle: "short",
    }).format(new Date(props.clock.timeMs.value));
  } catch {
    return "";
  }
});

const julian = computed(() => props.clock.timeMs.value / 86400000 + 2440587.5);

/** The nearest named speed, so the readout says "a day per second" rather than 86400000×. */
const rateLabel = computed(() => {
  const magnitude = Math.abs(props.clock.rate.value);
  let best: { key: string; rate: number } = RATES[0];
  for (const r of RATES) {
    if (
      Math.abs(Math.log10(r.rate) - Math.log10(magnitude)) <
      Math.abs(Math.log10(best.rate) - Math.log10(magnitude))
    ) {
      best = r;
    }
  }
  const exact = Math.abs(magnitude / best.rate - 1) < 0.02;
  const name = t(`solarsystem.time.rates.${best.key}`);
  return exact ? name : `${(magnitude / best.rate).toFixed(1)} × ${name}`;
});

const DAY = 86_400_000;
</script>

<template>
  <div class="space-y-3" data-demo="solar-time">
    <div class="flex flex-wrap items-center gap-2">
      <button
        type="button"
        :class="btnPrimary"
        :aria-label="
          clock.playing.value
            ? t('solarsystem.time.pause')
            : t('solarsystem.time.play')
        "
        @click="clock.toggle()"
      >
        <span aria-hidden="true">{{ clock.playing.value ? "❚❚" : "▶" }}</span>
        {{
          clock.playing.value
            ? t("solarsystem.time.pause")
            : t("solarsystem.time.play")
        }}
      </button>

      <button
        type="button"
        :class="btn"
        class="bg-slate-700 text-slate-100 hover:bg-slate-600"
        :disabled="clock.live.value"
        @click="clock.now()"
      >
        {{ t("solarsystem.time.now") }}
      </button>

      <div :class="segWrap">
        <button
          type="button"
          :class="[segBtn, !reversed ? segActive : segIdle]"
          @click="reversed = false"
        >
          {{ t("solarsystem.time.forward") }}
        </button>
        <button
          type="button"
          :class="[segBtn, reversed ? segActive : segIdle]"
          @click="reversed = true"
        >
          {{ t("solarsystem.time.backward") }}
        </button>
      </div>

      <button
        type="button"
        :class="btn"
        class="bg-slate-700 text-slate-100 hover:bg-slate-600"
        :title="t('solarsystem.time.stepBack')"
        @click="clock.step(-DAY)"
      >
        −1 d
      </button>
      <button
        type="button"
        :class="btn"
        class="bg-slate-700 text-slate-100 hover:bg-slate-600"
        :title="t('solarsystem.time.stepForward')"
        @click="clock.step(DAY)"
      >
        +1 d
      </button>

      <span
        v-if="clock.live.value"
        class="rounded-full bg-success-600/20 px-2 py-0.5 text-xs text-success-300"
      >
        {{ t("solarsystem.time.liveNow") }}
      </span>
    </div>

    <label class="block">
      <span
        class="mb-1 flex items-baseline justify-between text-xs text-slate-400"
      >
        <span>{{ t("solarsystem.time.rate") }}</span>
        <span class="tabular-nums text-slate-300"
          >{{ rateLabel }} {{ t("solarsystem.time.perSecond") }}</span
        >
      </span>
      <input
        v-model.number="logRate"
        type="range"
        min="0"
        :max="Math.log10(MAX_RATE)"
        step="0.01"
        class="w-full accent-brand-500"
        :aria-label="t('solarsystem.time.rate')"
      />
    </label>

    <label class="block">
      <span
        class="mb-1 flex items-baseline justify-between text-xs text-slate-400"
      >
        <span>{{ t("solarsystem.time.when") }}</span>
        <span class="tabular-nums text-slate-300"
          >{{ RANGE_FROM }} — {{ RANGE_TO }}</span
        >
      </span>
      <input
        v-model.number="scrub"
        type="range"
        min="0"
        max="1"
        step="0.0000001"
        class="w-full accent-brand-500"
        :aria-label="t('solarsystem.time.when')"
      />
    </label>

    <div
      class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-slate-400"
    >
      <input
        v-model="isoLocal"
        type="datetime-local"
        :class="inputCls"
        class="w-auto py-1 text-xs"
        :aria-label="t('solarsystem.time.exact')"
      />
      <span class="tabular-nums"
        >{{ utcLabel }} {{ t("solarsystem.time.utc") }}</span
      >
      <span v-if="siteLabel" class="tabular-nums"
        >{{ siteLabel }} · {{ t("solarsystem.time.site") }}</span
      >
      <span class="tabular-nums"
        >{{ t("solarsystem.time.jd") }} {{ julian.toFixed(4) }}</span
      >
    </div>
  </div>
</template>
