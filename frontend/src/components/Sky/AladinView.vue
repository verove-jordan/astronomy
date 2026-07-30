<script setup lang="ts">
import { ref, computed, onBeforeUnmount, onMounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { SkyTarget } from "@/types";
import { MAP_SELECTED } from "@/constants/colors";
import {
  ellipseCorners,
  hitTile,
  movedCenter,
  offsetCorners,
  type Corner,
  type SkyPoint,
} from "@/utils/skygrid";

// Aladin Lite v3 sky viewer, loaded on demand from the CDS CDN (it needs internet for survey tiles
// anyway). Shows the selected object centered, with the camera's field-of-view rectangle drawn on the
// real sky so you can judge framing. Falls back to the external links if it can't load.
//
// With the optional `overlays` prop (the mosaic planner), the single FOV rectangle is replaced by
// one polygon per tile (server-computed corners, camera rotation included), the selected tile is
// emphasized, and clicks hit-test the polygons (overlay-click) — Aladin's own object picking does
// not cover graphic overlays, so the hit test is ours.
//
// With `interactive`, pressing inside a tile DRAGS the whole grid (shift-drag rotates it): the
// pointer handlers run in the capture phase and stop propagation, so Aladin's own pan never starts
// — pressing outside the grid still pans the sky normally. The drag previews client-side at pointer
// speed and emits once on release, where the server recomputes the authoritative plan.
const props = defineProps<{
  target: SkyTarget | null;
  fovWDeg: number;
  fovHDeg: number;
  overlays?: { corners: Corner[]; selected?: boolean }[];
  interactive?: boolean;
  center?: SkyPoint | null; // grid centre, required for dragging
  objectEllipse?: {
    ra: number;
    dec: number;
    majorArcmin: number;
    minorArcmin: number;
    paDeg: number;
  } | null;
}>();
const emit = defineEmits<{
  "overlay-click": [index: number];
  "grid-move": [center: SkyPoint];
  "grid-rotate": [deltaPaDeg: number];
}>();
const { t } = useI18n();

const el = ref<HTMLDivElement | null>(null);
const failed = ref(false);
const dragHint = ref("");

/* eslint-disable @typescript-eslint/no-explicit-any */
let aladin: any = null;
let overlay: any = null;
let selOverlay: any = null;
let objOverlay: any = null;
let A: any = null;
let initialized = false;

const ALADIN_SRC =
  "https://aladin.cds.unistra.fr/AladinLite/api/v3/latest/aladin.js";

function loadAladin(): Promise<any> {
  const w = window as any;
  if (w.A?.init) return w.A.init.then(() => w.A);
  return new Promise((resolve, reject) => {
    let s = document.querySelector<HTMLScriptElement>(
      `script[src="${ALADIN_SRC}"]`,
    );
    if (!s) {
      s = document.createElement("script");
      s.src = ALADIN_SRC;
      s.async = true;
      s.onerror = () => reject(new Error("aladin script failed to load"));
      document.head.appendChild(s);
    }
    const ready = () => {
      const api = (window as any).A;
      if (api?.init) api.init.then(() => resolve(api)).catch(reject);
      else window.setTimeout(ready, 150);
    };
    s.addEventListener("load", ready);
    ready();
  });
}

// Four corners of the camera field of view (degrees), RA scaled by cos(dec) so the box stays square.
function fovCorners(ra: number, dec: number): Corner[] {
  const hw =
    props.fovWDeg / 2 / Math.max(Math.cos((dec * Math.PI) / 180), 1e-3);
  const hh = props.fovHDeg / 2;
  return [
    [ra - hw, dec - hh],
    [ra + hw, dec - hh],
    [ra + hw, dec + hh],
    [ra - hw, dec + hh],
  ];
}

// Live drag state: the tangent-plane offset and PA delta applied to the drawn grid while the
// pointer is down. Null when idle.
let drag: {
  startSky: SkyPoint;
  startX: number;
  startY: number;
  rotating: boolean;
  dXi: number;
  dEta: number;
  dPa: number;
} | null = null;

function drawnCorners(tile: { corners: Corner[] }): Corner[] {
  if (!drag || !props.center) return tile.corners;
  return offsetCorners(
    tile.corners,
    props.center,
    drag.dXi,
    drag.dEta,
    drag.dPa,
  );
}

function showTarget() {
  if (!aladin || !A || !props.target) return;
  const { ra_deg: ra, dec_deg: dec } = props.target;
  overlay.removeAll();
  selOverlay.removeAll();
  objOverlay?.removeAll();
  if (props.overlays?.length) {
    // Only re-centre and re-zoom when the user is not dragging — otherwise the map would fight
    // the pointer.
    if (!drag) {
      aladin.gotoRaDec(ra, dec);
      aladin.setFoV(overlaySpanDeg(ra, dec) || 1);
    }
    if (props.objectEllipse && props.objectEllipse.majorArcmin > 0) {
      const e = props.objectEllipse;
      objOverlay?.add(
        A.polygon(
          ellipseCorners(
            { ra: e.ra, dec: e.dec },
            e.majorArcmin,
            e.minorArcmin,
            e.paDeg,
          ),
        ),
      );
    }
    for (const tile of props.overlays) {
      (tile.selected ? selOverlay : overlay).add(A.polygon(drawnCorners(tile)));
    }
    return;
  }
  aladin.gotoRaDec(ra, dec);
  aladin.setFoV(Math.max(props.fovWDeg, props.fovHDeg) * 3 || 1);
  overlay.add(A.polygon(fovCorners(ra, dec)));
}

// overlaySpanDeg sizes the view to the whole tile grid (max corner offset from the center, ×1.3).
function overlaySpanDeg(ra: number, dec: number): number {
  const cosDec = Math.max(Math.cos((dec * Math.PI) / 180), 1e-3);
  let span = 0;
  for (const tile of props.overlays ?? []) {
    for (const [cra, cdec] of tile.corners) {
      const dra = norm180(cra - ra) * cosDec;
      span = Math.max(span, 2 * Math.hypot(dra, cdec - dec));
    }
  }
  return span * 1.3;
}

function norm180(d: number): number {
  const r = ((d % 360) + 360) % 360;
  return r > 180 ? r - 360 : r;
}

// skyAt converts a pointer event to sky coordinates. Aladin Lite v3 exposes pix2world; when a build
// doesn't, we fall back to the view's own angular scale, which is accurate enough for a drag
// (the release is server-recomputed anyway).
function skyAt(e: PointerEvent | MouseEvent): SkyPoint | null {
  const box = el.value?.getBoundingClientRect();
  if (!box || !aladin) return null;
  const x = e.clientX - box.left;
  const y = e.clientY - box.top;
  if (typeof aladin.pix2world === "function") {
    const p = aladin.pix2world(x, y);
    if (Array.isArray(p) && Number.isFinite(p[0]) && Number.isFinite(p[1])) {
      return { ra: p[0], dec: p[1] };
    }
  }
  const center = aladin.getRaDec?.();
  const fov = aladin.getFov?.();
  if (!Array.isArray(center) || !Array.isArray(fov) || !box.width) return null;
  const degPerPx = fov[0] / box.width;
  const dec = center[1] - (y - box.height / 2) * degPerPx;
  const cosDec = Math.max(Math.cos((dec * Math.PI) / 180), 1e-3);
  const ra = center[0] - ((x - box.width / 2) * degPerPx) / cosDec;
  return { ra: ((ra % 360) + 360) % 360, dec };
}

function onPointerDown(e: PointerEvent) {
  if (!props.interactive || !props.center || !props.overlays?.length) return;
  const sky = skyAt(e);
  if (!sky || hitTile(sky, props.overlays) < 0) return; // outside the grid → Aladin pans
  e.preventDefault();
  e.stopPropagation();
  drag = {
    startSky: sky,
    startX: e.clientX,
    startY: e.clientY,
    rotating: e.shiftKey,
    dXi: 0,
    dEta: 0,
    dPa: 0,
  };
  window.addEventListener("pointermove", onPointerMove);
  window.addEventListener("pointerup", onPointerUp, { once: true });
}

function onPointerMove(e: PointerEvent) {
  if (!drag || !props.center) return;
  if (drag.rotating) {
    // Horizontal travel spins the grid: a full view width is a half turn, which makes fine
    // adjustment easy without a separate handle.
    const width = el.value?.getBoundingClientRect().width || 1;
    drag.dPa = ((e.clientX - drag.startX) / width) * 180;
  } else {
    const sky = skyAt(e);
    if (!sky) return;
    const cosDec = Math.max(Math.cos((sky.dec * Math.PI) / 180), 1e-3);
    drag.dXi = norm180(sky.ra - drag.startSky.ra) * cosDec;
    drag.dEta = sky.dec - drag.startSky.dec;
  }
  showTarget();
}

function onPointerUp() {
  window.removeEventListener("pointermove", onPointerMove);
  const d = drag;
  drag = null;
  if (!d || !props.center) return;
  if (d.rotating) {
    if (Math.abs(d.dPa) > 0.05) emit("grid-rotate", d.dPa);
    else showTarget();
    return;
  }
  if (Math.hypot(d.dXi, d.dEta) < 1e-4) {
    // A press with no travel is a click: keep the existing tile-selection behaviour.
    const i = hitTile(d.startSky, props.overlays ?? []);
    if (i >= 0) emit("overlay-click", i);
    showTarget();
    return;
  }
  emit("grid-move", movedCenter(props.center, d.dXi, d.dEta));
}

// handleSkyClick keeps click-to-select working when the view is not interactive (Aladin's own
// click event; our pointer handler owns selection while dragging is enabled).
function handleSkyClick(pos: { ra?: number; dec?: number } | undefined) {
  if (props.interactive) return;
  if (!pos || pos.ra === undefined || pos.dec === undefined) return;
  const tiles = props.overlays;
  if (!tiles?.length) return;
  const i = hitTile({ ra: pos.ra, dec: pos.dec }, tiles);
  if (i >= 0) emit("overlay-click", i);
}

async function ensureInit() {
  if (initialized || !el.value || !props.target) return;
  initialized = true;
  try {
    A = await loadAladin();
    aladin = A.aladin(el.value, {
      survey: "P/DSS2/color",
      cooFrame: "ICRSd",
      fov: Math.max(props.fovWDeg, props.fovHDeg) * 3 || 1,
      showReticle: false,
      showLayersControl: false,
      showFullscreenControl: false,
      showGotoControl: false,
      showSimbadPointerControl: false,
    });
    objOverlay = A.graphicOverlay({ color: "#f59e0b", lineWidth: 1 });
    aladin.addOverlay(objOverlay);
    overlay = A.graphicOverlay({ color: "#6366f1", lineWidth: 2 });
    aladin.addOverlay(overlay);
    selOverlay = A.graphicOverlay({ color: MAP_SELECTED, lineWidth: 3 });
    aladin.addOverlay(selOverlay);
    aladin.on("click", handleSkyClick);
    // Capture phase: our handler runs before Aladin's own canvas listeners, so stopping propagation
    // inside a tile prevents the map from panning under the drag.
    el.value.addEventListener("pointerdown", onPointerDown, true);
    dragHint.value = props.interactive ? t("mosaic.map.dragHint") : "";
    showTarget();
  } catch {
    failed.value = true;
    initialized = false;
  }
}

onMounted(ensureInit);
onBeforeUnmount(() => {
  window.removeEventListener("pointermove", onPointerMove);
  el.value?.removeEventListener("pointerdown", onPointerDown, true);
});
// Redraw on a new target AND when the field of view or the tile overlays change (optics edit,
// planner knob tweak, tile selection), so the framing and zoom always reflect the current state.
watch(
  () => [props.target?.name, props.fovWDeg, props.fovHDeg, props.overlays],
  () => {
    if (!initialized) void ensureInit();
    else showTarget();
  },
);
/* eslint-enable @typescript-eslint/no-explicit-any */

const aladinUrl = computed(() => {
  const tg = props.target;
  if (!tg) return "#";
  const fov = (Math.max(props.fovWDeg, props.fovHDeg) * 3 || 1).toFixed(2);
  return `https://aladin.cds.unistra.fr/AladinLite/?target=${tg.ra_deg}%20${tg.dec_deg}&fov=${fov}`;
});
const googleUrl = computed(
  () =>
    `https://www.google.com/search?tbm=isch&q=${encodeURIComponent(
      props.target?.name ?? "",
    )}`,
);
</script>

<template>
  <div>
    <div
      ref="el"
      class="h-64 w-full overflow-hidden rounded-md border border-slate-200 bg-black dark:border-slate-700"
    />
    <p v-if="failed" class="mt-1 text-xs text-slate-400">
      {{ t("tonight.preview.unavailable") }}
    </p>
    <p v-else-if="dragHint" class="mt-1 text-xs text-slate-400">
      {{ dragHint }}
    </p>
    <div class="mt-1 flex gap-4 text-xs">
      <a
        :href="aladinUrl"
        target="_blank"
        rel="noopener"
        class="text-brand-600 hover:underline dark:text-brand-300"
        >{{ t("tonight.preview.aladin") }}</a
      >
      <a
        :href="googleUrl"
        target="_blank"
        rel="noopener"
        class="text-brand-600 hover:underline dark:text-brand-300"
        >{{ t("tonight.preview.google") }}</a
      >
    </div>
  </div>
</template>
