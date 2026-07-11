<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useWeatherStore } from "@/stores/weather";
import { RADAR_GRADIENT } from "@/composables/useRainviewerLayer";

// Legend + live indicator for the RainViewer radar overlay. A pulsing dot + "LIVE" means the playhead
// sits inside the observed window (radarFrame != null); scrubbed into the forecast future there is no
// radar, so we say so rather than paint a stale frame.
const { t } = useI18n();
const wx = useWeatherStore();
const live = computed(() => !!wx.radarFrame);
const gradient = `linear-gradient(to right, ${RADAR_GRADIENT.join(",")})`;
</script>

<template>
  <div
    class="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-slate-500 dark:text-slate-400"
  >
    <span class="flex items-center gap-1 font-medium">
      <span
        class="inline-block h-2 w-2 rounded-full"
        :class="
          live ? 'bg-emerald-500 motion-safe:animate-pulse' : 'bg-slate-400'
        "
        aria-hidden="true"
      />
      {{ t("tonight.layers.radar") }}
      <span
        v-if="live"
        class="uppercase tracking-wide text-emerald-600 dark:text-emerald-400"
        >{{ t("tonight.layers.live") }}</span
      >
    </span>
    <template v-if="live">
      <span>{{ t("tonight.layers.radarLight") }}</span>
      <span
        class="h-2 w-20 rounded-full border border-slate-300 dark:border-slate-600"
        :style="{ background: gradient }"
      />
      <span>{{ t("tonight.layers.radarHeavy") }}</span>
    </template>
    <span v-else>{{ t("tonight.layers.liveNoData") }}</span>
    <span class="ml-auto opacity-70">RainViewer</span>
  </div>
</template>
