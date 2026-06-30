<script setup lang="ts">
import { computed, onMounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useWeatherStore } from "@/stores/weather";
import { useSkyStore } from "@/stores/sky";
import { verdictColor, dewRiskColor } from "@/utils/weather";

const { t } = useI18n();
const wx = useWeatherStore();
const sky = useSkyStore();

// The badge is always visible on the control bar, so it seeds the shared weather fetch (deduped).
onMounted(() => wx.fetch());
watch(
  () => sky.query?.location,
  (loc, prev) => {
    if (loc && prev && (loc.lat !== prev.lat || loc.lon !== prev.lon))
      wx.fetch(true);
  },
);

const h = computed(() => wx.nowHour);
</script>

<template>
  <div
    v-if="h"
    class="flex items-center gap-2 rounded-md border border-slate-200 px-2 py-1 text-xs dark:border-slate-700"
    :title="t('tonight.weather.title')"
  >
    <span
      class="inline-block h-3 w-3 shrink-0 rounded-full"
      :style="{ backgroundColor: verdictColor(h.verdict) }"
    />
    <span class="text-slate-600 dark:text-slate-300"
      >{{ t("tonight.weather.metrics.clouds") }}
      {{ Math.round(h.cloud_pct) }}%</span
    >
    <span v-if="h.seeing_arcsec > 0" class="text-slate-500 dark:text-slate-400"
      >· {{ h.seeing_arcsec.toFixed(1) }}″</span
    >
    <span
      class="inline-block h-2.5 w-2.5 rounded-full"
      :style="{ backgroundColor: dewRiskColor(h.dew_risk) }"
      :title="t(`tonight.weather.dewRisk.${h.dew_risk}`)"
    />
  </div>
</template>
