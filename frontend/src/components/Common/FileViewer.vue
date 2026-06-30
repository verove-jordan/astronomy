<script setup lang="ts">
import {
  ref,
  computed,
  onMounted,
  onBeforeUnmount,
  watch,
  nextTick,
} from "vue";
import { useI18n } from "vue-i18n";
import { useBrowseStore } from "@/stores/browse";
import { useImageZoom } from "@/composables/useImageZoom";
import { useImageStretch } from "@/composables/useImageStretch";
import Spinner from "@/components/Common/Spinner.vue";
import IconX from "@/components/Icons/IconX.vue";
import IconZoomIn from "@/components/Icons/IconZoomIn.vue";
import IconZoomOut from "@/components/Icons/IconZoomOut.vue";
import IconFit from "@/components/Icons/IconFit.vue";
import IconOneToOne from "@/components/Icons/IconOneToOne.vue";
import IconReset from "@/components/Icons/IconReset.vue";

// Full-viewport viewer for one capture file: decodes via /api/preview (any format), renders to a
// canvas, and applies a Siril-style display stretch (black/white point + gamma) live, with pan/zoom.
const props = defineProps<{ path: string }>();
const emit = defineEmits<{ (e: "close"): void }>();
const { t } = useI18n();
const browseStore = useBrowseStore();

const container = ref<HTMLElement | null>(null);
const canvas = ref<HTMLCanvasElement | null>(null);
const loading = ref(true);
const error = ref("");

// Display stretch state (0–65535 domain, like Siril's bar). gamma 1 = linear.
const black = ref(0);
const white = ref(65535);
const gamma = ref(1);

const { image, setImage, render } = useImageStretch();
const {
  transform,
  transitionClass,
  zoomPercent,
  setNatural,
  fit,
  reset,
  zoomBy,
  actualSize,
  onPointerDown,
  onPointerMove,
  onPointerUp,
  onDblClick,
  onKey,
} = useImageZoom(container);

let ctx: CanvasRenderingContext2D | null = null;

function draw() {
  if (ctx) render(ctx, black.value, white.value, gamma.value);
}

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const p = await browseStore.loadPreview(props.path);
    setImage(p);
    await nextTick();
    const cv = canvas.value;
    if (!cv) return;
    cv.width = p.w;
    cv.height = p.h;
    ctx = cv.getContext("2d");
    // Open on a sensible, non-clipped stretch (white at the suggested high point).
    black.value = 0;
    white.value = p.autoHi || 65535;
    draw();
    setNatural(p.w, p.h);
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    loading.value = false;
  }
}

function applyAuto() {
  const p = image.value;
  if (!p) return;
  black.value = p.autoLo;
  white.value = p.autoHi;
}
function resetStretch() {
  black.value = 0;
  white.value = 65535;
  gamma.value = 1;
}
function toggleGamma() {
  gamma.value = gamma.value === 1 ? 2.2 : 1;
}

// Keep the handles ordered, then redraw whenever the stretch changes.
watch(black, (v) => {
  if (v >= white.value) black.value = Math.max(0, white.value - 1);
});
watch(white, (v) => {
  if (v <= black.value) white.value = Math.min(65535, black.value + 1);
});
watch([black, white, gamma], draw);

function onEsc(e: KeyboardEvent) {
  if (e.key === "Escape") emit("close");
}
onMounted(() => {
  window.addEventListener("keydown", onEsc);
  load();
});
onBeforeUnmount(() => window.removeEventListener("keydown", onEsc));
watch(() => props.path, load);

const filename = computed(() => props.path.split("/").pop() || props.path);
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 z-50 flex flex-col bg-slate-950/95 backdrop-blur-sm"
    >
      <!-- header: filename + zoom controls + close -->
      <header
        class="flex items-center justify-between gap-3 px-4 py-2 text-slate-200"
      >
        <span class="min-w-0 truncate text-sm" :title="path">{{
          filename
        }}</span>
        <div class="flex items-center gap-1">
          <button
            class="rounded p-1 hover:bg-slate-700"
            :aria-label="t('viewer.zoomOut')"
            :title="t('viewer.zoomOut')"
            @click="zoomBy(1 / 1.3)"
          >
            <IconZoomOut />
          </button>
          <span class="w-12 text-center text-xs tabular-nums"
            >{{ zoomPercent }}%</span
          >
          <button
            class="rounded p-1 hover:bg-slate-700"
            :aria-label="t('viewer.zoomIn')"
            :title="t('viewer.zoomIn')"
            @click="zoomBy(1.3)"
          >
            <IconZoomIn />
          </button>
          <button
            class="rounded p-1 hover:bg-slate-700"
            :aria-label="t('viewer.fit')"
            :title="t('viewer.fit')"
            @click="fit()"
          >
            <IconFit />
          </button>
          <button
            class="rounded p-1 hover:bg-slate-700"
            :aria-label="t('viewer.actualSize')"
            :title="t('viewer.actualSize')"
            @click="actualSize()"
          >
            <IconOneToOne />
          </button>
          <button
            class="rounded p-1 hover:bg-slate-700"
            :aria-label="t('viewer.reset')"
            :title="t('viewer.reset')"
            @click="reset()"
          >
            <IconReset />
          </button>
          <button
            class="ml-2 rounded p-1 hover:bg-slate-700"
            :aria-label="t('common.close')"
            :title="t('common.close')"
            @click="emit('close')"
          >
            <IconX />
          </button>
        </div>
      </header>

      <!-- canvas viewport (pan/zoom) -->
      <div
        ref="container"
        tabindex="0"
        class="relative flex-1 cursor-grab touch-none overflow-hidden bg-black outline-none focus-visible:ring-2 focus-visible:ring-brand-500"
        @pointerdown="onPointerDown"
        @pointermove="onPointerMove"
        @pointerup="onPointerUp"
        @pointerleave="onPointerUp"
        @dblclick="onDblClick"
        @keydown="onKey"
      >
        <canvas
          ref="canvas"
          class="absolute left-0 top-0 max-w-none select-none will-change-transform"
          :class="transitionClass"
          :style="{ transform, transformOrigin: '0 0' }"
        />
        <div
          v-if="loading"
          class="absolute inset-0 flex items-center justify-center"
        >
          <Spinner>{{ t("common.loading") }}</Spinner>
        </div>
        <div
          v-else-if="error"
          class="absolute inset-0 flex items-center justify-center px-6 text-center text-sm text-danger"
        >
          {{ error }}
        </div>
      </div>

      <!-- display-stretch controls -->
      <footer
        class="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3 text-slate-200"
      >
        <label class="flex min-w-[14rem] grow items-center gap-2 text-xs">
          <span class="w-10 shrink-0 text-slate-400">{{
            t("viewer.black")
          }}</span>
          <input
            v-model.number="black"
            type="range"
            min="0"
            max="65535"
            class="grow accent-brand-500"
          />
          <span class="w-12 shrink-0 text-right tabular-nums">{{ black }}</span>
        </label>
        <label class="flex min-w-[14rem] grow items-center gap-2 text-xs">
          <span class="w-10 shrink-0 text-slate-400">{{
            t("viewer.white")
          }}</span>
          <input
            v-model.number="white"
            type="range"
            min="0"
            max="65535"
            class="grow accent-brand-500"
          />
          <span class="w-12 shrink-0 text-right tabular-nums">{{ white }}</span>
        </label>
        <div class="flex items-center gap-2">
          <button
            class="rounded bg-slate-800 px-2 py-1 text-xs hover:bg-slate-700"
            @click="applyAuto"
          >
            {{ t("viewer.auto") }}
          </button>
          <button
            class="rounded px-2 py-1 text-xs"
            :class="
              gamma === 1
                ? 'bg-slate-800 hover:bg-slate-700'
                : 'bg-brand-600 hover:bg-brand-500'
            "
            :title="t('viewer.gammaHint')"
            @click="toggleGamma"
          >
            {{ t("viewer.gamma") }}
          </button>
          <button
            class="rounded bg-slate-800 px-2 py-1 text-xs hover:bg-slate-700"
            @click="resetStretch"
          >
            {{ t("viewer.stretchReset") }}
          </button>
        </div>
      </footer>
    </div>
  </Teleport>
</template>
