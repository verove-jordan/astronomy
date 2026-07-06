<script setup lang="ts">
// Improvement-series timeline: the campaign's goal/status/best-vs-target header plus one horizontal
// card per attempt (a normal job linked by series_id) — created date, status, best supervised score
// and final-image thumb, each linking to its job. Continue/Stop drive the series
// (POST /api/series/{id}/continue|stop).
import { computed, onMounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useSeriesStore } from "@/stores/series";
import { thumbUrl } from "@/services/api";
import Pill from "@/components/Common/Pill.vue";
import StatusPill from "@/components/Common/StatusPill.vue";
import Spinner from "@/components/Common/Spinner.vue";
import { btnGhost, btnPrimary, card } from "@/constants/styles";
import { formatTimestamp } from "@/utils/format";
import type { Job } from "@/types";

const props = defineProps<{ seriesId: number }>();
const { t } = useI18n();
const store = useSeriesStore();

onMounted(() => void store.get(props.seriesId));
watch(
  () => props.seriesId,
  (id) => void store.get(id),
);

const detail = computed(() => store.details[props.seriesId] ?? null);
const sr = computed(() => detail.value?.series ?? null);
const acting = computed(() => !!store.acting[props.seriesId]);

// Series status chip colors (complete JIT-safe strings keyed by runtime value).
const seriesPill: Record<string, string> = {
  active:
    "bg-brand-100 text-brand-800 dark:bg-brand-900/40 dark:text-brand-300",
  done: "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300",
  stopped: "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300",
};

// bestScore digs an attempt's best supervised combined score out of its persisted iterations
// (deep-sky/comet/milkyway nest them under `final`; planetary's flat result carries them top-level).
function bestScore(job: Job): number | null {
  const iters = job.result?.final?.iterations ?? job.result?.iterations ?? [];
  if (!iters.length) return null;
  return Math.max(...iters.map((it) => it.combined_score));
}

// previewFor resolves the attempt's final PNG as a small S3-fallback-tagged thumb ("" when the
// result has no image output yet).
function previewFor(job: Job): string {
  const outs = job.result?.final?.outputs ?? job.result?.outputs ?? [];
  const png = outs.find((o) => o.endsWith(".png"));
  return png ? thumbUrl(png, 320) : "";
}

const attempts = computed(() =>
  (detail.value?.jobs ?? []).map((job, i) => ({
    job,
    n: i + 1,
    score: bestScore(job),
    thumb: previewFor(job),
  })),
);
</script>

<template>
  <section v-if="sr || store.loading" :class="card" data-demo="series-panel">
    <Spinner v-if="!sr">{{ t("common.loading") }}</Spinner>
    <template v-else>
      <div class="flex flex-wrap items-center gap-2">
        <h2 class="text-lg font-medium">
          {{ t("series.title") }} #{{ sr.id }}
        </h2>
        <Pill :color-class="seriesPill[sr.status] ?? seriesPill.stopped">
          {{ t("series.status." + sr.status) }}
        </Pill>
        <span
          class="min-w-0 truncate text-sm text-slate-500 dark:text-slate-400"
        >
          {{ sr.object }}
        </span>
        <span class="ml-auto flex shrink-0 items-center gap-2">
          <button
            v-if="sr.status !== 'active'"
            :class="btnPrimary"
            :disabled="acting"
            data-demo="series-continue"
            @click="store.continueSeries(sr.id)"
          >
            {{ t("series.continue") }}
          </button>
          <button
            v-else
            :class="btnGhost"
            :disabled="acting"
            data-demo="series-stop"
            @click="store.stopSeries(sr.id)"
          >
            {{ t("series.stop") }}
          </button>
        </span>
      </div>

      <p v-if="sr.goal" class="mt-1 text-sm text-slate-600 dark:text-slate-300">
        <span class="font-medium text-slate-500 dark:text-slate-400"
          >{{ t("series.goal") }}:</span
        >
        {{ sr.goal }}
      </p>
      <div
        class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500 dark:text-slate-400"
      >
        <span>
          {{ t("series.attempts") }}:
          <span class="font-medium text-slate-700 dark:text-slate-200"
            >{{ attempts.length }}/{{ sr.max_attempts }}</span
          >
        </span>
        <span v-if="sr.best_score > 0">
          {{ t("series.best") }}:
          <span class="font-medium text-slate-700 dark:text-slate-200">{{
            sr.best_score.toFixed(1)
          }}</span>
        </span>
        <span v-if="sr.target_score > 0">
          {{ t("series.target") }}:
          <span class="font-medium text-slate-700 dark:text-slate-200">{{
            sr.target_score.toFixed(1)
          }}</span>
        </span>
      </div>

      <!-- Horizontal attempt cards, oldest → newest; the campaign's best attempt is ringed. -->
      <div v-if="attempts.length" class="mt-3 flex gap-3 overflow-x-auto pb-1">
        <router-link
          v-for="a in attempts"
          :key="a.job.id"
          :to="{ name: 'job', params: { id: String(a.job.id) } }"
          class="w-44 shrink-0 rounded-md border p-2 transition-colors"
          :class="
            a.job.id === sr.best_job_id
              ? 'border-brand-500 ring-1 ring-brand-500'
              : 'border-slate-200 hover:border-brand-400 dark:border-slate-700 dark:hover:border-brand-500'
          "
        >
          <div
            class="mb-1.5 aspect-video w-full overflow-hidden rounded bg-slate-100 dark:bg-slate-900"
          >
            <img
              v-if="a.thumb"
              :src="a.thumb"
              :alt="t('series.attempt', { n: a.n })"
              class="h-full w-full object-cover"
              loading="lazy"
              decoding="async"
            />
            <div
              v-else
              class="flex h-full items-center justify-center text-[10px] text-slate-400"
            >
              {{ t("common.none") }}
            </div>
          </div>
          <div class="flex items-center justify-between gap-1">
            <span class="text-xs font-medium">
              {{ t("series.attempt", { n: a.n }) }}
            </span>
            <StatusPill :status="a.job.status" />
          </div>
          <div
            class="mt-1 flex items-center justify-between gap-1 text-[10px] text-slate-500 dark:text-slate-400"
          >
            <span>{{ formatTimestamp(a.job.created_at) }}</span>
            <span
              v-if="a.score !== null"
              class="font-medium text-slate-700 dark:text-slate-200"
              >{{ a.score.toFixed(1) }}</span
            >
          </div>
        </router-link>
      </div>
      <p v-else class="mt-3 text-xs text-slate-400">{{ t("series.empty") }}</p>
    </template>
  </section>
</template>
