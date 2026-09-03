<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { BASE, fileUrl } from "@/services/api";
import {
  GALAXY_VERSION,
  decodeGalaxyCloud,
  type GalaxyCloud,
} from "@/utils/galaxycloud";
import {
  btnPrimary,
  checkbox,
  segActive,
  segBtn,
  segIdle,
  segWrap,
} from "@/constants/styles";
import {
  GALAXY_DEFAULT_GAIN,
  useStarField3D,
} from "@/composables/useStarField3D";
import { useFlyControls } from "@/composables/useFlyControls";
import {
  DEPTH_MEASURED,
  decodeScene,
  formatDistance,
  type Scene3DPoints,
  type StarRecord,
} from "@/utils/scene3d";
import { LY_PER_PARSEC, compact } from "@/utils/starInfo";
import type { Scene3DManifest, StarAnnotations } from "@/types";
import Spinner from "@/components/Common/Spinner.vue";
import StarInfoCard from "@/components/Common/StarInfoCard.vue";
import IconReset from "@/components/Icons/IconReset.vue";
import IconFullscreen from "@/components/Icons/IconFullscreen.vue";
import { frameSpanPc } from "@/utils/scene3dgalaxy";

// The run's photograph as a volume. The engine did every bit of the astronomy and cached the result
// beside the run; this component fetches one small manifest plus one binary buffer, hands the buffer
// to the GPU untouched, and draws a handful of calls a frame.

const props = defineProps<{
  manifest: Scene3DManifest;
  // The run's star annotation, already in the store. A hovered star reads its full catalogue row
  // straight out of this by index, so nothing about a star is duplicated into the binary.
  stars?: StarAnnotations | null;
  // True while the parent is re-running the star annotation, which is what rebuilds a scene whose
  // cached annotation is too old to derive one from.
  rebuilding?: boolean;
  heightClass?: string;
}>();
const emit = defineEmits<{ recompute: [] }>();
const { t, locale } = useI18n();

const root = ref<HTMLElement | null>(null);
const canvas = ref<HTMLCanvasElement | null>(null);
const points = ref<Scene3DPoints | null>(null);
const loading = ref(false);
const loadError = ref("");

// depth is the slider that opens the picture into the volume: 0 is the photograph itself, 1 is the
// full logarithmic cone.
const depth = ref(0);
const showStars = ref(true);
const showObjects = ref(true);
const showFrustum = ref(true);
// Estimated distances are drawn by default — they are most of a typical field — but one click hides
// them, leaving only the stars whose parallax was actually measured.
const showEstimated = ref(true);
const showMotion = ref(false);
const motionYears = ref(100000);
const starSize = ref(2.6);
// The galaxy view: off by default. `galaxyZoom` is the journey — 0 is standing inside the
// photograph, 1 is looking at the whole Milky Way, and everything in between is true scale.
const showGalaxy = ref(false);
const galaxyZoom = ref(0);
// The Milky Way point cloud, fetched the first time the layer is switched on, and how brightly it is
// exposed — the one thing about the cloud that only an eye can settle.
const galaxyCloud = ref<GalaxyCloud | null>(null);
const galaxyGain = ref(GALAXY_DEFAULT_GAIN);
let galaxyLoading = false;
// The image's sky anchors, which carry the field's ROLL. Without them the Galaxy cannot be oriented
// and must not be drawn; the composable checks them against the manifest's own camera as well.
const frame = computed(() => props.stars?.solve?.frame ?? null);

const backdropUrl = computed(() =>
  props.manifest.backdrop ? fileUrl(props.manifest.backdrop) : "",
);

const {
  supported,
  error,
  orbit,
  galaxyAvailable,
  journeyCtx,
  selected,
  hovered,
  resetView,
  openView,
  requestDraw,
  onPointerDown,
  onPointerMove,
  onPointerUp,
  onPointerLeave,
  onWheel,
} = useStarField3D(canvas, {
  manifest: computed(() => props.manifest),
  points,
  backdropUrl,
  depth,
  showStars,
  showObjects,
  showFrustum,
  showEstimated,
  showMotion,
  motionYears,
  starSize,
  showGalaxy,
  galaxyZoom,
  galaxyCloud,
  galaxyGain,
  frame,
});

// How much sky is in frame at the current point of the journey — the readout beside the slider. A
// percentage would say nothing; "17 pc", then "40 kpc", then "9 Mpc" is the whole story of what the
// slider does. Read off the renderer's own journey so the number cannot describe a different one.
const galaxySpanLabel = computed(() => {
  const ctx = journeyCtx.value;
  if (!ctx) return "";
  return formatDistance(
    frameSpanPc(galaxyZoom.value, ctx, props.manifest.camera.tan_half_w),
  );
});

// Exploration mode. The keyboard flies and the mouse looks; orbiting stays exactly as it was when
// the mode is off, so nothing anybody already knows how to do changes.
const fly = useFlyControls({
  orbit,
  requestDraw,
  canvas,
  tanHalfH: computed(() => props.manifest.camera?.tan_half_h ?? 0),
});

// While flying, a drag turns the head instead of swinging the camera around a point. The orbit
// handlers are bypassed rather than modified — with the mode off, every gesture takes the original
// path, byte for byte.
let lookFrom: { x: number; y: number } | null = null;

// Which drags mean "turn my head" as opposed to "swing the camera around the field".
//
// Both are needed and they are not the same question. Looking answers "what else is near me"; orbiting
// answers "what does this look like from the side", which is the one thing a first-person view can
// never show you — from inside, a cloud of stars looks much the same in every direction. So the plain
// drag looks, and orbit keeps the gesture it has always had: the right button, plus Alt with the left
// for trackpads and one-button mice. Shift and the middle button still pan.
function isLookDrag(e: PointerEvent) {
  return (
    fly.flying.value &&
    !fly.pointerLocked.value &&
    e.button === 0 &&
    !e.shiftKey &&
    !e.altKey
  );
}

function handlePointerDown(e: PointerEvent) {
  if (isLookDrag(e)) {
    lookFrom = { x: e.clientX, y: e.clientY };
    (e.target as Element).setPointerCapture?.(e.pointerId);
    return;
  }
  onPointerDown(e);
}

function handlePointerMove(e: PointerEvent) {
  if (lookFrom) {
    fly.applyLook(e.clientX - lookFrom.x, e.clientY - lookFrom.y);
    lookFrom = { x: e.clientX, y: e.clientY };
    return;
  }
  onPointerMove(e);
}

function handlePointerUp(e: PointerEvent) {
  if (lookFrom) {
    lookFrom = null;
    return;
  }
  onPointerUp(e);
}

function handlePointerLeave() {
  lookFrom = null;
  onPointerLeave();
}

// loadPoints fetches the star field. It is deliberately not part of the manifest request: the
// manifest is a few kilobytes and decides whether the view exists at all, while this is the payload
// worth not downloading for someone who never opens the 3D tab.
async function loadPoints(path: string) {
  loading.value = true;
  loadError.value = "";
  try {
    const res = await fetch(fileUrl(path));
    if (!res.ok) throw new Error(String(res.status));
    points.value = decodeScene(await res.arrayBuffer());
  } catch (e) {
    loadError.value = (e as Error).message || t("scene3d.loadFailed");
    points.value = null;
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.manifest.points,
  (p) => {
    if (p) void loadPoints(p);
  },
  { immediate: true },
);

// loadGalaxyCloud fetches the Milky Way.
//
// Not part of the run at all: the cloud is the same Galaxy for every photograph ever taken, so it is
// served from its own endpoint with a week's cache lifetime and an ETag. First open of the first run
// pays for it; every run after that is a cache hit, and the per-run part is a 3×3 matrix.
//
// Deferred until the layer is switched on. Someone who never asks for the Galaxy never downloads it.
async function loadGalaxyCloud() {
  if (galaxyCloud.value || galaxyLoading) return;
  galaxyLoading = true;
  try {
    const res = await fetch(`${BASE}/api/galaxy/points?v=${GALAXY_VERSION}`);
    if (!res.ok) throw new Error(String(res.status));
    galaxyCloud.value = decodeGalaxyCloud(await res.arrayBuffer());
  } catch {
    // A missing Galaxy costs the layer, not the scene. The toggle stays on and the reference rings
    // still draw, so the failure is visible without being fatal.
    galaxyCloud.value = null;
  } finally {
    galaxyLoading = false;
  }
}

watch(showGalaxy, (on) => {
  if (on) void loadGalaxyCloud();
});

// --- fullscreen ------------------------------------------------------------------------------------

// The native Fullscreen API rather than a fixed overlay: it gives Escape, the browser's own
// affordances and the real screen bounds for free, and the canvas simply resizes into whatever it
// gets (the ResizeObserver already redraws, and the projection is fitted to the live aspect).
const isFullscreen = ref(false);

function syncFullscreen() {
  isFullscreen.value = document.fullscreenElement === root.value;
}

async function toggleFullscreen() {
  try {
    if (document.fullscreenElement) await document.exitFullscreen();
    else await root.value?.requestFullscreen();
  } catch {
    // Denied or unsupported — the view keeps working at its normal size, which is the whole fallback.
  }
}

onMounted(() => document.addEventListener("fullscreenchange", syncFullscreen));

onBeforeUnmount(() => {
  document.removeEventListener("fullscreenchange", syncFullscreen);
  points.value = null;
});

const nf = (n: number) => n.toLocaleString(locale.value);
const depthPercent = computed(() => Math.round(depth.value * 100));
const counts = computed(() => props.manifest.stars);
const photometric = computed(() => props.manifest.photometric);
const billboards = computed(() => props.manifest.billboards ?? []);

// holdoutWarning fires when the frame's own cross-validation says the estimated distances did not
// recover the measured ones. Shown rather than acted on: the user decides whether to keep drawing
// them, but never without being told.
const holdoutWarning = computed(() => {
  const p = photometric.value;
  if (!p.calibrated || !p.holdout_n) return "";
  const ratio = p.holdout_median_ratio ?? 1;
  const scatter = p.holdout_scatter_dex ?? 0;
  if (ratio > 1.6 || ratio < 0.62 || scatter > 0.45) {
    return t("scene3d.holdoutPoor", {
      ratio: ratio.toFixed(2),
      scatter: scatter.toFixed(2),
    });
  }
  return "";
});

// The panel previews whatever the pointer is over and falls back to whatever was last clicked, so
// moving the mouse away leaves the pinned star up to be read rather than blanking the panel — and
// nothing floats over the scene to be left behind.
const shown = computed<StarRecord | null>(
  () => hovered.value ?? selected.value,
);
const pinned = computed(() => !hovered.value && !!selected.value);

// catalogueRow is the hovered star's full annotation entry, found by the index the engine put in the
// record. That index is the whole reason the binary can stay 32 bytes wide and still answer
// "what is this?" — the answer already arrived with stars.json.
const catalogueRow = computed(() => {
  const s = shown.value;
  const list = props.stars?.stars;
  if (!s || !list) return null;
  return list[s.srcIndex]?.star ?? null;
});

const shownTitle = computed(() => {
  const s = shown.value;
  if (!s) return "";
  return catalogueRow.value?.name || s.name || t("scene3d.anonymousStar");
});

const shownDistance = computed(() => {
  const s = shown.value;
  if (!s) return "";
  return `${formatDistance(s.distPc)} · ${compact(s.distPc * LY_PER_PARSEC)} ${t("scene3d.ly")}`;
});

// The angular size a star actually subtends — the honest answer to "how big is it really". It is
// always a fraction of a milliarcsecond, which is the point: a star is a point source at any
// distance a telescope can see it from.
const shownAngularSize = computed(() => {
  const info = catalogueRow.value;
  const s = shown.value;
  if (!info || !s || !(s.distPc > 0)) return "";
  const absMag = info.absmag;
  const ci = info.ci;
  if (
    absMag === null ||
    absMag === undefined ||
    ci === null ||
    ci === undefined
  ) {
    return "";
  }
  const lum = Math.pow(10, (4.83 - absMag) / 2.5); // solar luminosities
  const a = 0.92 * ci;
  const teff = 4600 * (1 / (a + 1.7) + 1 / (a + 0.62));
  if (!(teff > 1000)) return "";
  // Stefan–Boltzmann: L ∝ R²T⁴, so R/R☉ = √L / (T/T☉)².
  const radiusSun = Math.sqrt(lum) / Math.pow(teff / 5772, 2);
  const SUN_RADIUS_PC = 2.2546e-8;
  const mas =
    ((2 * radiusSun * SUN_RADIUS_PC) / s.distPc) * (180 / Math.PI) * 3.6e6;
  return t("scene3d.angularSizeValue", {
    r: compact(radiusSun),
    mas: mas < 0.01 ? mas.toExponential(1) : compact(mas),
  });
});

const shownSpeed = computed(() => {
  const v = shown.value?.velocity;
  if (!v) return "";
  return `${Math.hypot(v[0], v[1], v[2]).toFixed(1)} km/s`;
});

const motionLabel = computed(() =>
  motionYears.value >= 1e6
    ? t("scene3d.motionMyr", { n: (motionYears.value / 1e6).toFixed(1) })
    : t("scene3d.motionKyr", { n: Math.round(motionYears.value / 1000) }),
);

// Objects whose shape is a real reconstruction rather than a flat card, with the honesty tier the
// engine assigned. The tier is never merged away: a measured inclination and a modelled nebula
// depth are not the same kind of statement.
const shapedObjects = computed(() =>
  billboards.value.filter((b) => b.shape && b.shape.kind !== "plane"),
);

const shapeTierClass: Record<string, string> = {
  measured: "text-emerald-500 dark:text-emerald-400",
  assumed: "text-sky-500 dark:text-sky-400",
  modelled: "text-amber-600 dark:text-amber-400",
};
</script>

<template>
  <div
    ref="root"
    class="flex flex-col gap-2"
    :class="isFullscreen ? 'h-full bg-slate-950 p-3' : ''"
  >
    <!-- A run can have stars and still have no scene — most often because its annotation predates
         the geometry the scene is built from. Saying so, with the one action that fixes it, beats
         hiding the view and leaving the absence unexplained. -->
    <div
      v-if="!manifest.available"
      class="flex flex-col items-center justify-center gap-3 rounded-lg border border-slate-200 bg-slate-50 p-8 text-center dark:border-slate-700 dark:bg-slate-900"
      :class="heightClass || 'h-[28rem]'"
    >
      <p class="text-sm text-slate-600 dark:text-slate-300">
        {{ manifest.reason || t("scene3d.unavailable") }}
      </p>
      <button
        v-if="manifest.needs_recompute"
        type="button"
        :class="btnPrimary"
        :disabled="rebuilding"
        @click="emit('recompute')"
      >
        {{ rebuilding ? t("scene3d.rebuilding") : t("scene3d.rebuild") }}
      </button>
      <p
        v-if="manifest.needs_recompute"
        class="max-w-md text-xs text-slate-500 dark:text-slate-400"
      >
        {{ t("scene3d.rebuildHint") }}
      </p>
    </div>

    <!-- The scene and its readout sit side by side: the panel is where a star's details go, so
         nothing ever floats over the field and nothing is left behind when the pointer moves off. -->
    <div
      v-else
      class="flex min-h-0 flex-col gap-2 sm:flex-row"
      :class="isFullscreen ? 'flex-1' : heightClass || 'h-[28rem]'"
    >
      <div
        class="relative min-w-0 flex-1 overflow-hidden rounded-lg bg-slate-950"
      >
        <!-- tabindex so the arrow keys reach the canvas once it has been clicked; without it the keys
           go to the page and scroll it instead of flying. -->
        <canvas
          ref="canvas"
          class="h-full w-full touch-none outline-none"
          :tabindex="supported ? 0 : undefined"
          :class="
            !supported
              ? ''
              : fly.pointerLocked.value
                ? 'cursor-none'
                : fly.flying.value
                  ? 'cursor-crosshair'
                  : 'cursor-grab active:cursor-grabbing'
          "
          @pointerdown="handlePointerDown"
          @pointermove="handlePointerMove"
          @pointerup="handlePointerUp"
          @pointercancel="handlePointerUp"
          @pointerleave="handlePointerLeave"
          @wheel="onWheel"
          @contextmenu.prevent
        />

        <div
          v-if="!supported"
          class="absolute inset-0 flex items-center justify-center p-6 text-center text-sm text-slate-300"
        >
          {{ error || t("scene3d.noWebgl") }}
        </div>
        <div
          v-else-if="loading"
          class="absolute inset-0 flex items-center justify-center"
          role="status"
        >
          <Spinner />
        </div>
        <p
          v-else-if="loadError"
          class="absolute inset-0 flex items-center justify-center p-6 text-center text-sm text-danger-400"
        >
          {{ t("scene3d.loadFailed") }}
        </p>

        <!-- View controls: the viewer's own toolbar owns the top-right, matching the 2D image viewer. -->
        <div class="absolute right-2 top-2 z-10 flex gap-1">
          <button
            v-if="supported"
            type="button"
            class="rounded-md px-2 py-1 text-xs font-medium backdrop-blur transition-colors"
            :class="
              fly.flying.value
                ? 'bg-brand-600 text-white hover:bg-brand-500'
                : 'bg-slate-900/80 text-slate-200 hover:bg-slate-700'
            "
            :aria-pressed="fly.flying.value"
            :title="t('scene3d.exploreHint')"
            @click="fly.toggle()"
          >
            {{
              fly.flying.value ? t("scene3d.exploreOn") : t("scene3d.explore")
            }}
          </button>
          <button
            type="button"
            class="rounded-md bg-slate-900/80 px-2 py-1 text-xs font-medium text-slate-200 backdrop-blur transition-colors hover:bg-slate-700"
            :title="t('scene3d.openViewHint')"
            @click="openView()"
          >
            {{ t("scene3d.openView") }}
          </button>
          <button
            type="button"
            class="rounded-md bg-slate-900/80 p-2 text-slate-200 backdrop-blur transition-colors hover:bg-slate-700"
            :aria-label="t('scene3d.resetView')"
            :title="t('scene3d.resetViewHint')"
            @click="resetView()"
          >
            <IconReset />
          </button>
          <button
            type="button"
            class="rounded-md bg-slate-900/80 p-2 text-slate-200 backdrop-blur transition-colors hover:bg-slate-700"
            :aria-label="
              isFullscreen
                ? t('scene3d.exitFullscreen')
                : t('scene3d.fullscreen')
            "
            :title="
              isFullscreen
                ? t('scene3d.exitFullscreen')
                : t('scene3d.fullscreen')
            "
            @click="toggleFullscreen()"
          >
            <IconFullscreen :exit="isFullscreen" />
          </button>
        </div>

        <!-- The controls, on screen, while the mode is on. An exploration mode you have to be told
           about in a tooltip is one nobody finds; the legend costs a corner and answers every
           question at once. -->
        <div
          v-if="fly.flying.value"
          class="pointer-events-none absolute bottom-2 left-2 z-10 rounded-md bg-slate-900/80 px-3 py-2 text-[11px] text-slate-300 backdrop-blur"
        >
          <div class="mb-1 flex items-center gap-2 font-medium text-brand-300">
            <span
              class="h-1.5 w-1.5 rounded-full"
              :class="fly.moving.value ? 'bg-brand-400' : 'bg-slate-500'"
            />
            {{ t("scene3d.exploreOn") }}
          </div>
          <dl class="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5">
            <dt class="font-mono text-slate-400">↑↓ · W S</dt>
            <dd>{{ t("scene3d.keys.forward") }}</dd>
            <dt class="font-mono text-slate-400">←→ · A D</dt>
            <dd>{{ t("scene3d.keys.strafe") }}</dd>
            <dt class="font-mono text-slate-400">Space · C</dt>
            <dd>{{ t("scene3d.keys.updown") }}</dd>
            <dt class="font-mono text-slate-400">Shift</dt>
            <dd>{{ t("scene3d.keys.boost") }}</dd>
            <dt class="font-mono text-slate-400">
              {{
                fly.pointerLocked.value
                  ? t("scene3d.keys.mouse")
                  : t("scene3d.keys.drag")
              }}
            </dt>
            <dd>{{ t("scene3d.keys.look") }}</dd>
            <dt class="font-mono text-slate-400">
              {{ t("scene3d.keys.rightDrag") }}
            </dt>
            <dd>{{ t("scene3d.keys.orbit") }}</dd>
            <dt class="font-mono text-slate-400">
              {{ t("scene3d.keys.shiftDrag") }}
            </dt>
            <dd>{{ t("scene3d.keys.pan") }}</dd>
            <dt class="font-mono text-slate-400">
              {{ t("scene3d.keys.wheel") }}
            </dt>
            <dd>{{ t("scene3d.keys.zoom") }}</dd>
            <dt class="font-mono text-slate-400">Esc</dt>
            <dd>{{ t("scene3d.keys.exit") }}</dd>
          </dl>
          <button
            type="button"
            class="pointer-events-auto mt-1.5 rounded bg-slate-700/80 px-1.5 py-0.5 text-[11px] text-slate-200 transition-colors hover:bg-slate-600"
            @click="fly.togglePointerLock()"
          >
            {{
              fly.pointerLocked.value
                ? t("scene3d.keys.locked")
                : t("scene3d.keys.lock")
            }}
          </button>
        </div>

        <p
          v-else
          class="pointer-events-none absolute bottom-2 right-2 z-10 rounded bg-slate-900/70 px-2 py-1 text-[11px] text-slate-400 backdrop-blur"
        >
          {{ t("scene3d.navHint") }}
        </p>
      </div>

      <!-- Readout panel. Hovering previews a star here; clicking pins it so it can be read while the
           pointer goes elsewhere, and the pin is shown as such rather than looking like a tooltip
           that failed to close. -->
      <!-- Below the small breakpoint it drops under the scene as a short scrollable strip rather
           than stealing width the canvas has none of. -->
      <aside
        class="max-h-44 shrink-0 overflow-y-auto rounded-lg border border-slate-200 bg-white p-3 text-xs sm:max-h-none sm:w-64 dark:border-slate-700 dark:bg-slate-900"
        :class="isFullscreen ? 'sm:w-80' : ''"
      >
        <template v-if="shown">
          <div class="mb-1.5 flex items-center justify-between gap-2">
            <span
              class="rounded px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide"
              :class="
                pinned
                  ? 'bg-brand-100 text-brand-700 dark:bg-brand-900/40 dark:text-brand-200'
                  : 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400'
              "
            >
              {{ pinned ? t("scene3d.pinned") : t("scene3d.hovering") }}
            </span>
            <button
              v-if="selected"
              type="button"
              class="text-slate-400 transition-colors hover:text-slate-600 dark:hover:text-slate-200"
              :title="t('scene3d.unpin')"
              @click="selected = null"
            >
              ✕
            </button>
          </div>
          <StarInfoCard
            :info="catalogueRow"
            :title="shownTitle"
            :mag-estimate="shown.mag"
            title-class="text-slate-800 dark:text-slate-100"
          >
            <template #trail>
              <template v-if="!catalogueRow && shownDistance">
                <dt class="text-slate-500">{{ t("stars.info.distance") }}</dt>
                <dd>{{ shownDistance }}</dd>
              </template>
              <dt class="text-slate-500">{{ t("scene3d.depthSource") }}</dt>
              <dd
                :class="
                  shown.depth === DEPTH_MEASURED
                    ? 'text-emerald-600 dark:text-emerald-400'
                    : 'text-amber-600 dark:text-amber-400'
                "
              >
                {{
                  shown.depth === DEPTH_MEASURED
                    ? t("scene3d.measured")
                    : t("scene3d.estimated")
                }}
              </dd>
              <template v-if="shownAngularSize">
                <dt class="text-slate-500">{{ t("scene3d.angularSize") }}</dt>
                <dd>{{ shownAngularSize }}</dd>
              </template>
              <template v-if="shownSpeed">
                <dt class="text-slate-500">{{ t("scene3d.speed") }}</dt>
                <dd>{{ shownSpeed }}</dd>
              </template>
            </template>
            <p
              v-if="shown.clusterMember"
              class="mt-1 text-brand-600 dark:text-brand-400"
            >
              {{ t("scene3d.clusterMember") }}
            </p>
          </StarInfoCard>
        </template>
        <p v-else class="text-slate-500 dark:text-slate-400">
          {{ t("scene3d.panelEmpty") }}
        </p>
      </aside>
    </div>

    <!-- Depth slider: the whole interaction. At 0 the scene IS the photograph. -->
    <div
      v-if="manifest.available"
      class="flex flex-wrap items-center gap-3 text-sm"
    >
      <label
        class="flex flex-1 items-center gap-2"
        :title="t('scene3d.depthHint')"
      >
        <span class="shrink-0 text-slate-600 dark:text-slate-300">
          {{ t("scene3d.depth") }}
        </span>
        <input
          v-model.number="depth"
          type="range"
          min="0"
          max="1"
          step="0.01"
          class="min-w-[8rem] flex-1 accent-brand-600"
        />
        <span
          class="w-10 shrink-0 tabular-nums text-slate-500 dark:text-slate-400"
        >
          {{ depthPercent }}%
        </span>
      </label>

      <label class="flex items-center gap-2" :title="t('scene3d.starSizeHint')">
        <span class="text-slate-600 dark:text-slate-300">
          {{ t("scene3d.starSize") }}
        </span>
        <input
          v-model.number="starSize"
          type="range"
          min="0.8"
          max="8"
          step="0.1"
          class="w-24 accent-brand-600"
        />
      </label>
    </div>

    <!-- Layers, the estimated-distance switch, and motion. -->
    <div
      v-if="manifest.available"
      class="flex flex-wrap items-center gap-4 text-sm"
    >
      <span :class="segWrap">
        <button
          type="button"
          :class="[segBtn, showStars ? segActive : segIdle]"
          @click="showStars = !showStars"
        >
          {{ t("scene3d.layerStars") }}
        </button>
        <button
          type="button"
          :class="[segBtn, showObjects ? segActive : segIdle]"
          :disabled="!billboards.length"
          @click="showObjects = !showObjects"
        >
          {{ t("scene3d.layerObjects") }}
        </button>
        <button
          type="button"
          :class="[segBtn, showFrustum ? segActive : segIdle]"
          @click="showFrustum = !showFrustum"
        >
          {{ t("scene3d.layerField") }}
        </button>
      </span>

      <label
        class="flex items-center gap-2 text-slate-600 dark:text-slate-300"
        :title="t('scene3d.estimatedHint')"
      >
        <input v-model="showEstimated" type="checkbox" :class="checkbox" />
        {{ t("scene3d.showEstimated") }}
      </label>

      <label
        v-if="counts.moving"
        class="flex items-center gap-2 text-slate-600 dark:text-slate-300"
        :title="t('scene3d.motionHint')"
      >
        <input v-model="showMotion" type="checkbox" :class="checkbox" />
        {{ t("scene3d.motion", { n: nf(counts.moving) }) }}
      </label>
      <label v-if="showMotion" class="flex items-center gap-2">
        <input
          v-model.number="motionYears"
          type="range"
          min="10000"
          max="2000000"
          step="10000"
          class="w-32 accent-brand-600"
        />
        <span class="tabular-nums text-slate-500 dark:text-slate-400">
          {{ motionLabel }}
        </span>
      </label>

      <!-- The Milky Way, off by default. Disabled rather than hidden when the field's orientation is
           unknown: "this run cannot do it" is a more useful answer than a control that is not there,
           and guessing a roll would draw a plausible, wrong Galaxy. -->
      <label
        class="flex items-center gap-2 text-slate-600 dark:text-slate-300"
        :class="galaxyAvailable ? '' : 'opacity-50'"
        :title="
          galaxyAvailable
            ? t('scene3d.galaxyHint')
            : t('scene3d.galaxyUnavailable')
        "
      >
        <input
          v-model="showGalaxy"
          type="checkbox"
          :class="checkbox"
          :disabled="!galaxyAvailable"
        />
        {{ t("scene3d.galaxy") }}
      </label>
      <label
        v-if="showGalaxy && galaxyAvailable"
        class="flex items-center gap-2"
        :title="t('scene3d.galaxyZoomHint')"
      >
        <span class="shrink-0 text-slate-600 dark:text-slate-300">
          {{ t("scene3d.galaxyZoom") }}
        </span>
        <input
          v-model.number="galaxyZoom"
          type="range"
          min="0"
          max="1"
          step="0.01"
          class="w-32 accent-brand-600"
        />
        <!-- Not a percentage: the honest readout is how much sky is actually in frame, which runs
             from tens of parsecs to megaparsecs across the same slider. -->
        <span class="tabular-nums text-slate-500 dark:text-slate-400">
          {{ galaxySpanLabel }}
        </span>
      </label>
      <label
        v-if="showGalaxy && galaxyAvailable"
        class="flex items-center gap-2"
        :title="t('scene3d.galaxyGainHint')"
      >
        <span class="shrink-0 text-slate-600 dark:text-slate-300">
          {{ t("scene3d.galaxyGain") }}
        </span>
        <input
          v-model.number="galaxyGain"
          type="range"
          min="0.1"
          max="2"
          step="0.05"
          class="w-24 accent-brand-600"
        />
      </label>
    </div>

    <p
      v-if="showGalaxy && galaxyAvailable"
      class="text-xs text-slate-500 dark:text-slate-400"
    >
      {{ t("scene3d.galaxyModelled") }}
      <span v-if="manifest.camera.right_handed === false">
        {{ t("scene3d.galaxyMirrored") }}
      </span>
      <!-- Said plainly rather than left to be noticed: a viewer who knows what the Milky Way looks
           like will go looking for the dark lanes, and the honest answer is that they are absent
           because nothing here can know where the eye stands relative to the dust. -->
      <span class="block">{{ t("scene3d.galaxyNoDust") }}</span>
    </p>

    <!-- The legend, which is also the honesty statement: what is measured, what is guessed, and
         what could not be placed at all. -->
    <p
      v-if="manifest.available"
      class="text-xs text-slate-500 dark:text-slate-400"
    >
      <span class="font-medium text-emerald-600 dark:text-emerald-400">
        {{ nf(counts.measured) }}
      </span>
      {{ t("scene3d.measuredCount") }}
      ·
      <span class="font-medium text-amber-600 dark:text-amber-400">
        {{ nf(counts.estimated) }}
      </span>
      {{ t("scene3d.estimatedCount") }}
      <template v-if="counts.unknown">
        · {{ t("scene3d.unknownCount", { n: nf(counts.unknown) }) }}
      </template>
      <template v-if="counts.physical_colour">
        · {{ t("scene3d.physicalColour", { n: nf(counts.physical_colour) }) }}
      </template>
      <template v-if="photometric.holdout_n">
        ·
        {{
          t("scene3d.holdout", {
            ratio: (photometric.holdout_median_ratio ?? 1).toFixed(2),
            n: nf(photometric.holdout_n),
          })
        }}
      </template>
    </p>
    <p
      v-if="holdoutWarning"
      class="text-xs text-warning-600 dark:text-warning-400"
    >
      {{ holdoutWarning }}
    </p>
    <p
      v-else-if="!photometric.calibrated && photometric.reason"
      class="text-xs text-slate-500 dark:text-slate-400"
    >
      {{ t("scene3d.noEstimates") }}
    </p>

    <!-- Objects in the field: where each one's distance came from, and — for anything with a real
         three-dimensional form — what that form rests on. -->
    <ul
      v-if="billboards.length"
      class="flex flex-col gap-1 text-xs text-slate-500 dark:text-slate-400"
    >
      <li v-for="b in billboards" :key="b.name">
        <span class="font-medium text-slate-600 dark:text-slate-300">
          {{ b.name }}
        </span>
        {{ formatDistance(b.dist_pc) }}
        <span
          v-if="b.dist_source === 'measured'"
          :title="t('scene3d.measuredFromFrameHint')"
        >
          ({{ t("scene3d.measuredFromFrame", { n: nf(b.members ?? 0) }) }})
        </span>
        <template v-if="b.shape && b.shape.kind !== 'plane'">
          ·
          <span :class="shapeTierClass[b.shape.source]">
            {{ t(`scene3d.shape.${b.shape.kind}`) }} —
            {{ t(`scene3d.tier.${b.shape.source}`) }}
          </span>
          <span class="text-slate-400 dark:text-slate-500">
            {{ b.shape.note }}</span
          >
          <span v-if="b.shape.cite" class="italic"> ({{ b.shape.cite }})</span>
        </template>
      </li>
    </ul>
    <p
      v-if="shapedObjects.length"
      class="text-xs text-slate-500 dark:text-slate-400"
    >
      {{ t("scene3d.shapeLegend") }}
    </p>
  </div>
</template>
