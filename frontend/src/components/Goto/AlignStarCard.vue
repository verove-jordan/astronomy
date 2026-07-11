<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { GotoStar } from "@/types";
import Pill from "@/components/Common/Pill.vue";
import IconCompassArrow from "@/components/Icons/IconCompassArrow.vue";
import { btnPrimary, btnGhost } from "@/constants/styles";

// seqLabel overrides the order bubble for phase-aware sequences ("C1".."C4" calibration markers).
const props = defineProps<{
  star: GotoStar;
  canUndo?: boolean;
  seqLabel?: string;
}>();
const emit = defineEmits<{
  accept: [string];
  skip: [string];
  undo: [string];
}>();

const { t } = useI18n();

// The headline is the exact hand-controller label when the mount has a star catalogue — that is the
// name the user scrolls to on the telecommand; the catalog name stays as a secondary hint.
const displayName = computed(() => props.star.hc_name || props.star.name);
const showCatalogName = computed(
  () => !!props.star.hc_name && props.star.hc_name !== props.star.name,
);

const cardClass = computed(() => {
  switch (props.star.status) {
    case "accepted":
      return "border-l-4 border-l-green-500 bg-green-50/40 dark:bg-green-900/10";
    case "recommended":
      return "border-l-4 border-l-brand-500 bg-brand-50/40 ring-1 ring-brand-500/40 dark:bg-brand-900/10";
    default:
      return "border-l-4 border-l-slate-300 opacity-70 dark:border-l-slate-700";
  }
});
const isRecommended = computed(() => props.star.status === "recommended");
const isAccepted = computed(() => props.star.status === "accepted");
</script>

<template>
  <div
    :class="[
      'rounded-lg border border-slate-200 p-3 transition-colors dark:border-slate-700',
      cardClass,
    ]"
  >
    <div class="flex items-start gap-3">
      <div
        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-slate-200 text-sm font-bold text-slate-700 dark:bg-slate-700 dark:text-slate-100"
      >
        {{ seqLabel ?? star.order }}
      </div>
      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-semibold text-slate-800 dark:text-slate-100">{{
            displayName
          }}</span>
          <span v-if="showCatalogName" class="text-xs text-slate-400">{{
            t("goto.card.catalogName", { name: star.name })
          }}</span>
          <Pill
            color-class="bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-200"
            >{{ star.constellation }}</Pill
          >
          <Pill
            color-class="bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300"
            >{{ t("goto.card.magnitude", { mag: star.mag.toFixed(1) }) }}</Pill
          >
        </div>
        <div
          class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm text-slate-600 dark:text-slate-300"
        >
          <IconCompassArrow
            class="text-brand-500"
            :style="{ transform: `rotate(${star.az_deg}deg)` }"
          />
          <span class="font-medium">{{
            t("goto.card.look", {
              dir: star.compass,
              alt: Math.round(star.alt_deg),
            })
          }}</span>
          <span class="text-xs text-slate-400"
            >·
            {{
              t("goto.card.meridian", {
                side: t(`goto.card.${star.meridian_side}`),
              })
            }}</span
          >
        </div>
        <ul
          v-if="!isAccepted && star.reasons.length"
          class="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-slate-400"
        >
          <li v-for="(reason, i) in star.reasons" :key="i">· {{ reason }}</li>
        </ul>
      </div>
    </div>

    <div v-if="isRecommended" class="mt-3 flex flex-wrap gap-2">
      <button :class="btnPrimary" @click="emit('accept', star.name)">
        {{ t("goto.sequence.center") }}
      </button>
      <button :class="btnGhost" @click="emit('skip', star.name)">
        {{ t("goto.sequence.skip") }}
      </button>
    </div>
    <div v-else-if="isAccepted" class="mt-2 flex items-center justify-between">
      <span class="text-xs font-medium text-green-600 dark:text-green-400"
        >✓ {{ t("goto.sequence.accepted") }}</span
      >
      <button
        v-if="canUndo"
        class="text-xs text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
        @click="emit('undo', star.name)"
      >
        {{ t("goto.sequence.undo") }}
      </button>
    </div>
  </div>
</template>
