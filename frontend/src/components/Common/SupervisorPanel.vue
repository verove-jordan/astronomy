<script setup lang="ts">
// Shows the optional local-AI-agent finish supervisor's work: one card per iteration with its
// rendered preview, the deterministic + model scores, the one-line reasoning, and a badge on the
// chosen best. Reads result.final.iterations (populated only for supervised runs) — renders nothing
// otherwise.
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { fileUrl } from "@/services/api";
import { card } from "@/constants/styles";
import type { RunResult } from "@/types";

const props = defineProps<{ result: RunResult | null }>();
const { t } = useI18n();

const iterations = computed(() => props.result?.final?.iterations ?? []);

function tierClass(score: number): string {
  if (score >= 8)
    return "bg-emerald-500/20 text-emerald-600 dark:text-emerald-400";
  if (score >= 6) return "bg-amber-500/20 text-amber-600 dark:text-amber-400";
  return "bg-rose-500/20 text-rose-600 dark:text-rose-400";
}
</script>

<template>
  <section v-if="iterations.length" :class="card">
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
        <div class="space-y-1 p-3">
          <div class="flex items-center justify-between gap-2">
            <span class="text-sm font-medium">{{
              t("supervisor.iteration", { n: it.index + 1 })
            }}</span>
            <span
              class="rounded px-2 py-0.5 text-xs font-semibold"
              :class="tierClass(it.combined_score)"
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
          <p
            v-if="it.reasoning"
            class="text-sm text-slate-700 dark:text-slate-300"
          >
            {{ it.reasoning }}
          </p>
        </div>
      </div>
    </div>
  </section>
</template>
