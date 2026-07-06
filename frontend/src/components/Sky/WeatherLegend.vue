<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { gridLayerById } from "@/utils/weather";

const props = defineProps<{ layerId: string }>();
const { t } = useI18n();

const def = computed(() => gridLayerById(props.layerId));
// Bands are defined in paint order (bottom → top = high → low); the legend lists them low-first, the
// order an observer scans the sky. Layers without bands keep the single gradient bar.
const bandRows = computed(() =>
  def.value?.bands ? [...def.value.bands].reverse() : [],
);
function gradientCss(stops: string[]): string {
  return `linear-gradient(to right, ${stops.join(",")})`;
}
</script>

<template>
  <div
    v-if="def && bandRows.length"
    class="mt-1 space-y-0.5 text-[11px] text-slate-500 dark:text-slate-400"
  >
    <div
      v-for="band in bandRows"
      :key="band.metric"
      class="flex items-center gap-2"
    >
      <span class="w-24 font-medium">{{ t(band.labelKey) }}</span>
      <span>{{ t("tonight.weather.legend.less") }}</span>
      <span
        class="h-2 w-24 rounded-full border border-slate-300 dark:border-slate-600"
        :style="{ background: gradientCss(band.gradient) }"
      />
      <span>{{ t("tonight.weather.legend.more") }}</span>
    </div>
  </div>
  <div
    v-else-if="def"
    class="mt-1 flex items-center gap-2 text-[11px] text-slate-500 dark:text-slate-400"
  >
    <span class="font-medium">{{ t(def.labelKey) }}</span>
    <span>{{ t("tonight.weather.legend.less") }}</span>
    <span
      class="h-2 w-24 rounded-full border border-slate-300 dark:border-slate-600"
      :style="{ background: gradientCss(def.gradient) }"
    />
    <span>{{ t("tonight.weather.legend.more") }}</span>
  </div>
</template>
