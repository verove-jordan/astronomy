<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { card } from "@/constants/styles";
import type { StagePreview } from "@/types";

// Live per-panel progress board for a tiled-mosaic run: one row per panel with its stack → solve →
// assemble stage chips, derived from the streamed stage previews' `tile` tags (the mosaic pipeline
// labels per-panel milestones with the panel folder). Panels come from the referenced plan when
// known, else from the tags seen so far. Same chip model as ChannelSessionProgress.
const props = defineProps<{
  expectedFolders?: string[]; // plan tiles in capture order; absent → derive from the stream
  previews: StagePreview[];
  currentFolder?: string;
}>();
const { t } = useI18n();

const STAGES = ["stacked", "solved", "assembled"] as const;

// The engine tags per-panel milestones with the panel folder in the SESSION dimension (panels ride
// the same per-session preview machinery as capture nights); `tile` is accepted too.
function panelOf(p: StagePreview): string {
  return p.tile ?? (p.session?.startsWith("p") ? p.session : "");
}

const folders = computed<string[]>(() => {
  if (props.expectedFolders?.length) return props.expectedFolders;
  const seen = new Set<string>();
  for (const p of props.previews) {
    const f = panelOf(p);
    if (f) seen.add(f);
  }
  return [...seen].sort();
});

function has(stage: string, folder: string): boolean {
  return props.previews.some((p) => panelOf(p) === folder && p.stage === stage);
}
// The run-level assembled canvases imply every panel made it through.
const anyAssembled = computed(() =>
  props.previews.some((p) => p.stage === "aligned" && !panelOf(p)),
);
function done(stage: (typeof STAGES)[number], folder: string): boolean {
  switch (stage) {
    case "stacked":
      return (
        has("stacked", folder) || has("solved", folder) || anyAssembled.value
      );
    case "solved":
      // The engine emits a per-panel solve milestone; a finished assembly implies every panel
      // solved, since an unsolvable panel is dropped before the canvas is built.
      return has("solved", folder) || anyAssembled.value;
    case "assembled":
      return anyAssembled.value;
  }
}

const chipBase =
  "rounded px-1.5 py-0.5 text-[10px] font-medium transition-colors";
function chipClass(isDone: boolean, active: boolean): string {
  if (isDone) return `${chipBase} bg-success/10 text-success`;
  if (active) return `${chipBase} animate-pulse bg-brand-500/15 text-brand-500`;
  return `${chipBase} bg-slate-500/10 text-slate-400 dark:text-slate-500`;
}
</script>

<template>
  <section v-if="folders.length" :class="card">
    <h2 class="text-lg font-medium">{{ t("mosaic.job.panels") }}</h2>
    <p class="mb-3 text-sm text-slate-500 dark:text-slate-400">
      {{ t("mosaic.job.panelsHint") }}
    </p>
    <div class="space-y-1">
      <div
        v-for="folder in folders"
        :key="folder"
        class="flex flex-wrap items-center gap-2 text-sm"
      >
        <span
          class="w-14 shrink-0 font-mono text-xs"
          :class="
            currentFolder === folder
              ? 'font-semibold text-brand-500'
              : 'text-slate-500 dark:text-slate-400'
          "
          >{{ folder }}</span
        >
        <span
          v-for="stage in STAGES"
          :key="stage"
          :class="
            chipClass(
              done(stage, folder),
              currentFolder === folder && !done(stage, folder),
            )
          "
          >{{ t(`mosaic.job.stage_${stage}`) }}</span
        >
      </div>
    </div>
  </section>
</template>
