<script setup lang="ts">
import { useI18n } from "vue-i18n";
import type { ChannelDetection } from "@/types";
import { card } from "@/constants/styles";
import FilterChip from "@/components/Common/FilterChip.vue";

defineProps<{ detection: ChannelDetection }>();
const { t } = useI18n();
</script>

<template>
  <div :class="card">
    <h3 class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200">
      {{ t("mapping.title") }}
      <span class="ml-1 text-xs font-normal text-slate-400"
        >{{ Math.round(detection.overall_confidence * 100) }}%</span
      >
    </h3>
    <div class="flex flex-wrap gap-2">
      <FilterChip v-for="(r, i) in detection.runs" :key="i" :filter="r.filter">
        <span class="text-slate-500 dark:text-slate-400">×{{ r.count }}</span>
      </FilterChip>
    </div>
  </div>
</template>
