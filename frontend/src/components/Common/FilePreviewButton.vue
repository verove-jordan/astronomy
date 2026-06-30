<script setup lang="ts">
import { ref, nextTick, onBeforeUnmount } from "vue";
import { useI18n } from "vue-i18n";
import { useBrowseStore } from "@/stores/browse";
import { useViewerStore } from "@/stores/viewer";
import { useImageStretch } from "@/composables/useImageStretch";
import IconEye from "@/components/Icons/IconEye.vue";
import Spinner from "@/components/Common/Spinner.vue";
import { baseName } from "@/utils/format";
import type { PreviewImage } from "@/types";

// An eye button for one capture file: hovering it shows a small auto-stretched thumbnail (decoded
// once via /api/preview at a small size and cached); clicking it opens the full file viewer modal.
const props = defineProps<{ path: string }>();
const { t } = useI18n();
const browseStore = useBrowseStore();
const viewer = useViewerStore();
const { setImage, render } = useImageStretch();

const THUMB_MAX = 400; // longest-edge px the server downsamples to for the hover thumbnail
const BOX = 320; // popover image box, px

const show = ref(false);
const loading = ref(false);
const failed = ref(false);
const popX = ref(0);
const popY = ref(0);
const thumb = ref<HTMLCanvasElement | null>(null);
let cached: PreviewImage | null = null;
let hoverTimer: ReturnType<typeof setTimeout> | undefined;

// place positions the popover beside the button (left if it fits, else right), clamped to the viewport.
function place(rect: DOMRect) {
  let x = rect.left - BOX - 16;
  if (x < 8) x = rect.right + 12;
  let y = rect.top + rect.height / 2 - BOX / 2;
  y = Math.max(8, Math.min(y, window.innerHeight - BOX - 8));
  popX.value = x;
  popY.value = y;
}

async function ensureLoaded() {
  if (cached) return;
  loading.value = true;
  failed.value = false;
  try {
    cached = await browseStore.loadPreview(props.path, THUMB_MAX);
    setImage(cached);
  } catch {
    failed.value = true;
  } finally {
    loading.value = false;
  }
}

async function draw() {
  await nextTick();
  const cv = thumb.value;
  if (!cv || !cached) return;
  cv.width = cached.w;
  cv.height = cached.h;
  const ctx = cv.getContext("2d");
  if (ctx) render(ctx, 0, cached.autoHi || 65535, 1); // open on the suggested auto-stretch
}

function onEnter(e: Event) {
  const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
  hoverTimer = setTimeout(async () => {
    place(rect);
    show.value = true;
    await ensureLoaded();
    if (show.value && !failed.value) await draw();
  }, 140); // small delay so skating across rows doesn't fire a load
}
function onLeave() {
  if (hoverTimer) {
    clearTimeout(hoverTimer);
    hoverTimer = undefined;
  }
  show.value = false;
}
function onClick() {
  onLeave();
  viewer.open(props.path);
}

onBeforeUnmount(() => {
  if (hoverTimer) clearTimeout(hoverTimer);
});
</script>

<template>
  <button
    type="button"
    class="inline-flex items-center rounded-md px-2 py-1 text-brand-600 transition-colors hover:bg-brand-50 dark:text-brand-300 dark:hover:bg-brand-900/30"
    :aria-label="t('import.view')"
    :title="t('import.view')"
    @mouseenter="onEnter"
    @mouseleave="onLeave"
    @focus="onEnter"
    @blur="onLeave"
    @click="onClick"
  >
    <IconEye />
  </button>

  <Teleport to="body">
    <div
      v-if="show"
      class="pointer-events-none fixed z-[60] rounded-lg border border-slate-700 bg-surface-raised p-2 shadow-2xl"
      :style="{ left: popX + 'px', top: popY + 'px' }"
    >
      <div
        class="flex items-center justify-center overflow-hidden rounded bg-black"
        :style="{ width: BOX + 'px', height: BOX + 'px' }"
      >
        <Spinner v-if="loading" />
        <span v-else-if="failed" class="text-xs text-slate-400">—</span>
        <canvas
          v-else
          ref="thumb"
          class="max-h-full max-w-full object-contain"
        />
      </div>
      <div
        class="mt-1 max-w-[320px] truncate text-center text-xs text-slate-400"
      >
        {{ baseName(path) }}
      </div>
    </div>
  </Teleport>
</template>
