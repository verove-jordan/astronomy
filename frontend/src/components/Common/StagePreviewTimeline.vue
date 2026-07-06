<script setup lang="ts">
// A horizontal, scrollable filmstrip of the run's processing milestones (stacked, aligned, combined,
// colour-calibrated, star-reduced, final), left→right in pipeline order. Each frame is a cached
// thumbnail with a localized stage label (+ a filter chip for per-channel stages); clicking one opens a
// zoomable viewer as an 80%-of-viewport carousel (←/→ or the chevrons step between stages, Esc or a
// click on the surrounding margin closes). Renders the live stream while a job runs (`live`), falling
// back to the finished run's persisted previews (`result.stage_previews`). Reuses ImageViewer for enlarge.
import { computed, ref, onBeforeUnmount } from "vue";
import { useI18n } from "vue-i18n";
import { fileUrl, thumbUrl } from "@/services/api";
import { card, btnGhost } from "@/constants/styles";
import ImageViewer from "@/components/Common/ImageViewer.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";
import type { RunResult, StagePreview } from "@/types";

const props = defineProps<{
  result?: RunResult | null;
  live?: StagePreview[];
}>();
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
  activeIndex.value === null ? null : (previews.value[activeIndex.value] ?? null),
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

const arrowBtn =
  "absolute top-1/2 -translate-y-1/2 rounded-full bg-black/50 p-2 text-white transition hover:bg-black/70 focus:outline-none";
</script>

<template>
  <section v-if="previews.length" :class="card" data-demo="stage-previews">
    <h2 class="text-lg font-medium">{{ t("stagePreviews.title") }}</h2>
    <p class="mb-3 text-sm text-slate-500 dark:text-slate-400">
      {{ t("stagePreviews.hint") }}
    </p>
    <div class="flex gap-3 overflow-x-auto pb-2">
      <button
        v-for="(sp, idx) in previews"
        :key="sp.index"
        class="group w-40 shrink-0 overflow-hidden rounded-lg border border-slate-200 text-left dark:border-slate-700"
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
          <span class="truncate text-xs font-medium">{{
            stageLabel(sp.stage)
          }}</span>
          <FilterChip v-if="sp.filter" :filter="sp.filter" />
        </div>
      </button>
    </div>

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
            }}<span v-if="active.filter"> · {{ active.filter }}</span>
          </span>
          <button :class="btnGhost" @click="close">
            {{ t("stagePreviews.close") }}
          </button>
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
