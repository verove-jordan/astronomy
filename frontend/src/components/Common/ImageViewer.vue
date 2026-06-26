<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { useImageZoom } from "@/composables/useImageZoom";
import { NAV_RECT } from "@/constants/colors";
import Spinner from "@/components/Common/Spinner.vue";
import IconZoomIn from "@/components/Icons/IconZoomIn.vue";
import IconZoomOut from "@/components/Icons/IconZoomOut.vue";
import IconFit from "@/components/Icons/IconFit.vue";
import IconReset from "@/components/Icons/IconReset.vue";
import IconOneToOne from "@/components/Icons/IconOneToOne.vue";

defineProps<{ src: string; alt?: string; heightClass?: string }>();
const { t } = useI18n();

const container = ref<HTMLElement | null>(null);
const loaded = ref(false);
const {
  transform,
  transitionClass,
  zoomPercent,
  canZoom,
  viewport,
  setNatural,
  fit,
  reset,
  zoomBy,
  actualSize,
  centerOnNorm,
  onPointerDown,
  onPointerMove,
  onPointerUp,
  onDblClick,
  onKey,
} = useImageZoom(container);

function onLoad(e: Event) {
  const img = e.target as HTMLImageElement;
  setNatural(img.naturalWidth, img.naturalHeight);
  loaded.value = true;
}

const nav = ref<HTMLElement | null>(null);
let navDragging = false;
function navTo(e: MouseEvent) {
  const rect = nav.value?.getBoundingClientRect();
  if (!rect) return;
  centerOnNorm(
    (e.clientX - rect.left) / rect.width,
    (e.clientY - rect.top) / rect.height,
  );
}
function navDown(e: MouseEvent) {
  navDragging = true;
  navTo(e);
}
function navMove(e: MouseEvent) {
  if (navDragging) navTo(e);
}
function navUp() {
  navDragging = false;
}
</script>

<template>
  <div
    ref="container"
    tabindex="0"
    :class="[
      'relative w-full min-w-0 cursor-grab touch-none overflow-hidden rounded-md border border-slate-200 bg-slate-950 outline-none focus-visible:ring-2 focus-visible:ring-brand-500 dark:border-slate-700',
      heightClass || 'h-[32rem]',
    ]"
    @pointerdown="onPointerDown"
    @pointermove="onPointerMove"
    @pointerup="onPointerUp"
    @pointerleave="onPointerUp"
    @dblclick="onDblClick"
    @keydown="onKey"
  >
    <img
      :src="src"
      :alt="alt || ''"
      draggable="false"
      class="absolute left-0 top-0 max-w-none select-none will-change-transform"
      :class="transitionClass"
      :style="{ transform, transformOrigin: '0 0' }"
      @load="onLoad"
    />
    <div
      v-if="!loaded"
      class="absolute inset-0 flex items-center justify-center"
    >
      <Spinner>{{ t("common.loading") }}</Spinner>
    </div>

    <!-- toolbar -->
    <div
      class="absolute right-2 top-2 flex items-center gap-1 rounded-md bg-slate-900/80 p-1 text-slate-200 backdrop-blur"
    >
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
    </div>

    <!-- navigator minimap (keyboard/toolbar are the accessible controls) -->
    <div
      v-if="loaded && canZoom"
      ref="nav"
      aria-hidden="true"
      :title="t('viewer.navigator')"
      class="absolute bottom-2 right-2 h-24 w-32 cursor-pointer overflow-hidden rounded border border-white/30 bg-slate-900/70"
      @mousedown="navDown"
      @mousemove="navMove"
      @mouseup="navUp"
      @mouseleave="navUp"
    >
      <img :src="src" alt="" class="h-full w-full object-contain opacity-80" />
      <div
        class="absolute border-2"
        :style="{
          left: viewport.x * 100 + '%',
          top: viewport.y * 100 + '%',
          width: viewport.w * 100 + '%',
          height: viewport.h * 100 + '%',
          borderColor: NAV_RECT,
          backgroundColor: NAV_RECT + '33',
        }"
      />
    </div>
  </div>
</template>
