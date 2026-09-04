<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useImageStretch } from "@/composables/useImageStretch";
import { useCountdown } from "@/composables/useCountdown";
import { useImageZoom } from "@/composables/useImageZoom";
import { btnGhost, input } from "@/constants/styles";
import { useCaptureStore } from "@/stores/capture";
import { usePolarCamStore } from "@/stores/polarCam";

// The live view: what the telescope is seeing right now.
//
// The image arrives as a 16-bit buffer and is stretched HERE, in a canvas LUT — so dragging the
// black point is instant and never waits for a round trip, exactly like the file viewer. On top of
// it sits the viewfinder: a centre reticle and a centre box, because the one thing you cannot judge
// from a stretched thumbnail is whether the target is actually in the middle.
const props = defineProps<{
  // fill makes the viewer occupy its parent completely and float its toolbars over the image, for the
  // fullscreen layer. Inline it stays an aspect-ratio box with the toolbars in normal flow.
  fill?: boolean;
  // chromeVisible fades the floating toolbars in step with the rest of the fullscreen chrome.
  chromeVisible?: boolean;
}>();
const { t } = useI18n();
const store = useCaptureStore();
const polar = usePolarCamStore();

const canvas = ref<HTMLCanvasElement | null>(null);
const overlay = ref<HTMLCanvasElement | null>(null);
const container = ref<HTMLElement | null>(null);
const stretch = useImageStretch();

// Pan/zoom is the SAME engine the file viewer uses (`useImageZoom`): trackpad pinch, two-finger
// scroll to pan, drag, double-click to fit, keyboard. It was a slider-driven `scale()` before, which
// meant no panning at all — zooming in on a corner star was impossible, which is exactly when you
// need it (checking focus and framing away from the centre).
// maxZoomFactor 8 = up to eight screen pixels per SENSOR pixel. Judging focus means filling the view
// with a handful of pixels around one star, which is far deeper than browsing a finished image wants.
const zoom = useImageZoom(container, { maxZoomFactor: 8 });

// How long until the next frame lands. A five-minute sub otherwise looks like a frozen screen, with
// no way to tell "still integrating" from "the camera has stopped".
const nextFrame = useCountdown(
  () => store.liveExposureEnds,
  () => store.liveExposureUs,
);
// Only worth showing when the wait is long enough to notice — a 100 ms live loop flashing "0s" would
// be noise.
const showCountdown = computed(
  () => store.liveRunning && (store.liveExposureUs ?? 0) >= 1_000_000,
);

const black = ref(0);
const white = ref(65535);
const gamma = ref(0.5);
const autoStretch = ref(true);
const showReticle = ref(true);
const showGrid = ref(false);
const boxPx = ref(200);

// Real sensor pixels, on demand.
//
// The live stream is a ~1024 px preview of a 4656 px sensor, so magnifying it past its own resolution
// only interpolates — the star's actual pixel structure is not in the data. Focusing needs those
// pixels, so once the zoom passes the preview's resolution the visible region is re-fetched as a
// FULL-RESOLUTION crop (`/live/frame?x&y&w&h`) and drawn in its place. Everything below works in
// SENSOR coordinates so the two sources are interchangeable and the reticle never shifts.
interface DetailRect {
  x: number;
  y: number;
  w: number;
  h: number;
}
const detail = ref<DetailRect | null>(null);
// How many distinct values the visible real-pixel crop actually contains. Low numbers are the honest
// answer to "why does this look like three colours?": see quantisationNote below.
const detailLevels = ref(0);
let detailBusy = false;
let detailTimer = 0;

// The sensor's true dimensions, which the zoom engine treats as the natural size.
const sensorW = computed(
  () => store.liveStats?.width || store.liveImage?.w || 0,
);
const sensorH = computed(
  () => store.liveStats?.height || store.liveImage?.h || 0,
);

// Screen pixels per PREVIEW pixel. Above 1 the preview is being upscaled and it is time to fetch
// real pixels instead.
const previewUpscale = computed(() => {
  const pw = store.liveImage?.w ?? 0;
  if (!pw || !sensorW.value) return 0;
  return zoom.scale.value * (sensorW.value / pw);
});
const pixelPeek = computed(() => previewUpscale.value > 1);

// Past actual size, smoothing hides exactly what focusing needs to see: whether the star is spread
// over three pixels or eight. Show the pixel grid instead of a blur.
const crisp = computed(() => zoom.scale.value > 1);

// The image element is placed in SENSOR coordinates: full frame when showing the preview, or the
// crop's own rectangle when pixel-peeking. That is what lets the two sources swap without the view
// jumping — the pixels under the reticle stay the same pixels.
const imageStyle = computed(() => {
  const d = detail.value;
  const box = d ?? { x: 0, y: 0, w: sensorW.value, h: sensorH.value };
  return {
    left: `${box.x}px`,
    top: `${box.y}px`,
    width: `${box.w}px`,
    height: `${box.h}px`,
    // Past actual size, interpolation smooths away the very thing being judged.
    imageRendering: crisp.value ? ("pixelated" as const) : ("auto" as const),
  };
});

// Ask for the visible region at full resolution, debounced so a pinch does not fire a request per
// frame, and re-issued when a new exposure lands so the view stays live while the focuser turns.
function scheduleDetail() {
  window.clearTimeout(detailTimer);
  if (!pixelPeek.value) {
    if (detail.value) {
      detail.value = null;
      draw();
    }
    return;
  }
  detailTimer = window.setTimeout(fetchDetail, 120);
}

async function fetchDetail() {
  if (detailBusy || !pixelPeek.value || !sensorW.value) return;
  const v = zoom.viewport.value;
  // A margin means a small pan does not immediately need another round trip.
  const margin = 0.15;
  const x = Math.max(0, (v.x - v.w * margin) * sensorW.value);
  const y = Math.max(0, (v.y - v.h * margin) * sensorH.value);
  const w = Math.min(sensorW.value - x, v.w * (1 + 2 * margin) * sensorW.value);
  const h = Math.min(sensorH.value - y, v.h * (1 + 2 * margin) * sensorH.value);
  if (w < 8 || h < 8) return;

  detailBusy = true;
  try {
    // Only ever ask for as many pixels as this display can draw. Requesting the sensor's full
    // resolution for a fit-to-window view moved ~25 MB per frame to show detail no screen could
    // resolve; matching the container (times the device pixel ratio) is visually identical.
    const dpr = window.devicePixelRatio || 1;
    const screenW = (container.value?.clientWidth ?? 1024) * dpr;
    const img = await store.fetchCrop(x, y, w, h, Math.min(w, screenW));
    // The zoom may have moved on while the request was in flight; a stale crop drawn at the new
    // position would be visibly wrong, so it is dropped rather than shown.
    if (!pixelPeek.value) return;
    // Deliberately NOT re-deriving the stretch here. A crop's own histogram is not the frame's: a
    // 36x28 window of sky spans a few ADU, so auto-stretching each crop remapped brightness on every
    // pan and zoom — the image visibly changed as you moved, which makes judging focus impossible.
    // The stretch belongs to the FRAME and stays put while you look around it.
    stretch.setImage(img);
    detailLevels.value = countLevels(img.data);
    detail.value = { x, y, w, h };
    drawImageOnly(img);
  } catch {
    // A failed crop just leaves the preview in place — never a broken view mid-focus.
  } finally {
    detailBusy = false;
  }
}

// Entering or leaving fullscreen is a deliberate reframing, so re-fit. A plain window resize must NOT
// re-fit — that would yank the view away from a star someone had zoomed in on.
watch(
  () => props.fill,
  async () => {
    await nextTick();
    zoom.fit();
  },
);

watch(() => zoom.scale.value, scheduleDetail);
watch(() => [zoom.tx.value, zoom.ty.value], scheduleDetail);

// Repaint whenever a new frame lands, re-applying the auto stretch unless the user has taken over.
watch(
  () => store.liveImage,
  (img) => {
    if (!img) return;
    // The zoom engine works in SENSOR pixels, so 1:1 means one real sensor pixel per screen pixel
    // whether the data currently on screen is the downscaled preview or a full-resolution crop.
    if (
      sensorW.value &&
      (sensorW.value !== zoom.natW.value || sensorH.value !== zoom.natH.value)
    ) {
      zoom.setNatural(sensorW.value, sensorH.value);
    }
    // While pixel-peeking, the full-frame preview is only used for the histogram; the visible pixels
    // come from the crop, which is refreshed here so focusing stays live.
    if (pixelPeek.value) {
      void fetchDetail();
      return;
    }
    stretch.setImage(img);
    if (autoStretch.value) {
      const [lo, hi] = usableWindow(img.autoLo, img.autoHi);
      black.value = lo;
      white.value = hi;
    }
    draw();
  },
);

// countLevels counts distinct sample values in a crop.
//
// Cheap (the crop is a few thousand pixels) and worth knowing, because a 12-bit sensor reported in
// 16-bit words steps by 16, so a capped or empty field genuinely contains a handful of values. Seeing
// "3 colours" then is the data, not the renderer — and saying so beats letting someone chase a
// rendering bug that is not there.
function countLevels(pix: Uint16Array | number[]): number {
  const seen = new Set<number>();
  for (let i = 0; i < pix.length && seen.size <= 64; i++) seen.add(pix[i]);
  return seen.size;
}

// quantisationNote explains a posterised view: too few distinct values to render a gradient. It only
// appears while pixel-peeking, where the data is real sensor pixels rather than a smoothed downscale.
const quantisationNote = computed(() =>
  pixelPeek.value && detailLevels.value > 0 && detailLevels.value < 12
    ? t("capture.live.fewLevels", { levels: detailLevels.value })
    : "",
);

// usableWindow keeps the auto stretch from amplifying pure read noise into a black-and-white mosaic.
//
// The ASI1600 is a TWELVE-bit sensor reported in 16-bit words, so real values only ever step by 16.
// On a capped or empty field the auto window collapses to a dozen ADU — under one 12-bit level — and
// every pixel lands on black, mid-grey or white. Widening to a floor of 16 levels keeps a recognisable
// gradient. It cannot hide real signal: a frame with stars spans far more than this already.
const MIN_STRETCH_WINDOW = 16 * 16;

function usableWindow(lo: number, hi: number): [number, number] {
  if (hi - lo >= MIN_STRETCH_WINDOW) return [lo, hi];
  const mid = (lo + hi) / 2;
  const half = MIN_STRETCH_WINDOW / 2;
  return [
    Math.max(0, Math.round(mid - half)),
    Math.min(65535, Math.round(mid + half)),
  ];
}

watch([black, white, gamma], draw);
watch([showReticle, showGrid, boxPx], drawOverlay);
// Pan and zoom move the reticle on screen even when no new frame has arrived. So does a new polar
// solve: the marker is what the user is steering by, and it has to move the moment it is recomputed
// rather than waiting for the next exposure to land.
watch(
  () => [
    zoom.scale.value,
    zoom.tx.value,
    zoom.ty.value,
    sensorW.value,
    polar.target?.nx,
    polar.target?.ny,
    polar.pole?.pole.nx,
    polar.pole?.pole.ny,
  ],
  drawOverlay,
);

function draw() {
  const img = store.liveImage;
  if (!img) return;
  drawImageOnly(img);
  drawOverlay();
}

// drawImageOnly paints whichever buffer is current — preview or crop — at its own pixel size. The
// element's CSS size (set in the template from `detail`) is what places it in sensor space.
function drawImageOnly(img: { w: number; h: number }) {
  const el = canvas.value;
  if (!el) return;
  el.width = img.w;
  el.height = img.h;
  const ctx = el.getContext("2d");
  if (!ctx) return;
  stretch.render(ctx, black.value, white.value, gamma.value);
}

// drawOverlay paints the viewfinder in SCREEN pixels, mapping sensor coordinates through the same
// transform the image uses. That keeps the crosshair one pixel wide at every zoom, and keeps the
// centre box meaning what it says — a size in real sensor pixels, however magnified.
function drawOverlay() {
  const el = overlay.value;
  const box = container.value;
  if (!el || !box || !sensorW.value) return;

  const dpr = window.devicePixelRatio || 1;
  const w = box.clientWidth;
  const h = box.clientHeight;
  el.width = Math.round(w * dpr);
  el.height = Math.round(h * dpr);
  const ctx = el.getContext("2d");
  if (!ctx) return;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);

  const sc = zoom.scale.value;
  const toScreenX = (sx: number) => sx * sc + zoom.tx.value;
  const toScreenY = (sy: number) => sy * sc + zoom.ty.value;

  ctx.lineWidth = 1;
  ctx.strokeStyle = "rgba(129,140,248,0.9)";

  if (showReticle.value) {
    const cx = toScreenX(sensorW.value / 2);
    const cy = toScreenY(sensorH.value / 2);
    // A fixed-length arm: the crosshair is a marker, not a measurement, and one that grew with zoom
    // would swamp the frame.
    const arm = 26;
    ctx.beginPath();
    ctx.moveTo(cx - arm, cy);
    ctx.lineTo(cx + arm, cy);
    ctx.moveTo(cx, cy - arm);
    ctx.lineTo(cx, cy + arm);
    ctx.stroke();
    // The centre box IS a measurement — N sensor pixels — so it scales with the image.
    const half = (boxPx.value / 2) * sc;
    ctx.strokeRect(cx - half, cy - half, half * 2, half * 2);
  }

  if (showGrid.value) {
    ctx.strokeStyle = "rgba(148,163,184,0.45)";
    for (let i = 1; i < 3; i++) {
      const x = toScreenX((sensorW.value * i) / 3);
      const y = toScreenY((sensorH.value * i) / 3);
      ctx.beginPath();
      ctx.moveTo(x, 0);
      ctx.lineTo(x, h);
      ctx.moveTo(0, y);
      ctx.lineTo(w, y);
      ctx.stroke();
    }
  }

  drawPolarTarget(ctx, w, h, toScreenX, toScreenY);
  drawPoleFinder(ctx, toScreenX, toScreenY);
}

// drawPoleFinder marks the celestial pole and its guide star on the frame — the digital polar scope.
//
// It answers the question that comes BEFORE any measurement, and which is otherwise answered by lying
// on wet grass looking through a hole in the mount: where is the pole from here? The pole is drawn
// even when it falls outside the sensor, clipped to the border, because "it is that way and this far"
// is exactly what somebody hunting for it needs.
function drawPoleFinder(
  ctx: CanvasRenderingContext2D,
  toScreenX: (sx: number) => number,
  toScreenY: (sy: number) => number,
) {
  const view = polar.pole;
  if (!view) return;

  // The guide star first, so the pole marker draws over it if they overlap.
  if (view.star_visible) {
    const sx = toScreenX(view.star.nx * sensorW.value);
    const sy = toScreenY(view.star.ny * sensorH.value);
    ctx.save();
    ctx.strokeStyle = "rgba(148,197,253,0.9)";
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.arc(sx, sy, 9, 0, Math.PI * 2);
    ctx.stroke();
    labelAt(ctx, view.star_name, sx, sy + 22, "rgb(191,219,254)");
    ctx.restore();
  }

  // The pole itself: a cross rather than a ring, so it never reads as the target ring the adjustment
  // phase draws. They are different things — one is where the pole IS, the other is where the middle
  // of the frame has to GO — and a user who confuses them turns the wrong way.
  const px = toScreenX(view.pole.nx * sensorW.value);
  const py = toScreenY(view.pole.ny * sensorH.value);
  ctx.save();
  ctx.strokeStyle = "rgba(129,140,248,0.95)";
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  ctx.moveTo(px - 10, py);
  ctx.lineTo(px + 10, py);
  ctx.moveTo(px, py - 10);
  ctx.lineTo(px, py + 10);
  ctx.stroke();
  ctx.beginPath();
  ctx.arc(px, py, 4, 0, Math.PI * 2);
  ctx.stroke();
  labelAt(ctx, t("capture.polar.poleMark"), px, py - 14, "rgb(165,180,252)");
  ctx.restore();
}

// labelAt writes a haloed label, legible over any star field.
function labelAt(
  ctx: CanvasRenderingContext2D,
  text: string,
  x: number,
  y: number,
  fill: string,
) {
  ctx.font = "11px system-ui, sans-serif";
  ctx.textAlign = "center";
  ctx.lineWidth = 3;
  ctx.strokeStyle = "rgba(2,6,23,0.85)";
  ctx.strokeText(text, x, y);
  ctx.fillStyle = fill;
  ctx.fillText(text, x, y);
}

// drawPolarTarget marks where the middle of the frame has to end up for the mount to be polar-aligned.
//
// This is the whole point of measuring: two numbers in arcminutes are the correct answer and a
// hopeless instruction for somebody in the dark with one hand on a bolt. A ring to drive the
// crosshairs into is not.
//
// It is drawn from NORMALISED coordinates rather than pixels, because the marker is computed from a
// full-resolution frame while the screen is showing a downsampled preview, and the two only agree as
// fractions of the sensor.
function drawPolarTarget(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  toScreenX: (sx: number) => number,
  toScreenY: (sy: number) => number,
) {
  const target = polar.target;
  if (!target) return;

  const cx = toScreenX(sensorW.value / 2);
  const cy = toScreenY(sensorH.value / 2);
  const tx = toScreenX(target.nx * sensorW.value);
  const ty = toScreenY(target.ny * sensorH.value);

  // Off-frame is the NORMAL first measurement: a degree of error with a one-degree field puts the
  // marker off the edge every time. Pin it to the border with an arrow rather than drawing nothing,
  // so the user can still see which way to turn.
  const inside = tx >= 0 && ty >= 0 && tx <= w && ty <= h;
  const px = Math.max(12, Math.min(w - 12, tx));
  const py = Math.max(12, Math.min(h - 12, ty));

  ctx.save();
  ctx.strokeStyle = inside ? "rgba(74,222,128,0.95)" : "rgba(251,191,36,0.95)";
  ctx.lineWidth = 2;

  // The line from the crosshairs to the marker: the direction to drive the field.
  ctx.beginPath();
  ctx.setLineDash([6, 4]);
  ctx.moveTo(cx, cy);
  ctx.lineTo(px, py);
  ctx.stroke();
  ctx.setLineDash([]);

  // A ring of FIXED size — it marks a place, it does not measure one, and a ring that grew with zoom
  // would swallow the field just as the user got close enough to need it.
  ctx.beginPath();
  ctx.arc(px, py, 14, 0, Math.PI * 2);
  ctx.stroke();
  ctx.beginPath();
  ctx.arc(px, py, 3, 0, Math.PI * 2);
  ctx.stroke();

  // How far there is left to go, in the units the panel speaks.
  const label = `${target.offset_arcmin.toFixed(1)}′`;
  ctx.font = "12px system-ui, sans-serif";
  ctx.textAlign = "center";
  ctx.lineWidth = 3;
  ctx.strokeStyle = "rgba(2,6,23,0.85)";
  ctx.strokeText(label, px, py - 20);
  ctx.fillStyle = inside ? "rgb(134,239,172)" : "rgb(253,224,71)";
  ctx.fillText(label, px, py - 20);
  ctx.restore();
}

const tempLabel = computed(() => {
  const s = store.liveStats;
  if (!s?.has_temp) return "";
  return `${(s.temp_milli_c / 1000).toFixed(1)} °C`;
});

onBeforeUnmount(() => {
  void store.stopLive();
});

// --- recording ----------------------------------------------------------------------------------
//
// The button is a toggle over the device server's recorder, which keeps the frames the preview is
// already producing. Polling only while it runs: an idle panel must not ask once a second forever.
const recording = computed(() => store.liveRecord?.running === true);
let recordTimer: number | undefined;

async function toggleRecording() {
  try {
    if (recording.value) await store.stopLiveRecord();
    else await store.startLiveRecord();
  } catch (e) {
    store.liveError = e instanceof Error ? e.message : String(e);
  }
}

watch(recording, (on) => {
  window.clearInterval(recordTimer);
  recordTimer = on
    ? window.setInterval(() => void store.refreshLiveRecord(), 1000)
    : undefined;
});
onBeforeUnmount(() => window.clearInterval(recordTimer));
</script>

<template>
  <div :class="props.fill ? 'relative h-full w-full' : 'space-y-2'">
    <!-- touch-none keeps a two-finger gesture from scrolling the PAGE instead of panning the image;
         tabindex makes the keyboard zoom reachable. -->
    <div
      ref="container"
      tabindex="0"
      :class="[
        'cursor-grab touch-none overflow-hidden bg-black outline-none focus-visible:ring-2 focus-visible:ring-brand-500',
        props.fill
          ? 'absolute inset-0'
          : 'relative aspect-[4/3] w-full rounded-md border border-slate-200 dark:border-slate-700',
      ]"
      @pointerdown="zoom.onPointerDown"
      @pointermove="zoom.onPointerMove"
      @pointerup="zoom.onPointerUp"
      @pointerleave="zoom.onPointerUp"
      @dblclick="zoom.onDblClick"
      @keydown="zoom.onKey"
    >
      <!-- Both canvases share ONE transform so the reticle can never drift off the pixels it marks. -->
      <div
        class="absolute left-0 top-0 will-change-transform"
        :class="zoom.transitionClass.value"
        :style="{ transform: zoom.transform.value, transformOrigin: '0 0' }"
      >
        <canvas
          ref="canvas"
          class="absolute max-w-none select-none"
          :style="imageStyle"
        />
      </div>
      <!-- The viewfinder is drawn in SCREEN space, on top of the transformed image. Drawing it in
           image space instead meant it was rasterised at preview resolution and then magnified with
           everything else, so at 16× the crosshair became a fat blur across the very pixels being
           inspected. -->
      <canvas
        ref="overlay"
        class="pointer-events-none absolute inset-0 h-full w-full select-none"
      />
      <p
        v-if="!store.liveImage"
        class="absolute inset-0 flex items-center justify-center text-sm text-slate-500"
      >
        {{
          store.liveRunning
            ? t("capture.live.waiting")
            : t("capture.live.stopped")
        }}
      </p>
      <span
        v-if="pixelPeek"
        :class="[
          'absolute left-2 rounded bg-brand-600/80 px-1.5 py-0.5 font-mono text-[11px] text-white',
          // Clear of the exit-fullscreen button, which occupies the top-left corner of the layer.
          props.fill ? 'top-12' : 'top-2',
        ]"
        >{{ t("capture.live.realPixels") }}</span
      >
      <!-- Why the view looks posterised, when it does. -->
      <span
        v-if="quantisationNote"
        class="absolute bottom-2 left-2 right-2 rounded bg-amber-900/80 px-1.5 py-1 text-[11px] text-amber-100"
        >{{ quantisationNote }}</span
      >
      <!-- Discreet, in the corner under the temperature: it answers "is it still working?" at a
           glance and must not sit on top of the stars being examined. -->
      <div
        v-if="showCountdown && nextFrame.seconds.value !== null"
        class="pointer-events-none absolute right-2 top-8 flex flex-col items-end gap-0.5"
      >
        <span
          class="rounded bg-black/60 px-1.5 py-0.5 font-mono text-[11px] tabular-nums text-slate-200"
          >{{ nextFrame.label.value }}</span
        >
        <span
          v-if="nextFrame.progress.value !== null"
          class="h-0.5 w-12 overflow-hidden rounded bg-white/20"
        >
          <span
            class="block h-full bg-brand-400/80 transition-[width] duration-200"
            :style="{ width: (nextFrame.progress.value ?? 0) * 100 + '%' }"
          />
        </span>
      </div>
      <span
        v-if="tempLabel"
        class="absolute right-2 top-2 rounded bg-black/60 px-1.5 py-0.5 font-mono text-[11px] text-slate-200"
        >{{ tempLabel }}</span
      >
    </div>

    <!-- In fill mode the toolbars float over the bottom of the image (leaving room for the settings
         column on the right) and fade with the rest of the chrome, so the preview really does own the
         whole page. Inline they sit below the image in normal flow. -->
    <div
      :class="[
        props.fill
          ? 'absolute bottom-0 left-0 right-80 z-10 space-y-2 bg-slate-950/80 p-3 backdrop-blur transition-opacity duration-300'
          : 'space-y-2',
        props.fill && !props.chromeVisible
          ? 'pointer-events-none opacity-0'
          : 'opacity-100',
      ]"
    >
      <div class="flex flex-wrap items-center gap-3 text-xs">
        <button
          :class="btnGhost"
          class="!px-2 !py-1"
          @click="store.liveRunning ? store.stopLive() : store.startLive()"
        >
          {{
            store.liveRunning ? t("capture.live.stop") : t("capture.live.start")
          }}
        </button>
        <!-- Keeping what is already on screen. Only offered while the preview runs, because there
             is nothing to record otherwise, and it reads out its own count so a recording that the
             disk cannot keep up with is visible rather than quietly thin. -->
        <button
          v-if="store.liveRunning"
          :class="btnGhost"
          class="!px-2 !py-1"
          :title="t('capture.live.recordHint')"
          @click="toggleRecording"
        >
          <span
            class="mr-1 inline-block h-2 w-2 rounded-full align-middle"
            :class="recording ? 'animate-pulse bg-red-500' : 'bg-slate-400'"
          />
          {{
            recording ? t("capture.live.recording") : t("capture.live.record")
          }}
          <span v-if="recording" class="ml-1 tabular-nums opacity-70">
            {{ store.liveRecord?.saved ?? 0
            }}<template v-if="store.liveRecord?.max_frames"
              >/{{ store.liveRecord.max_frames }}</template
            >
          </span>
        </button>
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          <input
            v-model="autoStretch"
            type="checkbox"
            class="accent-brand-600"
          />
          {{ t("capture.live.autoStretch") }}
        </label>
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          <input
            v-model="showReticle"
            type="checkbox"
            class="accent-brand-600"
          />
          {{ t("capture.live.reticle") }}
        </label>
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          <input v-model="showGrid" type="checkbox" class="accent-brand-600" />
          {{ t("capture.live.grid") }}
        </label>
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          {{ t("capture.live.box") }}
          <input
            v-model.number="boxPx"
            type="number"
            min="20"
            max="2000"
            step="20"
            :class="input"
            class="w-20"
          />
        </label>
        <div class="flex items-center gap-1 text-slate-500 dark:text-slate-400">
          {{ t("capture.live.zoom") }}
          <button
            :class="btnGhost"
            class="!px-1.5 !py-0.5"
            :title="t('capture.live.zoomOut')"
            @click="zoom.zoomBy(1 / 1.4)"
          >
            −
          </button>
          <span class="w-12 text-center font-mono"
            >{{ zoom.zoomPercent.value }}%</span
          >
          <button
            :class="btnGhost"
            class="!px-1.5 !py-0.5"
            :title="t('capture.live.zoomIn')"
            @click="zoom.zoomBy(1.4)"
          >
            +
          </button>
          <button :class="btnGhost" class="!px-1.5 !py-0.5" @click="zoom.fit()">
            {{ t("capture.live.fit") }}
          </button>
          <button
            :class="btnGhost"
            class="!px-1.5 !py-0.5"
            @click="zoom.actualSize()"
          >
            1:1
          </button>
        </div>
      </div>

      <div
        v-if="!autoStretch"
        class="flex flex-wrap items-center gap-3 text-xs"
      >
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          {{ t("capture.live.black") }}
          <input
            v-model.number="black"
            type="range"
            min="0"
            max="65535"
            class="w-28 accent-brand-600"
          />
        </label>
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          {{ t("capture.live.white") }}
          <input
            v-model.number="white"
            type="range"
            min="1"
            max="65535"
            class="w-28 accent-brand-600"
          />
        </label>
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          {{ t("capture.live.gamma") }}
          <input
            v-model.number="gamma"
            type="range"
            min="0.1"
            max="1"
            step="0.05"
            class="w-24 accent-brand-600"
          />
        </label>
      </div>
    </div>

    <p v-if="store.liveError" class="text-xs text-danger-500">
      {{ store.liveError }}
    </p>
  </div>
</template>
