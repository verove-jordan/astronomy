<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { CHANNEL_TARGETS } from "@/composables/useChannelMapping";
import { card, input } from "@/constants/styles";
import type { ChannelDetection } from "@/types";
import FilterChip from "@/components/Common/FilterChip.vue";

const props = defineProps<{
  detectedFilters: string[];
  modelValue: Record<string, string>;
  detection?: ChannelDetection | null;
}>();
const emit = defineEmits<{
  "update:modelValue": [v: Record<string, string>];
}>();
const { t } = useI18n();

function setTarget(f: string, target: string) {
  emit("update:modelValue", { ...props.modelValue, [f]: target });
}
function confPct(f: string): number | null {
  const run = props.detection?.runs.find((r) => r.filter === f);
  return run ? Math.round(run.confidence * 100) : null;
}
</script>

<template>
  <div v-if="detectedFilters.length" :class="card">
    <h3 class="mb-1 text-sm font-semibold text-slate-700 dark:text-slate-200">
      {{ t("mapping.title") }}
    </h3>
    <p class="mb-3 text-xs text-slate-500 dark:text-slate-400">
      {{ t("mapping.hint") }}
    </p>
    <p v-if="detection" class="mb-3 text-xs text-slate-500 dark:text-slate-400">
      {{ t("mapping.confidence") }}:
      <span class="font-medium"
        >{{ Math.round(detection.overall_confidence * 100) }}%</span
      >
    </p>
    <div class="flex flex-col gap-2">
      <div
        v-for="f in detectedFilters"
        :key="f"
        class="flex flex-wrap items-center gap-2"
      >
        <FilterChip :filter="f" />
        <span v-if="confPct(f) !== null" class="text-xs text-slate-400"
          >({{ confPct(f) }}%)</span
        >
        <span class="text-slate-300 dark:text-slate-600">→</span>
        <select
          :value="modelValue[f] ?? f"
          :class="[input, 'w-auto']"
          @change="(e) => setTarget(f, (e.target as HTMLSelectElement).value)"
        >
          <option v-for="tgt in CHANNEL_TARGETS" :key="tgt" :value="tgt">
            {{ tgt === "ignore" ? t("mapping.ignore") : tgt }}
          </option>
        </select>
      </div>
    </div>
  </div>
</template>
