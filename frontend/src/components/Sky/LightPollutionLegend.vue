<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { BORTLE_COLORS, bortleRampColor } from "@/utils/bortle";

// The Bortle 1–9 colour ramp under the light-pollution overlay. When a Bortle class is supplied it is
// marked ON the ramp: the legend stops being a static key and becomes a reading of the selected site,
// which is the question anyone actually has in front of this map ("how dark is *there*?").
//
// `bortle` may be fractional (the API's bortle_f). The ramp is continuous, so a 4.2 sits visibly nearer
// its class boundary than a 4.8 — which is the whole point of carrying the decimal: nine buckets cannot
// separate a village edge from the field beyond it, but the underlying model can.
const props = defineProps<{ bortle?: number | null }>();
const { t } = useI18n();
const gradient = `linear-gradient(to right, ${BORTLE_COLORS.join(", ")})`;

// Valid only for a finite class in 1..9 — the API returns 0 for "unknown", which must not plant the
// marker at the dark end and quietly claim a pristine sky.
const marked = computed(() => {
  const b = props.bortle;
  return typeof b === "number" && Number.isFinite(b) && b >= 1 && b <= 9
    ? b
    : null;
});
// Position along the ramp: class 1 at the left edge, class 9 at the right. Fractional readings land
// between the class stops rather than snapping to them.
const markerPct = computed(() =>
  marked.value === null ? 0 : ((marked.value - 1) / 8) * 100,
);
// One decimal: the model resolves well inside a class, and the extra digit is what makes two nearby
// spots comparable at all.
const markedLabel = computed(() =>
  marked.value === null ? "" : marked.value.toFixed(1),
);
</script>

<template>
  <div class="mt-2 text-[10px] text-slate-500 dark:text-slate-400">
    <div class="mb-0.5 flex items-center justify-between">
      <span>{{ t("tonight.layers.legendDark") }}</span>
      <span>{{ t("tonight.layers.legendBright") }}</span>
    </div>
    <div class="relative">
      <div
        class="h-2 w-full rounded border border-slate-200 dark:border-slate-700"
        :style="{ background: gradient }"
      />
      <!-- The site marker: a caret straddling the ramp at the measured class. -->
      <div
        v-if="marked !== null"
        class="pointer-events-none absolute -top-0.5 h-3 w-1 -translate-x-1/2 rounded-sm border border-white shadow ring-1 ring-black/40"
        :style="{
          left: `${markerPct}%`,
          backgroundColor: bortleRampColor(marked),
        }"
        data-testid="bortle-marker"
      />
    </div>
    <div class="mt-0.5 flex items-center justify-between">
      <span>{{ t("tonight.layers.bortle") }} 1</span>
      <span
        v-if="marked !== null"
        class="font-semibold text-slate-700 dark:text-slate-200"
        data-testid="bortle-value"
      >
        {{ t("tonight.layers.bortle") }} {{ markedLabel }}
      </span>
      <span>9</span>
    </div>
  </div>
</template>
