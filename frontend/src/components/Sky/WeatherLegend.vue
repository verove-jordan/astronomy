<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { gridLayerById } from "@/utils/weather";

const props = defineProps<{ layerId: string }>();
const { t } = useI18n();

const def = computed(() => gridLayerById(props.layerId));
const gradientCss = computed(
  () => `linear-gradient(to right, ${(def.value?.gradient ?? []).join(",")})`,
);
</script>

<template>
  <div
    v-if="def"
    class="mt-1 flex items-center gap-2 text-[11px] text-slate-500 dark:text-slate-400"
  >
    <span class="font-medium">{{ t(def.labelKey) }}</span>
    <span>{{ t("tonight.weather.legend.less") }}</span>
    <span
      class="h-2 w-24 rounded-full border border-slate-300 dark:border-slate-600"
      :style="{ background: gradientCss }"
    />
    <span>{{ t("tonight.weather.legend.more") }}</span>
  </div>
</template>
