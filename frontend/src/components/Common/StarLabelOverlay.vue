<script setup lang="ts">
// Star-name label layer over the zoomable result image: a container-sized canvas (labels stay a
// fixed size — it is NOT inside the transformed layer) that maps each label's final-image pixel
// anchor through the viewer's live transform. Density is balanced: the brightest labels win,
// more appear as you zoom in, and a greedy pass drops anything whose measured text box would
// collide. Redraws are rAF-coalesced and only triggered by transform/data changes — no animation
// loop, zero idle cost. The viewer surface is always slate-950, so the light-on-dark canvas
// colours are correct in both themes.
import { onBeforeUnmount, onMounted, watch } from "vue";
import { ref } from "vue";
import type { StarLabel } from "@/types";

const props = defineProps<{
  labels: StarLabel[]; // importance-sorted by the store (mag ascending, DSOs boosted)
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

const SCAN_CAP = 1500; // wide-field guard: never scan more candidates than this per selection
const ANCHOR_MIN_SEP = 28; // px between accepted anchors
const PAN_RESELECT_PX = 32; // cumulative pan before the label choice is recomputed
const OVERSCAN = 24; // px beyond the viewport still considered visible (avoids edge pop-in)

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
    ctx.beginPath();
    ctx.arc(p.x, p.y, 3, 0, Math.PI * 2);
    ctx.lineWidth = 1;
    ctx.strokeStyle = dso ? "rgba(252,211,77,0.9)" : "rgba(226,232,240,0.9)";
    ctx.stroke();

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

watch(
  () => [props.scale, props.tx, props.ty, props.cw, props.ch, props.labels],
  scheduleDraw,
);
onMounted(scheduleDraw);
onBeforeUnmount(() => {
  if (raf) cancelAnimationFrame(raf);
});
</script>

<template>
  <canvas
    ref="canvas"
    aria-hidden="true"
    class="pointer-events-none absolute inset-0 h-full w-full"
  />
</template>
