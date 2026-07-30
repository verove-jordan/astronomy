<script setup lang="ts">
// Compact chips summarizing a job's run parameters (the Process-page checkboxes/selects), so the Tasks
// list and a task's detail record — and let you compare — what a run was configured with. With
// `showKnobs`, the fine-knob JSON overrides (params.params) get one chip each too — the task detail
// shows the FULL recipe while list rows stay compact.
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { JobParams } from "@/types";
import Pill from "@/components/Common/Pill.vue";

const props = defineProps<{
  params?: JobParams;
  showGoal?: boolean;
  showKnobs?: boolean;
}>();
const { t, te } = useI18n();

interface Chip {
  key: string;
  label: string;
  cls: string;
}

const NEUTRAL =
  "bg-slate-100 text-slate-700 ring-1 ring-slate-300 dark:bg-slate-700/40 dark:text-slate-200 dark:ring-slate-600";
const ACCENT =
  "bg-brand-100 text-brand-800 ring-1 ring-brand-200 dark:bg-brand-900/40 dark:text-brand-300 dark:ring-brand-800/50";

const chips = computed<Chip[]>(() => {
  const p = props.params;
  if (!p) return [];
  const out: Chip[] = [];
  if (p.storage_mode === "s3")
    out.push({ key: "s3", label: t("run.chips.s3"), cls: ACCENT });
  if (p.low_disk)
    out.push({ key: "lowdisk", label: t("run.chips.s3LowDisk"), cls: NEUTRAL });
  if (p.palette && p.palette !== "natural")
    out.push({
      key: "palette",
      label: t("run.chips.palette", { name: p.palette.toUpperCase() }),
      cls: ACCENT,
    });
  if (p.color_calibration)
    out.push({ key: "spcc", label: t("run.chips.spcc"), cls: NEUTRAL });
  if (p.denoise)
    out.push({ key: "denoise", label: t("run.chips.denoise"), cls: NEUTRAL });
  if (p.ha_exclude_stars)
    out.push({ key: "ha", label: t("run.chips.ha"), cls: NEUTRAL });
  if (p.mosaic)
    out.push({ key: "mosaic", label: t("run.chips.mosaic"), cls: ACCENT });
  if (
    Number((p.params as Record<string, unknown> | undefined)?.earthshine_gain) >
    0
  )
    out.push({
      key: "earthshine",
      label: t("run.chips.earthshine"),
      cls: ACCENT,
    });
  if (p.drop_wheel_transition)
    out.push({
      key: "dropTransition",
      label: t("run.chips.dropTransition"),
      cls: NEUTRAL,
    });
  if (p.supervise)
    out.push({ key: "ai", label: t("run.chips.ai"), cls: ACCENT });
  if (p.sequential)
    out.push({
      key: "sequential",
      label: t("run.chips.sequential"),
      cls: NEUTRAL,
    });
  if (props.showGoal) {
    if (p.tier)
      out.push({
        key: "tier",
        label: t("run.chips.tier", { tier: p.tier }),
        cls: NEUTRAL,
      });
    if (p.max_iters)
      out.push({
        key: "iters",
        label: t("run.chips.iters", { n: p.max_iters }),
        cls: NEUTRAL,
      });
  }
  return out;
});

// One chip per fine-knob override (alphabetical, like the launch form's JSON box), each with the
// glossary description as its tooltip.
interface KnobChip {
  key: string;
  value: string;
  title?: string;
}
const knobChips = computed<KnobChip[]>(() => {
  const knobs = props.showKnobs
    ? ((props.params?.params ?? {}) as Record<string, unknown>)
    : {};
  return Object.keys(knobs)
    .sort()
    .map((k) => ({
      key: k,
      value: String(knobs[k]),
      title: te(`paramDocs.${k}`) ? t(`paramDocs.${k}`) : undefined,
    }));
});
</script>

<template>
  <div v-if="chips.length || knobChips.length" class="flex flex-wrap gap-1">
    <Pill v-for="c in chips" :key="c.key" :color-class="c.cls">{{
      c.label
    }}</Pill>
    <Pill
      v-for="k in knobChips"
      :key="'knob:' + k.key"
      color-class="bg-slate-100 text-slate-700 ring-1 ring-slate-300 dark:bg-slate-700/40 dark:text-slate-200 dark:ring-slate-600"
      :title="k.title"
    >
      <span class="font-mono">{{ k.key }} {{ k.value }}</span>
    </Pill>
  </div>
  <span v-else class="text-xs text-slate-400">—</span>
</template>
