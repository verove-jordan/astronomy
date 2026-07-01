<script setup lang="ts">
// Shows the local-AI-agent finish supervisor's work: one card per iteration with its rendered
// preview, the re-entry tier it used, the diagnosed defects, the deterministic + model scores and the
// one-line reasoning, plus a badge on the chosen best. Renders the live stream while a job runs
// (`live`), falling back to the completed run's persisted iterations (`result.final.iterations`).
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { fileUrl } from "@/services/api";
import { card } from "@/constants/styles";
import MarkdownText from "@/components/Common/MarkdownText.vue";
import type { RunResult, IterationRecord } from "@/types";

const props = defineProps<{
  result?: RunResult | null;
  live?: IterationRecord[];
}>();
const { t } = useI18n();

// Prefer the live stream (freshest, incl. the winner's chosen re-emit); fall back to the persisted
// iterations when opening an already-finished job.
const iterations = computed<IterationRecord[]>(() =>
  props.live && props.live.length
    ? props.live
    : (props.result?.final?.iterations ?? []),
);

function scoreClass(score: number): string {
  if (score >= 8)
    return "bg-emerald-500/20 text-emerald-600 dark:text-emerald-400";
  if (score >= 6) return "bg-amber-500/20 text-amber-600 dark:text-amber-400";
  return "bg-rose-500/20 text-rose-600 dark:text-rose-400";
}

function severityClass(severity: string): string {
  if (severity === "high")
    return "bg-rose-500/15 text-rose-600 dark:text-rose-400";
  if (severity === "medium")
    return "bg-amber-500/15 text-amber-600 dark:text-amber-400";
  return "bg-slate-500/15 text-slate-500 dark:text-slate-400";
}

// Humanize a defect kind ("background_cast_green" → "background cast green") for the chip label.
function defectLabel(kind: string): string {
  return kind.replace(/_/g, " ");
}
</script>

<template>
  <section v-if="iterations.length" :class="card" data-demo="supervisor-panel">
    <h2 class="text-lg font-medium">{{ t("supervisor.title") }}</h2>
    <p class="mb-4 text-sm text-slate-500 dark:text-slate-400">
      {{ t("supervisor.hint") }}
    </p>
    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <div
        v-for="it in iterations"
        :key="it.index"
        class="overflow-hidden rounded-lg border"
        :class="
          it.chosen
            ? 'border-emerald-500 ring-1 ring-emerald-500/40'
            : 'border-slate-200 dark:border-slate-700'
        "
      >
        <div class="relative bg-slate-900">
          <img
            :src="fileUrl(it.png_path)"
            :alt="t('supervisor.iteration', { n: it.index + 1 })"
            class="h-40 w-full object-contain"
          />
          <span
            v-if="it.chosen"
            class="absolute right-2 top-2 rounded bg-emerald-500 px-1.5 py-0.5 text-xs font-medium text-white"
          >
            {{ t("supervisor.chosen") }}
          </span>
        </div>
        <div class="space-y-1.5 p-3">
          <div class="flex items-center justify-between gap-2">
            <span class="flex items-center gap-1.5 text-sm font-medium">
              {{ t("supervisor.iteration", { n: it.index + 1 }) }}
              <span
                v-if="it.tier"
                class="rounded bg-sky-500/15 px-1.5 py-0.5 text-xs font-semibold text-sky-600 dark:text-sky-400"
                :title="t(`supervisor.tierName.${it.tier}`)"
              >
                {{ t("supervisor.tier", { tier: it.tier }) }}
              </span>
            </span>
            <span
              class="rounded px-2 py-0.5 text-xs font-semibold"
              :class="scoreClass(it.combined_score)"
            >
              {{ it.combined_score.toFixed(1) }}
            </span>
          </div>
          <p class="text-xs text-slate-500 dark:text-slate-400">
            {{
              t("supervisor.scoreSplit", {
                det: it.det_score.toFixed(1),
                model: it.model_score.toFixed(1),
              })
            }}
          </p>
          <div
            v-if="it.defects && it.defects.length"
            class="flex flex-wrap gap-1"
          >
            <span
              v-for="(d, i) in it.defects"
              :key="i"
              class="rounded px-1.5 py-0.5 text-xs"
              :class="severityClass(d.severity)"
              :title="d.note || d.severity"
            >
              {{ defectLabel(d.kind) }}
            </span>
          </div>
          <div v-if="it.reasoning" class="text-slate-700 dark:text-slate-300">
            <MarkdownText :text="it.reasoning" />
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
