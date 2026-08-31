<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

import IconFullscreen from "@/components/Icons/IconFullscreen.vue";
import IconReset from "@/components/Icons/IconReset.vue";
import { useSolarSystem3D } from "@/composables/useSolarSystem3D";
import type { SolarBody, SolarManifest } from "@/types";

// The canvas, its labels and the handful of controls that belong on top of it. Everything else —
// the time machine, the readout, the layer switches — lives on the page around it.

const props = defineProps<{
  manifest: SolarManifest | null;
  timeMs: number;
  warp: number;
  moonScale: number;
  exaggerate: number;
  showOrbits: boolean;
  showAxes: boolean;
  showStars: boolean;
  showLabels: boolean;
  follow: string | null;
  selected: string | null;
}>();

const emit = defineEmits<{
  (e: "update:selected", key: string | null): void;
  (e: "update:follow", key: string | null): void;
  (e: "hover", key: string | null): void;
}>();

const { t } = useI18n();

const canvas = ref<HTMLCanvasElement | null>(null);
const root = ref<HTMLElement | null>(null);
const isFullscreen = ref(false);

// The composable drives refs, so mirror the props into refs it can watch. Writes to `selected` and
// `follow` go back out as events, keeping the page the single owner of that state.
const manifestRef = computed(() => props.manifest);
const timeRef = computed(() => props.timeMs);
const warpRef = computed(() => props.warp);
const moonScaleRef = computed(() => props.moonScale);
const exaggerateRef = computed(() => props.exaggerate);
const showOrbitsRef = computed(() => props.showOrbits);
const showAxesRef = computed(() => props.showAxes);
const showStarsRef = computed(() => props.showStars);
const showLabelsRef = computed(() => props.showLabels);

const selectedProxy = computed({
  get: () => props.selected,
  set: (v: string | null) => emit("update:selected", v),
});
const followProxy = computed({
  get: () => props.follow,
  set: (v: string | null) => emit("update:follow", v),
});

const scene = useSolarSystem3D(canvas, {
  manifest: manifestRef,
  timeMs: timeRef,
  warp: warpRef,
  moonScale: moonScaleRef,
  exaggerate: exaggerateRef,
  showOrbits: showOrbitsRef,
  showAxes: showAxesRef,
  showStars: showStarsRef,
  showLabels: showLabelsRef,
  follow: followProxy,
  selected: selectedProxy,
});

watch(scene.hovered, (v) => emit("hover", v));

const byKey = computed(() => {
  const m = new Map<string, SolarBody>();
  for (const b of props.manifest?.bodies ?? []) m.set(b.key, b);
  return m;
});

/**
 * Which labels to draw. Everything at once is unreadable when a satellite system collapses to a few
 * pixels, so a label is dropped when a more important one is already sitting on top of it — the Sun
 * and the planets outrank the moons, and anything selected outranks all of them.
 */
const labels = computed(() => {
  if (!props.showLabels) return [];
  const rank = (key: string) => {
    if (key === props.selected || key === scene.hovered.value) return 0;
    const kind = byKey.value.get(key)?.kind;
    return kind === "star"
      ? 1
      : kind === "planet"
        ? 2
        : kind === "dwarf"
          ? 3
          : 4;
  };
  const candidates = scene.drawn.value
    .filter((d) => d.screen && d.screen[0] > -80 && d.screen[1] > -20)
    .sort((a, b) => rank(a.key) - rank(b.key));

  const kept: typeof candidates = [];
  for (const c of candidates) {
    const [x, y] = c.screen!;
    const clash = kept.some(
      (k) => Math.abs(k.screen![0] - x) < 62 && Math.abs(k.screen![1] - y) < 15,
    );
    if (!clash) kept.push(c);
  }
  return kept;
});

function labelStyle(d: { screen: [number, number] | null; radiusPx: number }) {
  const [x, y] = d.screen!;
  return {
    transform: `translate(${Math.round(x + Math.max(6, d.radiusPx + 4))}px, ${Math.round(y - 8)}px)`,
  };
}

function onLabelClick(key: string) {
  emit("update:selected", key);
  scene.frameBody(key);
}

async function toggleFullscreen() {
  if (!root.value) return;
  if (document.fullscreenElement) await document.exitFullscreen();
  else await root.value.requestFullscreen();
}

function syncFullscreen() {
  isFullscreen.value = !!document.fullscreenElement;
}
document.addEventListener("fullscreenchange", syncFullscreen);
onBeforeUnmount(() =>
  document.removeEventListener("fullscreenchange", syncFullscreen),
);

defineExpose({ frameBody: scene.frameBody, home: scene.home });
</script>

<template>
  <div
    ref="root"
    class="relative overflow-hidden rounded-lg bg-[#02030a]"
    data-demo="solar-canvas"
  >
    <canvas
      ref="canvas"
      class="block h-[26rem] w-full touch-none outline-none sm:h-[34rem]"
      :class="isFullscreen ? 'h-screen sm:h-screen' : ''"
      :aria-label="t('solarsystem.view.canvasLabel')"
      @contextmenu.prevent
    />

    <p
      v-if="!scene.supported.value"
      class="absolute inset-0 grid place-items-center p-6 text-center text-sm text-slate-300"
    >
      {{ t("solarsystem.noWebgl") }}
    </p>

    <!-- Labels are HTML rather than drawn in the canvas: they stay crisp at any pixel ratio, they
         are selectable, and a screen reader can find them. -->
    <div
      v-if="scene.supported.value"
      class="pointer-events-none absolute inset-0"
      aria-hidden="false"
    >
      <button
        v-for="d in labels"
        :key="d.key"
        type="button"
        class="pointer-events-auto absolute left-0 top-0 whitespace-nowrap rounded px-1 text-xs leading-4 transition-colors"
        :class="
          d.key === selected
            ? 'bg-brand-600/80 text-white'
            : 'text-slate-300 hover:bg-slate-800/80 hover:text-white'
        "
        :style="labelStyle(d)"
        @click="onLabelClick(d.key)"
      >
        {{ t(`solarsystem.bodies.${d.key}`) }}
      </button>
    </div>

    <div class="absolute right-2 top-2 z-10 flex gap-1">
      <button
        type="button"
        class="rounded-md bg-slate-900/80 p-2 text-slate-200 backdrop-blur hover:bg-slate-700"
        :title="t('solarsystem.view.reset')"
        :aria-label="t('solarsystem.view.reset')"
        @click="scene.home()"
      >
        <IconReset />
      </button>
      <button
        type="button"
        class="rounded-md bg-slate-900/80 p-2 text-slate-200 backdrop-blur hover:bg-slate-700"
        :title="t('solarsystem.view.fullscreen')"
        :aria-label="t('solarsystem.view.fullscreen')"
        @click="toggleFullscreen"
      >
        <IconFullscreen />
      </button>
    </div>

    <p
      class="pointer-events-none absolute bottom-2 right-3 text-[11px] text-slate-500"
    >
      {{ t("solarsystem.view.navHint") }}
    </p>

    <p
      v-if="follow"
      class="pointer-events-none absolute bottom-2 left-3 text-[11px] text-brand-300"
    >
      {{
        t("solarsystem.view.following", {
          body: t(`solarsystem.bodies.${follow}`),
        })
      }}
    </p>
  </div>
</template>
