<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { BORTLE_COLORS } from "@/utils/bortle";

const { t } = useI18n();
const gradient = `linear-gradient(to right, ${BORTLE_COLORS.join(", ")})`;
</script>

<template>
  <div class="mt-2 text-[10px] text-slate-500 dark:text-slate-400">
    <div class="mb-0.5 flex items-center justify-between">
      <span>{{ t("tonight.layers.legendDark") }}</span>
      <span>{{ t("tonight.layers.legendBright") }}</span>
    </div>
    <div
      class="h-2 w-full rounded border border-slate-200 dark:border-slate-700"
      :style="{ background: gradient }"
    />
    <div class="mt-0.5 flex items-center justify-between">
      <span>{{ t("tonight.layers.bortle") }} 1</span>
      <span>9</span>
    </div>
  </div>
</template>
