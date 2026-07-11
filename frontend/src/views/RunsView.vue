<script setup lang="ts">
import { ref, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { useJobsStore } from "@/stores/jobs";
import { fileUrl, thumbUrl } from "@/services/api";
import RunResultPanels from "@/components/Common/RunResultPanels.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import EngineChip from "@/components/Common/EngineChip.vue";
import Spinner from "@/components/Common/Spinner.vue";
import { card, btnGhost } from "@/constants/styles";
import { baseName } from "@/utils/format";
import type { RunSummary, RunResult } from "@/types";

const { t } = useI18n();
const jobsStore = useJobsStore();

const openResult = ref<RunResult | null>(null);
const openTitle = ref("");
const loadingRun = ref(false);

onMounted(() => jobsStore.listRuns());

async function openRun(run: RunSummary) {
  loadingRun.value = true;
  openResult.value = null;
  openTitle.value = `${run.object} · ${run.run_id}`;
  try {
    const res = await fetch(fileUrl(run.run_json));
    if (res.ok) openResult.value = (await res.json()) as RunResult;
  } finally {
    loadingRun.value = false;
  }
}
function closeRun() {
  openResult.value = null;
  openTitle.value = "";
}
function fmtDate(ms: number): string {
  return ms ? new Date(ms).toLocaleString() : "";
}
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center gap-3">
      <div>
        <h1 class="text-2xl font-semibold">{{ t("runs.title") }}</h1>
        <p class="text-sm text-slate-500 dark:text-slate-400">
          {{ t("runs.subtitle") }}
        </p>
      </div>
      <button v-if="openTitle" :class="[btnGhost, 'ml-auto']" @click="closeRun">
        ← {{ t("runs.title") }}
      </button>
    </div>

    <!-- Opened run: render the same panels as JobView from the stored run.json -->
    <template v-if="openTitle">
      <h2 class="text-lg font-medium">{{ openTitle }}</h2>
      <Spinner v-if="loadingRun">{{ t("common.loading") }}</Spinner>
      <RunResultPanels v-else-if="openResult" :result="openResult" />
      <p v-else class="text-sm text-slate-400">{{ t("job.noResult") }}</p>
    </template>

    <!-- Gallery -->
    <template v-else>
      <Spinner v-if="jobsStore.loading">{{ t("common.loading") }}</Spinner>
      <p v-else-if="!jobsStore.runs.length" class="text-sm text-slate-400">
        {{ t("runs.empty") }}
      </p>
      <div v-else class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <button
          v-for="run in jobsStore.runs"
          :key="run.dir"
          :class="[
            card,
            'group min-w-0 text-left transition-shadow hover:shadow-md',
          ]"
          data-demo="run-card"
          @click="openRun(run)"
        >
          <div
            class="mb-2 aspect-video w-full overflow-hidden rounded-md bg-slate-100 dark:bg-slate-900"
          >
            <img
              v-if="run.final_preview"
              :src="thumbUrl(run.final_preview, 480)"
              :alt="run.object"
              class="h-full w-full object-cover transition-transform group-hover:scale-[1.02]"
              loading="lazy"
              decoding="async"
            />
            <div
              v-else
              class="flex h-full items-center justify-center text-xs text-slate-400"
            >
              {{ t("common.none") }}
            </div>
          </div>
          <div class="flex min-w-0 items-center gap-2">
            <span class="min-w-0 grow truncate font-medium">{{
              run.object
            }}</span>
            <span v-if="run.mode" class="text-xs text-slate-500">{{
              run.mode
            }}</span>
            <!-- Engine build that produced the run; amber when older than the serving engine. -->
            <EngineChip :engine="run.engine" />
          </div>
          <div
            class="mt-0.5 truncate text-xs text-slate-500 dark:text-slate-400"
          >
            {{ baseName(run.run_id) }} · {{ fmtDate(run.created_at_ms) }}
          </div>
          <div
            v-if="run.channels && run.channels.length"
            class="mt-2 flex flex-wrap gap-1"
          >
            <FilterChip
              v-for="c in run.channels"
              :key="c"
              :filter="c"
              compact
            />
          </div>
        </button>
      </div>

      <div
        v-if="jobsStore.runs.length"
        class="flex items-center justify-center gap-3 pt-2"
      >
        <button
          v-if="jobsStore.runsHasMore"
          :class="btnGhost"
          :disabled="jobsStore.loadingMore"
          @click="jobsStore.listRuns(false)"
        >
          {{ jobsStore.loadingMore ? t("common.loading") : t("runs.loadMore") }}
        </button>
        <span class="text-xs text-slate-400">
          {{
            t("runs.count", {
              n: jobsStore.runs.length,
              total: jobsStore.runsTotal,
            })
          }}
        </span>
      </div>
    </template>
  </div>
</template>
