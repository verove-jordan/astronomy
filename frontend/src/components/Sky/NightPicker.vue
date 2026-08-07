<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { segWrap, segBtn, segActive, segIdle } from "@/constants/styles";
import type { SkyNight } from "@/types";

// Pick which night to plan for. Each button carries the two things that decide whether a night is worth
// driving to before any forecast is consulted: how many hours of real darkness it has, and how much of
// that the Moon spoils. Nights past the forecast's useful range are marked rather than hidden — the
// user can still look, they just should not treat it as a plan.
const props = defineProps<{
  modelValue: number;
  nights: SkyNight[];
  count: number;
}>();
const emit = defineEmits<{ "update:modelValue": [value: number] }>();
const { t, locale } = useI18n();

// Fall back to bare offsets when the night list has not loaded: the picker must still work.
const entries = computed<{ index: number }[]>(() =>
  props.nights.length
    ? props.nights
    : Array.from({ length: props.count }, (_, i) => ({ index: i })),
);

function label(index: number): string {
  if (index === 0) return t("darksky.night.tonight");
  if (index === 1) return t("darksky.night.tomorrow");
  const night = props.nights[index];
  if (!night) return t("darksky.night.inDays", { n: index });
  // Parse as local midday so a date-only string cannot slip a day in either direction.
  const date = new Date(`${night.date_local}T12:00:00`);
  return date.toLocaleDateString(locale.value, {
    weekday: "short",
    day: "numeric",
  });
}

// A compact glyph for the Moon: full nights are the ones a faint-object plan has to work around.
const MOON_GLYPH: Record<string, string> = {
  new: "●",
  waxing_crescent: "◐",
  first_quarter: "◐",
  waxing_gibbous: "◑",
  full: "○",
  waning_gibbous: "◑",
  last_quarter: "◐",
  waning_crescent: "◐",
};

function title(night: SkyNight | undefined): string {
  if (!night) return "";
  return [
    t("darksky.night.darkWindow", {
      from: night.start_local,
      to: night.end_local,
      hours: night.dark_hours.toFixed(1),
    }),
    t("darksky.night.moon", {
      phase: t(`tonight.moonPhase.${night.moon_phase}`),
      pct: Math.round(night.moon_illum * 100),
      hours: night.moon_up_hours.toFixed(1),
    }),
    night.low_confidence ? t("darksky.night.lowConfidenceHint") : "",
  ]
    .filter(Boolean)
    .join("\n");
}
</script>

<template>
  <div class="flex flex-wrap items-center gap-2">
    <span class="text-sm text-slate-500 dark:text-slate-400">
      {{ t("darksky.night.label") }}
    </span>
    <div :class="segWrap" role="group" :aria-label="t('darksky.night.label')">
      <button
        v-for="entry in entries"
        :key="entry.index"
        type="button"
        :class="[
          segBtn,
          entry.index === modelValue ? segActive : segIdle,
          'inline-flex items-center gap-1',
        ]"
        :aria-pressed="entry.index === modelValue"
        :title="title(nights[entry.index])"
        @click="emit('update:modelValue', entry.index)"
      >
        <span>{{ label(entry.index) }}</span>
        <span
          v-if="nights[entry.index]"
          class="opacity-70"
          :aria-hidden="true"
          >{{ MOON_GLYPH[nights[entry.index].moon_phase] ?? "" }}</span
        >
        <span
          v-if="nights[entry.index]?.low_confidence"
          class="text-warning"
          :title="t('darksky.night.lowConfidenceHint')"
          >~</span
        >
      </button>
    </div>
  </div>
</template>
