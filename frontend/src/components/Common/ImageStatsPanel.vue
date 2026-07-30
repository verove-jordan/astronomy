<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { AssistMeasurement } from "@/stores/agent";

// A compact, collapsible readout of the objective stats the backend measured for one image — the same
// ground truth the model was given. A green dot = that axis is clean; amber = worth attention.
const props = defineProps<{ m: AssistMeasurement }>();
const { t } = useI18n();

const maxBlack = computed(() => Math.max(...props.m.black_clip));
const maxWhite = computed(() => Math.max(...props.m.white_clip));

const castLabel = computed(() => {
  const v = props.m.green_cast;
  if (Math.abs(v) <= 0.02) return t("astroAgent.stats.neutral");
  const hue =
    v > 0 ? t("astroAgent.stats.green") : t("astroAgent.stats.magenta");
  return `${hue} (${v > 0 ? "+" : ""}${v.toFixed(3)})`;
});

interface Row {
  label: string;
  value: string;
  ok: boolean;
}
const rows = computed<Row[]>(() => [
  {
    label: t("astroAgent.stats.background"),
    value: props.m.background.toFixed(3),
    ok: props.m.background <= 0.2,
  },
  {
    label: t("astroAgent.stats.cast"),
    value: castLabel.value,
    ok: Math.abs(props.m.green_cast) <= 0.02,
  },
  {
    label: t("astroAgent.stats.clipping"),
    value: `${(maxBlack.value * 100).toFixed(1)}% / ${(maxWhite.value * 100).toFixed(1)}%`,
    ok: maxBlack.value <= 0.01 && maxWhite.value <= 0.02,
  },
  {
    label: t("astroAgent.stats.gradient"),
    value: `${props.m.gradient_pct.toFixed(1)}%`,
    ok: props.m.gradient_pct <= 10,
  },
  {
    label: t("astroAgent.stats.trail"),
    value: props.m.trail
      ? t("astroAgent.stats.detected")
      : t("astroAgent.stats.none"),
    ok: !props.m.trail,
  },
]);
</script>

<template>
  <details
    class="rounded-md border border-slate-700 bg-slate-900/50 px-2 py-1 text-xs text-slate-300"
  >
    <summary class="cursor-pointer select-none text-slate-400">
      {{ t("astroAgent.stats.title") }}
    </summary>
    <div class="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
      <template v-for="(r, i) in rows" :key="i">
        <span class="flex items-center gap-1.5">
          <span
            class="inline-block h-1.5 w-1.5 rounded-full"
            :class="r.ok ? 'bg-green-500' : 'bg-amber-500'"
          />
          {{ r.label }}
        </span>
        <span class="font-mono text-slate-200">{{ r.value }}</span>
      </template>
    </div>
  </details>
</template>
