<script setup lang="ts">
// Star-name label layer over the zoomable result image: a container-sized canvas (labels stay a
// fixed size — it is NOT inside the transformed layer) that maps each label's final-image pixel
// anchor through the viewer's live transform. Density is balanced: the brightest labels win,
// more appear as you zoom in, and a greedy pass drops anything whose measured text box would
// collide. Redraws are rAF-coalesced and only triggered by transform/data changes — no animation
// loop, zero idle cost. The viewer surface is always slate-950, so the light-on-dark canvas
// colours are correct in both themes.
import { computed, onBeforeUnmount, onMounted, watch } from "vue";
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import type { DetectedStar, StarCatalogInfo, StarLabel } from "@/types";
import type { ScreenPoint } from "@/utils/starOverlay";
import { outlineFor, selectStarMarkers } from "@/utils/starOverlay";
import StarInfoCard from "@/components/Common/StarInfoCard.vue";

const props = defineProps<{
  labels: StarLabel[]; // importance-sorted by the store (mag ascending, DSOs boosted)
  stars?: DetectedStar[]; // detected peaks, brightest first (same coordinate space as labels)
  starLimit?: number; // how many detected stars to plot; 0 / absent = none
  scaleArcsecPx?: number; // plate scale, for reporting a star's size in arcseconds on hover
  imageW: number; // stars.json coordinate space (usually === natW)
  imageH: number;
  natW: number; // displayed image intrinsic px
  natH: number;
  scale: number; // the viewer's live affine (img transform = translate(tx,ty) scale(scale))
  tx: number;
  ty: number;
  cw: number; // container CSS px
  ch: number;
}>();

const canvas = ref<HTMLCanvasElement | null>(null);
const { t, te } = useI18n();

// A detection's size is measured in pixels; the plate scale turns that into the arcseconds an
// observer actually cares about. Without a solved field there is no scale, so it stays in pixels.
function starArcsec(s: DetectedStar): string {
  const r = s.r_px;
  if (!r) return "";
  const fwhmPx = r * 2;
  if (!props.scaleArcsecPx) return `${fwhmPx.toFixed(1)} px`;
  return `${(fwhmPx * props.scaleArcsecPx).toFixed(2)}″ (${fwhmPx.toFixed(1)} px)`;
}

function formatArcmin(v: number): string {
  return v >= 60 ? `${(v / 60).toFixed(2)}°` : `${v.toFixed(1)}′`;
}

const SCAN_CAP = 1500; // wide-field guard: never scan more candidates than this per selection
const ANCHOR_MIN_SEP = 28; // px between accepted anchors
const PAN_RESELECT_PX = 32; // cumulative pan before the label choice is recomputed
const OVERSCAN = 24; // px beyond the viewport still considered visible (avoids edge pop-in)

// HoverTarget is whatever the pointer is currently over: a named catalogue object, or one of the
// anonymous detections. Both carry their screen anchor so the tooltip can sit beside it.
type HoverTarget =
  | { kind: "label"; label: StarLabel; x: number; y: number }
  | { kind: "star"; star: DetectedStar; x: number; y: number };

interface PlacedLabel {
  label: StarLabel;
  below: boolean; // text placed under the anchor (fallback when above collides)
  textW: number;
}

let selection: PlacedLabel[] = [];
let selScale = -1;
let selTx = 0;
let selTy = 0;
let selLabels: StarLabel[] = [];
const textWidths = new Map<string, number>();

function fontFor(l: StarLabel): string {
  return l.kind === "dso"
    ? "italic 12px system-ui, sans-serif"
    : "12px system-ui, sans-serif";
}

function toScreen(l: StarLabel): { x: number; y: number } {
  const kx = props.imageW > 0 ? props.natW / props.imageW : 1;
  const ky = props.imageH > 0 ? props.natH / props.imageH : 1;
  return {
    x: props.tx + l.x * kx * props.scale,
    y: props.ty + l.y * ky * props.scale,
  };
}

// screenExtent resolves a label's footprint against the viewer's current transform (null when the
// object has no catalogued size or is too small to outline right now — see utils/starOverlay).
function screenExtent(l: StarLabel) {
  const k = (props.imageW > 0 ? props.natW / props.imageW : 1) * props.scale;
  return outlineFor(l.extent, k);
}

// zoomRel is 1 at fit and grows as the visible fraction of the image shrinks (2 at 2×…).
function zoomRel(): number {
  const sw = props.scale * props.natW;
  const sh = props.scale * props.natH;
  if (sw <= 0 || sh <= 0) return 1;
  const vw = Math.min(1, props.cw / sw);
  const vh = Math.min(1, props.ch / sh);
  return Math.max(1, 1 / Math.sqrt(vw * vh));
}

function labelCap(z: number): number {
  return Math.min(40, Math.max(8, Math.round(8 * z)));
}

function measure(
  ctx: CanvasRenderingContext2D,
  text: string,
  font: string,
): number {
  const key = font + "|" + text;
  const cached = textWidths.get(key);
  if (cached !== undefined) return cached;
  ctx.font = font;
  const w = ctx.measureText(text).width;
  textWidths.set(key, w);
  return w;
}

function needsReselect(): boolean {
  if (selLabels !== props.labels || selScale !== props.scale) return true;
  return Math.hypot(props.tx - selTx, props.ty - selTy) > PAN_RESELECT_PX;
}

interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}
const overlaps = (a: Box, b: Box) =>
  a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

// recomputeSelection greedily accepts labels in importance order: visible, far enough from every
// accepted anchor, and with a non-colliding text box (above the anchor, else below).
function recomputeSelection(ctx: CanvasRenderingContext2D) {
  selection = [];
  selScale = props.scale;
  selTx = props.tx;
  selTy = props.ty;
  selLabels = props.labels;

  const cap = labelCap(zoomRel());
  const anchors: { x: number; y: number }[] = [];
  const boxes: Box[] = [];

  const n = Math.min(props.labels.length, SCAN_CAP);
  for (let i = 0; i < n && selection.length < cap; i++) {
    const l = props.labels[i];
    const p = toScreen(l);
    if (
      p.x < -OVERSCAN ||
      p.x > props.cw + OVERSCAN ||
      p.y < -OVERSCAN ||
      p.y > props.ch + OVERSCAN
    ) {
      continue;
    }
    let tooClose = false;
    for (const a of anchors) {
      if (Math.hypot(a.x - p.x, a.y - p.y) < ANCHOR_MIN_SEP) {
        tooClose = true;
        break;
      }
    }
    if (tooClose) continue;

    const textW = measure(ctx, l.name, fontFor(l));
    const above: Box = { x: p.x + 6, y: p.y - 24, w: textW + 6, h: 18 };
    const belowBox: Box = { x: p.x + 6, y: p.y + 6, w: textW + 6, h: 18 };
    let below = false;
    let box = above;
    if (boxes.some((b) => overlaps(b, above))) {
      if (boxes.some((b) => overlaps(b, belowBox))) continue;
      below = true;
      box = belowBox;
    }
    anchors.push(p);
    boxes.push(box);
    selection.push({ label: l, below, textW });
  }
}

function haloText(
  ctx: CanvasRenderingContext2D,
  text: string,
  x: number,
  y: number,
  fill: string,
  font: string,
) {
  ctx.font = font;
  ctx.lineWidth = 3;
  ctx.strokeStyle = "rgba(2,6,23,0.85)";
  ctx.strokeText(text, x, y);
  ctx.fillStyle = fill;
  ctx.fillText(text, x, y);
}

// The current frame's drawn markers, kept so a pointer move can hit-test what is actually on screen
// rather than re-deriving the selection.
let lastMarkers: ScreenPoint[] = [];
let hoveredMarker: ScreenPoint | null = null;

let raf = 0;
function scheduleDraw() {
  if (raf) return;
  raf = requestAnimationFrame(() => {
    raf = 0;
    draw();
  });
}

function draw() {
  const el = canvas.value;
  if (!el || props.cw <= 0 || props.ch <= 0) return;
  // DPR self-heal at draw time: covers container resizes AND monitor moves.
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  const bw = Math.round(props.cw * dpr);
  const bh = Math.round(props.ch * dpr);
  if (el.width !== bw || el.height !== bh) {
    el.width = bw;
    el.height = bh;
  }
  const ctx = el.getContext("2d");
  if (!ctx) return;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, props.cw, props.ch);

  // Detected stars first, so the named labels always draw on top of them.
  const k = (props.imageW > 0 ? props.natW / props.imageW : 1) * props.scale;
  const markers = selectStarMarkers(
    props.stars,
    { k, tx: props.tx, ty: props.ty },
    { w: props.cw, h: props.ch },
    props.starLimit ?? 0,
  );
  lastMarkers = markers;
  for (const p of markers) {
    // Each ring takes the star's OWN colour so it stays distinguishable whatever it sits on: a blue
    // ring around a blue star, an amber one around a red giant. Without a colour (mono master) fall
    // back to the neutral sky tone that reads as "measured" rather than "named".
    ctx.beginPath();
    ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2);
    ctx.lineWidth = p === hoveredMarker ? 2 : 1;
    ctx.strokeStyle = p.hex ?? "rgb(125,211,252)";
    ctx.globalAlpha = p === hoveredMarker ? 1 : 0.8;
    ctx.stroke();
    ctx.globalAlpha = 1;
  }

  if (props.labels.length === 0) return;
  if (needsReselect()) recomputeSelection(ctx);

  const showSecondary = zoomRel() >= 2;
  for (const s of selection) {
    const p = toScreen(s.label);
    if (
      p.x < -OVERSCAN ||
      p.x > props.cw + OVERSCAN ||
      p.y < -OVERSCAN ||
      p.y > props.ch + OVERSCAN
    ) {
      continue;
    }
    const dso = s.label.kind === "dso";
    // An extended object gets its catalogued footprint outlined; everything else (and anything
    // still too small on screen) keeps the plain anchor dot.
    const ext = dso ? screenExtent(s.label) : null;
    ctx.lineWidth = 1;
    ctx.strokeStyle = dso ? "rgba(252,211,77,0.9)" : "rgba(226,232,240,0.9)";
    if (ext) {
      ctx.save();
      ctx.setLineDash([5, 4]);
      ctx.strokeStyle = "rgba(252,211,77,0.55)";
      ctx.beginPath();
      ctx.ellipse(p.x, p.y, ext.rx, ext.ry, ext.angle, 0, Math.PI * 2);
      ctx.stroke();
      ctx.restore();
    } else {
      ctx.beginPath();
      ctx.arc(p.x, p.y, 3, 0, Math.PI * 2);
      ctx.stroke();
    }

    const primaryY = s.below ? p.y + 18 : p.y - 12;
    haloText(
      ctx,
      s.label.name,
      p.x + 8,
      primaryY,
      dso ? "rgba(252,211,77,0.95)" : "rgba(226,232,240,0.95)",
      fontFor(s.label),
    );
    if (showSecondary && s.label.secondary) {
      haloText(
        ctx,
        s.label.secondary,
        p.x + 8,
        primaryY + 12,
        "rgba(148,163,184,0.9)",
        "10px system-ui, sans-serif",
      );
    }
  }
}

// --- hover ------------------------------------------------------------------------------------
// The canvas is the only thing that knows where anything ended up on screen, so it owns hit-testing
// too. A hit prefers a NAMED label over a bare detection: when both are under the cursor the name is
// the more informative answer.
const hover = ref<HoverTarget | null>(null);
const hoverAt = ref({ x: 0, y: 0 });
const HIT_SLOP = 6; // px of forgiveness around a marker, so a 3 px ring is still catchable

function hitTest(mx: number, my: number): HoverTarget | null {
  let best: HoverTarget | null = null;
  let bestD = Infinity;
  for (const s of selection) {
    const p = toScreen(s.label);
    const d = Math.hypot(p.x - mx, p.y - my);
    const reach = Math.max(HIT_SLOP, screenExtent(s.label) ? 10 : HIT_SLOP);
    if (d <= reach && d < bestD) {
      best = { kind: "label", label: s.label, x: p.x, y: p.y };
      bestD = d;
    }
  }
  if (best) return best;
  for (const m of lastMarkers) {
    const d = Math.hypot(m.x - mx, m.y - my);
    if (d <= m.r + HIT_SLOP && d < bestD) {
      best = { kind: "star", star: m.star, x: m.x, y: m.y };
      bestD = d;
    }
  }
  return best;
}

function onMove(e: MouseEvent) {
  const el = canvas.value;
  if (!el) return;
  const r = el.getBoundingClientRect();
  const mx = e.clientX - r.left;
  const my = e.clientY - r.top;
  const hit = hitTest(mx, my);
  const changed = hit?.x !== hover.value?.x || hit?.y !== hover.value?.y;
  hover.value = hit;
  hoverAt.value = { x: mx, y: my };
  const nextMarker = hit?.kind === "star" ? (lastMarkers.find((m) => m.star === hit.star) ?? null) : null;
  if (nextMarker !== hoveredMarker || changed) {
    hoveredMarker = nextMarker;
    scheduleDraw();
  }
}

function onLeave() {
  if (hover.value || hoveredMarker) {
    hover.value = null;
    hoveredMarker = null;
    scheduleDraw();
  }
}

// --- hover card content -----------------------------------------------------------------------
// Whatever is under the cursor, the card answers the same question, so both branches read one
// catalogue record and one set of derivations.
const info = computed<StarCatalogInfo | null>(
  () =>
    (hover.value?.kind === "label"
      ? hover.value.label.star
      : hover.value?.star.star) ?? null,
);

const infoTitle = computed(() => {
  if (hover.value?.kind === "label") return hover.value.label.name;
  return info.value?.name || t("stars.info.unnamed");
});







watch(
  () => [
    props.scale,
    props.tx,
    props.ty,
    props.cw,
    props.ch,
    props.labels,
    props.stars,
    props.starLimit,
  ],
  () => {
    onLeave(); // a pan/zoom invalidates every screen position the hover was based on
    scheduleDraw();
  },
);
onMounted(scheduleDraw);
onBeforeUnmount(() => {
  if (raf) cancelAnimationFrame(raf);
});
</script>

<template>
  <div class="absolute inset-0">
    <canvas
      ref="canvas"
      aria-hidden="true"
      class="absolute inset-0 h-full w-full"
      @mousemove="onMove"
      @mouseleave="onLeave"
    />
    <!-- Hover card: everything known about whatever is under the cursor. Flips to the other side of
         the anchor near an edge so it never runs off the viewer. -->
    <div
      v-if="hover"
      class="pointer-events-none absolute z-20 max-w-[19rem] rounded-md border border-slate-700 bg-slate-900/95 px-2.5 py-2 text-xs text-slate-200 shadow-lg backdrop-blur"
      :style="{
        left: `${hoverAt.x > cw - 230 ? hoverAt.x - 240 : hoverAt.x + 14}px`,
        top: `${hoverAt.y > ch - 230 ? Math.max(4, hoverAt.y - 220) : hoverAt.y + 14}px`,
      }"
      role="tooltip"
    >
      <StarInfoCard
        :info="info"
        :title="infoTitle"
        :secondary="hover.kind === 'label' ? hover.label.secondary : info?.secondary"
        :title-class="
          hover.kind === 'label' && hover.label.kind === 'dso'
            ? 'text-amber-300'
            : info
              ? 'text-slate-100'
              : 'text-slate-300'
        "
        :mag-estimate="hover.kind === 'star' ? hover.star.mag : null"
      >
        <!-- What kind of object this is, which only the label overlay knows. -->
        <template #lead>
          <template v-if="hover.kind === 'label' && hover.label.type">
            <dt class="text-slate-500">{{ t("stars.info.type") }}</dt>
            <dd>{{ te(`skyTypes.${hover.label.type}`) ? t(`skyTypes.${hover.label.type}`) : hover.label.type }}</dd>
          </template>
          <template v-if="hover.kind === 'label' && hover.label.diameter_arcmin">
            <dt class="text-slate-500">{{ t("stars.info.size") }}</dt>
            <dd>{{ formatArcmin(hover.label.diameter_arcmin) }}</dd>
          </template>
          <template v-if="hover.kind === 'label' && !info && hover.label.mag < 90">
            <dt class="text-slate-500">{{ t("stars.info.mag") }}</dt>
            <dd>{{ hover.label.mag.toFixed(2) }}</dd>
          </template>
        </template>

        <template #trail>
          <template v-if="info?.con">
            <dt class="text-slate-500">{{ t("stars.info.constellation") }}</dt>
            <dd>{{ te(`constellations.${info.con}`) ? t(`constellations.${info.con}`) : info.con }}</dd>
          </template>

          <!-- Measured on THIS image rather than read from a catalogue. -->
          <template v-if="hover.kind === 'star'">
            <template v-if="starArcsec(hover.star)">
              <dt class="text-slate-500">{{ t("stars.info.fwhm") }}</dt>
              <dd>{{ starArcsec(hover.star) }}</dd>
            </template>
            <dt class="text-slate-500">{{ t("stars.info.pos") }}</dt>
            <dd>{{ hover.star.x }}, {{ hover.star.y }} px</dd>
            <template v-if="hover.star.hex">
              <dt class="text-slate-500">{{ t("stars.info.colour") }}</dt>
              <dd class="flex items-center gap-1">
                <span class="inline-block h-2.5 w-2.5 rounded-full border border-slate-600" :style="{ backgroundColor: hover.star.hex }" />
                {{ hover.star.hex }}
              </dd>
            </template>
          </template>
        </template>
      </StarInfoCard>
    </div>
  </div>
</template>
