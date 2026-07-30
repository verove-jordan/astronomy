<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { SkyTarget } from "@/types";
import {
  btnGhost,
  card,
  scoreTier,
  scoreTierBar,
  emissionLineBar,
} from "@/constants/styles";
import ProgressBar from "@/components/Common/ProgressBar.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import AladinView from "@/components/Sky/AladinView.vue";

const props = defineProps<{
  target: SkyTarget | null;
  fovWDeg: number;
  fovHDeg: number;
}>();
const { t } = useI18n();

const SUBSCORE_KEYS = [
  "max_alt",
  "alt_now",
  "dark_hours",
  "framing",
  "detectability",
  "moon",
] as const;

const bars = computed(() => {
  const s = props.target?.subscores;
  if (!s) return [];
  return SUBSCORE_KEYS.map((k) => {
    const pct = Math.round((s[k] ?? 0) * 100);
    return { key: k, pct, barClass: scoreTierBar[scoreTier(pct)] };
  });
});

// Emission lines to plot: Hα/OIII/SII always; Hβ/NII only when the (curated) data provides them.
const lines = computed(() => {
  const c = props.target?.composition;
  if (!c) return [];
  const raw: { key: string; val: number }[] = [
    { key: "ha", val: c.ha },
    { key: "oiii", val: c.oiii },
    { key: "sii", val: c.sii },
  ];
  if (c.hb) raw.push({ key: "hb", val: c.hb });
  if (c.nii) raw.push({ key: "nii", val: c.nii });
  return raw.map((l) => ({
    ...l,
    pct: Math.round(l.val * 100),
    barClass: emissionLineBar[l.key] ?? "bg-slate-400",
  }));
});

// Exit pupil outside the comfortable 0.5–7 mm window (empty magnification or too dim / sky too bright).
const exitWarn = computed(() => {
  const ep = props.target?.exit_pupil_mm;
  return ep != null && ep > 0 && (ep < 0.5 || ep > 7);
});

// Size as the true ellipse "major'×minor'" when a minor axis is known, else the major axis alone.
const sizeText = computed(() => {
  const tg = props.target;
  if (!tg || !(tg.size_arcmin > 0)) return "—";
  const maj = tg.size_arcmin.toFixed(1);
  return tg.size_minor_arcmin && tg.size_minor_arcmin > 0
    ? `${maj}'×${tg.size_minor_arcmin.toFixed(1)}'`
    : `${maj}'`;
});
</script>

<template>
  <div :class="card" role="region" aria-live="polite">
    <template v-if="target">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
          {{ t("tonight.detail.title", { name: target.name }) }}
        </h3>
        <router-link
          :to="{ name: 'mosaic', query: { object: target.name } }"
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          >{{ t("tonight.detail.planMosaic") }}</router-link
        >
      </div>
      <p
        v-if="target.fov_fill_pct > 100"
        class="mt-1 text-xs text-amber-600 dark:text-amber-400"
      >
        {{ t("tonight.detail.mosaicHint") }}
      </p>

      <AladinView
        class="mt-2"
        :target="target"
        :fov-w-deg="fovWDeg"
        :fov-h-deg="fovHDeg"
      />

      <p class="mt-3 text-sm text-slate-500 dark:text-slate-400">
        {{ target.reason }}
      </p>

      <!-- Object facts (type, common name, morphology, size, surface brightness) from the catalogue. -->
      <dl class="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
        <div class="flex justify-between gap-2">
          <dt class="text-slate-400 dark:text-slate-500">
            {{ t("tonight.detail.type") }}
          </dt>
          <dd class="text-slate-600 dark:text-slate-300">
            {{ t("tonight.types." + target.type) }}
          </dd>
        </div>
        <div v-if="target.common_name" class="flex justify-between gap-2">
          <dt class="text-slate-400 dark:text-slate-500">
            {{ t("tonight.detail.commonName") }}
          </dt>
          <dd class="text-slate-600 dark:text-slate-300">
            {{ target.common_name }}
          </dd>
        </div>
        <div v-if="target.morphology" class="flex justify-between gap-2">
          <dt class="text-slate-400 dark:text-slate-500">
            {{ t("tonight.detail.morphology") }}
          </dt>
          <dd class="text-slate-600 dark:text-slate-300">
            {{ target.morphology }}
          </dd>
        </div>
        <div v-if="target.size_arcmin > 0" class="flex justify-between gap-2">
          <dt class="text-slate-400 dark:text-slate-500">
            {{ t("tonight.detail.size") }}
          </dt>
          <dd class="text-slate-600 dark:text-slate-300">{{ sizeText }}</dd>
        </div>
        <div
          v-if="target.surface_brightness > 0"
          class="flex justify-between gap-2"
        >
          <dt class="text-slate-400 dark:text-slate-500">
            {{ t("tonight.detail.surfaceBrightness") }}
          </dt>
          <dd class="text-slate-600 dark:text-slate-300">
            {{ target.surface_brightness.toFixed(1) }}
          </dd>
        </div>
      </dl>

      <!-- Visual (eyepiece) recommendation -->
      <div
        v-if="target.chosen_eyepiece"
        class="mt-3 rounded-md bg-slate-50 p-2 text-xs dark:bg-slate-800/50"
      >
        <div
          class="mb-1 font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
        >
          {{ t("tonight.visual.title") }}
        </div>
        <div
          class="grid grid-cols-2 gap-x-4 gap-y-1 tabular-nums sm:grid-cols-4"
        >
          <div>
            <span class="text-slate-400"
              >{{ t("tonight.visual.eyepiece") }}: </span
            >{{ target.chosen_eyepiece }}
          </div>
          <div>
            <span class="text-slate-400"
              >{{ t("tonight.visual.magnification") }}: </span
            >{{ Math.round(target.mag_x ?? 0) }}×
          </div>
          <div>
            <span class="text-slate-400"
              >{{ t("tonight.visual.trueField") }}: </span
            >{{ (target.true_fov_deg ?? 0).toFixed(2) }}°
          </div>
          <div>
            <span class="text-slate-400"
              >{{ t("tonight.visual.exitPupil") }}: </span
            >{{ (target.exit_pupil_mm ?? 0).toFixed(1) }} mm
          </div>
        </div>
        <div v-if="target.surface_brightness" class="mt-1 text-slate-400">
          {{ t("tonight.visual.surfaceBrightness") }}:
          {{ target.surface_brightness.toFixed(1) }}
        </div>
        <p
          v-if="target.fov_fill_pct > 100"
          class="mt-1 text-amber-600 dark:text-amber-400"
        >
          {{ t("tonight.visual.noFit") }}
        </p>
        <p v-else-if="exitWarn" class="mt-1 text-amber-600 dark:text-amber-400">
          {{ t("tonight.visual.exitWarn") }}
        </p>
      </div>

      <div class="mt-3 grid gap-x-4 gap-y-2 sm:grid-cols-2">
        <div v-for="b in bars" :key="b.key" class="flex items-center gap-2">
          <span
            class="w-24 shrink-0 text-xs text-slate-500 dark:text-slate-400"
            >{{ t("tonight.sub." + b.key) }}</span
          >
          <ProgressBar :percent="b.pct" :bar-class="b.barClass" />
          <span
            class="w-7 text-right text-xs tabular-nums text-slate-500 dark:text-slate-400"
            >{{ b.pct }}</span
          >
        </div>
      </div>

      <!-- Emission composition / filter-wheel planning -->
      <div class="mt-4">
        <div class="flex items-center justify-between">
          <h4
            class="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
          >
            {{ t("tonight.composition.title") }}
          </h4>
          <span class="text-[10px] uppercase tracking-wide text-slate-400">
            {{ t("tonight.composition.source." + target.composition.source) }}
          </span>
        </div>
        <div class="mt-1 flex flex-wrap items-center gap-1.5">
          <span class="text-xs text-slate-500 dark:text-slate-400"
            >{{ t("tonight.composition.load") }}:</span
          >
          <FilterChip
            v-for="f in target.composition.filters"
            :key="f"
            :filter="f"
          />
          <span class="ml-1 text-xs text-slate-400"
            >· {{ target.composition.palette }}</span
          >
        </div>
        <div class="mt-2 grid gap-x-4 gap-y-1.5 sm:grid-cols-2">
          <div v-for="l in lines" :key="l.key" class="flex items-center gap-2">
            <span
              class="w-10 shrink-0 text-xs text-slate-500 dark:text-slate-400"
              >{{ t("tonight.lines." + l.key) }}</span
            >
            <ProgressBar :percent="l.pct" :bar-class="l.barClass" />
          </div>
        </div>
        <p v-if="target.composition.note" class="mt-1 text-xs text-slate-400">
          {{ target.composition.note }}
        </p>
      </div>
    </template>
    <p v-else class="text-sm text-slate-500 dark:text-slate-400">
      {{ t("tonight.detail.select") }}
    </p>
  </div>
</template>
