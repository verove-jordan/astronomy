<script setup lang="ts">
// A horizontal, scrollable filmstrip of the run's processing milestones (stacked, aligned, combined,
// colour-calibrated, star-reduced, final), left→right in pipeline order. Each frame is a cached
// thumbnail with a localized stage label (+ a filter chip for per-channel stages); clicking one opens a
// zoomable viewer as an 80%-of-viewport carousel (←/→ or the chevrons step between stages, Esc or a
// click on the surrounding margin closes). Renders the live stream while a job runs (`live`), falling
// back to the finished run's persisted previews (`result.stage_previews`). Reuses ImageViewer for enlarge.
import { computed, ref, watch, onBeforeUnmount } from "vue";
import { useI18n } from "vue-i18n";
import { fileUrl, thumbUrl, jobStages, exportJobStage } from "@/services/api";
import { card, btnGhost } from "@/constants/styles";
import ImageViewer from "@/components/Common/ImageViewer.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";
import IconSliders from "@/components/Icons/IconSliders.vue";
import IconDownload from "@/components/Icons/IconDownload.vue";
import type {
  PhotomRecord,
  RunResult,
  StageArtifact,
  StagePreview,
} from "@/types";

const props = defineProps<{
  result?: RunResult | null;
  live?: StagePreview[];
  // editable adds a per-card "edit & re-run" affordance (completed deepsky/nebula runs); clicking it
  // emits the card's stage so the parent can open the param editor and re-run from that stage.
  editable?: boolean;
  // photom records (live or from run.json) caption the per-session `normalized` cards (×scale/offset).
  photom?: PhotomRecord[];
  // jobId enables the full-resolution downloads: the strip's own frames are half-scale 8-bit PNGs, so
  // the exportable stages are fetched from the run and rendered on demand. Absent (the Runs gallery
  // mounts from a disk run.json with no job) → the download row is simply not shown.
  jobId?: number;
}>();
const emit = defineEmits<{ edit: [stage: string] }>();
const { t } = useI18n();

// Prefer the live stream; fall back to the finished run's persisted previews. Sorted by index (the
// pipeline order) so the strip always reads left→right regardless of arrival order.
const previews = computed<StagePreview[]>(() => {
  const src =
    props.live && props.live.length
      ? props.live
      : (props.result?.stage_previews ?? []);
  return [...src].sort((a, b) => a.index - b.index);
});

// One strip row per capture night (sorted), then the run-level row (session-less milestones) last.
// Items keep their index into the flat `previews` array so the carousel tours EVERYTHING in pipeline
// order. A run with no session-tagged previews renders exactly the historical single row.
const rows = computed<
  { session: string; items: { sp: StagePreview; idx: number }[] }[]
>(() => {
  const bySession = new Map<string, { sp: StagePreview; idx: number }[]>();
  previews.value.forEach((sp, idx) => {
    const key = sp.session ?? "";
    if (!bySession.has(key)) bySession.set(key, []);
    bySession.get(key)!.push({ sp, idx });
  });
  const sessions = [...bySession.keys()].sort((a, b) => {
    if ((a === "") !== (b === "")) return a === "" ? 1 : -1; // run-level row last
    return a.localeCompare(b);
  });
  return sessions.map((session) => ({
    session,
    items: bySession.get(session)!,
  }));
});

// stageKey is a card's identity (stage + filter + session) — the :key that survives re-emits and can
// never collide across parallel sessions the way the bare ordinal could.
const stageKey = (sp: StagePreview) =>
  `${sp.stage}|${sp.filter ?? ""}|${sp.session ?? ""}`;

// photomFor joins a normalized card to its group's record (caption + tooltip).
function photomFor(sp: StagePreview): PhotomRecord | undefined {
  if (sp.stage !== "normalized" && sp.stage !== "prenorm") return undefined;
  return props.photom?.find(
    (r) =>
      (r.session ?? "") === (sp.session ?? "") &&
      (!sp.filter || r.label.startsWith(sp.filter + " ")),
  );
}
function photomCaption(sp: StagePreview): string {
  const r = photomFor(sp);
  if (!r || sp.stage !== "normalized") return "";
  const sign = r.offset >= 0 ? "+" : "";
  return `×${r.scale.toFixed(2)} · ${sign}${r.offset.toFixed(3)}`;
}

// stageLabel maps a stage key to its localized label, falling back to the raw key if unlocalized.
function stageLabel(stage: string): string {
  const key = `stagePreviews.stages.${stage}`;
  const label = t(key);
  return label === key ? stage : label;
}

// Carousel state: an index into `previews` (null = closed). ←/→ and the chevrons cycle; Esc / a margin
// click close. Arrow keys are captured at the window level (capture phase + stopPropagation) so they
// always drive the carousel and never reach ImageViewer's own arrow-to-pan handler.
const activeIndex = ref<number | null>(null);
const active = computed(() =>
  activeIndex.value === null
    ? null
    : (previews.value[activeIndex.value] ?? null),
);

function step(dir: number) {
  const n = previews.value.length;
  if (activeIndex.value === null || n === 0) return;
  activeIndex.value = (activeIndex.value + dir + n) % n;
}
function onKey(e: KeyboardEvent) {
  if (e.key === "ArrowRight") {
    e.preventDefault();
    e.stopPropagation();
    step(1);
  } else if (e.key === "ArrowLeft") {
    e.preventDefault();
    e.stopPropagation();
    step(-1);
  } else if (e.key === "Escape") {
    close();
  }
}
function open(i: number) {
  activeIndex.value = i;
  window.addEventListener("keydown", onKey, true);
}
function close() {
  activeIndex.value = null;
  window.removeEventListener("keydown", onKey, true);
}
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));

// Full-resolution stage exports. The list is fetched once per job and is deliberately NOT one entry
// per timeline card: some stages the strip shows have had their source overwritten in place by later
// passes, so they cannot be re-rendered honestly and the API does not offer them.
const stages = ref<StageArtifact[]>([]);
const exporting = ref("");
const exportError = ref("");

watch(
  () => props.jobId,
  async (id) => {
    stages.value = [];
    exportError.value = "";
    if (!id) return;
    try {
      stages.value = (await jobStages(id)).stages ?? [];
    } catch {
      stages.value = []; // a run with no exportable stages just hides the row
    }
  },
  { immediate: true },
);

// artifactFor maps a timeline card to its full-resolution export, when the run still holds that
// stage's source. Per-channel milestones key on stage+filter ("stacked_RGB"); the rest on the stage
// alone. A card with no match is not a bug: several linear intermediates are reprocessed IN PLACE,
// so their pixels no longer exist by the time the run ends and the API refuses to fake them.
function artifactFor(sp: StagePreview): StageArtifact | undefined {
  const keys = sp.filter ? [`${sp.stage}_${sp.filter}`, sp.stage] : [sp.stage];
  return stages.value.find((a) => keys.includes(a.key));
}

// leftoverStages are exportable stages with no frame in the strip (trimmed / background / denoised /
// stretched). They get their own row so nothing the run preserved is unreachable.
const leftoverStages = computed(() =>
  stages.value.filter(
    (a) => !previews.value.some((sp) => artifactFor(sp)?.key === a.key),
  ),
);

// downloadPreview hands over the strip's own PNG. Always available, but HALF scale — it is the
// fallback for a card whose full-resolution source was reprocessed away.
function downloadPreview(sp: StagePreview) {
  window.open(fileUrl(sp.png_path), "_blank");
}

// download renders the stage at native resolution, then hands the browser the written file.
async function download(a: StageArtifact, format: "png" | "tif") {
  if (!props.jobId || exporting.value) return;
  exporting.value = a.key + format;
  exportError.value = "";
  try {
    const { path } = await exportJobStage(props.jobId, a.key, format);
    window.open(fileUrl(path), "_blank");
  } catch (e) {
    exportError.value = e instanceof Error ? e.message : String(e);
  } finally {
    exporting.value = "";
  }
}

const arrowBtn =
  "absolute top-1/2 -translate-y-1/2 rounded-full bg-black/50 p-2 text-white transition hover:bg-black/70 focus:outline-none";
</script>

<template>
  <section v-if="previews.length" :class="card" data-demo="stage-previews">
    <h2 class="text-lg font-medium">{{ t("stagePreviews.title") }}</h2>
    <p class="mb-3 text-sm text-slate-500 dark:text-slate-400">
      {{ t("stagePreviews.hint") }}
    </p>
    <div v-for="row in rows" :key="row.session" class="mb-1">
      <div
        v-if="rows.length > 1"
        class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
      >
        {{
          row.session
            ? t("sessions.nightTitle", { date: row.session })
            : t("stagePreviews.title")
        }}
      </div>
      <div class="flex gap-3 overflow-x-auto pb-2">
        <div
          v-for="{ sp, idx } in row.items"
          :key="stageKey(sp)"
          class="w-40 shrink-0 overflow-hidden rounded-lg border border-slate-200 dark:border-slate-700"
        >
          <button
            class="group block w-full text-left"
            :title="t('stagePreviews.enlarge')"
            data-demo="stage-preview-frame"
            @click="open(idx)"
          >
            <div class="bg-slate-900">
              <img
                :src="thumbUrl(sp.png_path, 320)"
                :alt="stageLabel(sp.stage)"
                class="h-28 w-full object-contain transition group-hover:opacity-90"
              />
            </div>
            <div class="flex items-center justify-between gap-1 p-2">
              <span class="truncate text-xs font-medium">
                {{ stageLabel(sp.stage) }}
                <span
                  v-if="photomCaption(sp)"
                  class="block truncate font-normal text-slate-500 dark:text-slate-400"
                  :title="t('job.photomHint')"
                  >{{ photomCaption(sp) }}</span
                >
              </span>
              <FilterChip v-if="sp.filter" :filter="sp.filter" />
            </div>
          </button>
          <div
            class="flex items-center gap-1 border-t border-slate-200 px-2 py-1 dark:border-slate-700"
            data-demo="stage-download"
          >
            <IconDownload class="h-3 w-3 shrink-0 text-slate-400" />
            <template v-if="artifactFor(sp)">
              <button
                v-for="f in ['png', 'tif'] as const"
                :key="f"
                class="rounded px-1.5 py-0.5 text-[11px] font-semibold uppercase text-brand-600 transition hover:bg-brand-50 disabled:opacity-40 dark:text-brand-400 dark:hover:bg-brand-900/20"
                :disabled="exporting !== ''"
                :title="
                  t('stagePreviews.fullRes.fullTitle', {
                    format: f.toUpperCase(),
                  })
                "
                @click.stop="download(artifactFor(sp)!, f)"
              >
                {{ exporting === artifactFor(sp)!.key + f ? "…" : f }}
              </button>
            </template>
            <button
              v-else
              class="rounded px-1.5 py-0.5 text-[11px] font-semibold uppercase text-slate-500 transition hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
              :title="t('stagePreviews.fullRes.previewTitle')"
              @click.stop="downloadPreview(sp)"
            >
              png
            </button>
          </div>
          <button
            v-if="editable"
            class="flex w-full items-center justify-center gap-1 border-t border-slate-200 px-2 py-1.5 text-xs font-medium text-brand-600 transition hover:bg-brand-50 dark:border-slate-700 dark:text-brand-400 dark:hover:bg-brand-900/20"
            data-demo="stage-edit"
            @click="emit('edit', sp.stage)"
          >
            <IconSliders class="h-3.5 w-3.5" />
            {{ t("rerun.editStage") }}
          </button>
        </div>
      </div>
    </div>

    <!-- Full-resolution downloads. The frames above are half-scale 8-bit previews; these render the
         preserved source of each stage at native resolution. Only stages the run still holds
         faithfully appear — see internal/pipeline/stageexport.go. -->
    <div
      v-if="leftoverStages.length"
      class="mt-3 border-t border-slate-200 pt-3 dark:border-slate-700"
      data-demo="stage-fullres"
    >
      <h3 class="text-sm font-medium">
        {{ t("stagePreviews.fullRes.title") }}
      </h3>
      <p class="mb-2 text-xs text-slate-500 dark:text-slate-400">
        {{ t("stagePreviews.fullRes.hint") }}
      </p>
      <ul class="flex flex-wrap gap-2">
        <li
          v-for="a in leftoverStages"
          :key="a.key"
          class="flex items-center gap-1.5 rounded-lg border border-slate-200 px-2 py-1 dark:border-slate-700"
        >
          <span class="text-xs">{{ a.label }}</span>
          <button
            v-for="f in ['png', 'tif'] as const"
            :key="f"
            :class="btnGhost"
            class="px-1.5 py-0.5 text-[11px] uppercase"
            :disabled="exporting !== ''"
            :title="
              t('stagePreviews.fullRes.download', { format: f.toUpperCase() })
            "
            @click="download(a, f)"
          >
            {{
              exporting === a.key + f ? t("stagePreviews.fullRes.exporting") : f
            }}
          </button>
        </li>
      </ul>
    </div>

    <!-- Export failures surface here regardless of which button started them — a thumbnail, the
         modal, or the leftover row — so a failed render can never be silent. -->
    <p
      v-if="exportError"
      class="mt-2 text-xs text-rose-600 dark:text-rose-400"
      data-demo="stage-export-error"
    >
      {{ exportError }}
    </p>

    <!-- Enlarge carousel: a centred 80%-of-viewport panel over a dimmed backdrop; clicking the surrounding
         margin (backdrop) or pressing Esc closes; ←/→ or the chevrons step between stages. -->
    <div
      v-if="active"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      @click.self="close"
    >
      <div
        class="relative flex h-[80vh] w-[80vw] flex-col rounded-lg bg-slate-950 p-4 shadow-xl"
      >
        <div class="mb-2 flex items-center justify-between text-white">
          <span class="text-sm font-medium">
            {{ stageLabel(active.stage)
            }}<span v-if="active.filter"> · {{ active.filter }}</span
            ><span v-if="active.session">
              · {{ t("sessions.nightTitle", { date: active.session }) }}</span
            >
          </span>
          <div class="flex items-center gap-2">
            <template v-if="artifactFor(active)">
              <button
                v-for="f in ['png', 'tif'] as const"
                :key="f"
                :class="btnGhost"
                class="inline-flex items-center gap-1"
                :disabled="exporting !== ''"
                :title="
                  t('stagePreviews.fullRes.fullTitle', {
                    format: f.toUpperCase(),
                  })
                "
                @click="download(artifactFor(active)!, f)"
              >
                <IconDownload class="h-3.5 w-3.5" />
                {{
                  exporting === artifactFor(active)!.key + f
                    ? t("stagePreviews.fullRes.exporting")
                    : f.toUpperCase()
                }}
              </button>
            </template>
            <button
              v-else
              :class="btnGhost"
              class="inline-flex items-center gap-1"
              :title="t('stagePreviews.fullRes.previewTitle')"
              @click="downloadPreview(active)"
            >
              <IconDownload class="h-3.5 w-3.5" />
              PNG
            </button>
            <button :class="btnGhost" @click="close">
              {{ t("stagePreviews.close") }}
            </button>
          </div>
        </div>
        <div class="relative min-h-0 flex-1">
          <ImageViewer
            :src="fileUrl(active.png_path)"
            :alt="stageLabel(active.stage)"
            height-class="h-full"
          />
          <template v-if="previews.length > 1">
            <button
              :class="[arrowBtn, 'left-2']"
              :title="t('common.previous')"
              @click="step(-1)"
            >
              <IconChevronRight class="rotate-180" />
            </button>
            <button
              :class="[arrowBtn, 'right-2']"
              :title="t('common.next')"
              @click="step(1)"
            >
              <IconChevronRight />
            </button>
          </template>
        </div>
        <div class="mt-2 text-center text-xs text-slate-400">
          {{ (activeIndex ?? 0) + 1 }} / {{ previews.length }}
        </div>
      </div>
    </div>
  </section>
</template>
