<script setup lang="ts">
import { useI18n } from "vue-i18n";
import type { CaptureSummary } from "@/composables/useCaptureSummary";
import { card } from "@/constants/styles";
import { humanizeMs } from "@/utils/format";
import FilterChip from "@/components/Common/FilterChip.vue";

defineProps<{ summary: CaptureSummary; path?: string; title?: string }>();
const { t } = useI18n();
</script>

<template>
  <div :class="card">
    <div class="mb-2 flex flex-wrap items-baseline justify-between gap-2">
      <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ title || t("capture.summary") }}
      </h3>
      <span
        v-if="summary.objects.length"
        class="text-sm text-slate-500 dark:text-slate-400"
      >
        {{ t("capture.target") }}:
        <span class="font-medium text-slate-700 dark:text-slate-200">{{
          summary.objects.join(", ")
        }}</span>
      </span>
    </div>
    <p v-if="path" class="mb-2 truncate text-xs text-slate-400" :title="path">
      {{ path }}
    </p>
    <template v-if="summary.hasData">
      <div class="flex flex-wrap gap-4 text-sm">
        <div>
          <span class="font-semibold text-brand-600 dark:text-brand-300">{{
            summary.lightCount
          }}</span>
          <span class="ml-1 text-slate-500 dark:text-slate-400">{{
            t("capture.lights")
          }}</span>
        </div>
        <div>
          <span class="font-semibold text-brand-600 dark:text-brand-300">{{
            humanizeMs(summary.totalIntegrationMs)
          }}</span>
          <span class="ml-1 text-slate-500 dark:text-slate-400">{{
            t("capture.integration")
          }}</span>
        </div>
      </div>
      <div
        v-if="summary.filters.length"
        class="mt-2 flex flex-wrap items-center gap-1.5"
      >
        <span class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("capture.filters") }}:</span
        >
        <FilterChip
          v-for="f in summary.filters"
          :key="f.filter"
          :filter="f.filter"
        >
          <span class="text-slate-500 dark:text-slate-400">×{{ f.count }}</span>
        </FilterChip>
      </div>
    </template>
    <p v-else class="text-sm text-slate-400">{{ t("capture.none") }}</p>
  </div>
</template>
