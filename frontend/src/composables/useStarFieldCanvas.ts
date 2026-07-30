import { onMounted, onUnmounted, watch, type Ref } from "vue";
import { SKY_MAP } from "@/constants/colors";
import { tangentPlane, tangentSky } from "@/utils/astro";
import type { StarfieldStar } from "@/types";

// A tiny gnomonic star-field renderer for the mosaic capture assistant: deepstars around a center,
// frame rectangles (the expected camera footprint, neighbor-tile ghosts) and N/E direction labels.
// Deliberately NOT useSkyMapCanvas (full-sky stereographic alt-az with interaction) — this is a
// static finder chart. Convention: north up, east LEFT (like Aladin); `mirrored` flips east for
// optical trains with a diagonal.

// A rectangle to draw: either explicit sky corners (server-computed tile footprints) or a centered
// frame of wDeg×hDeg rotated by paDeg (the camera-angle preview at the pole).
export interface StarFieldRect {
  cornersRaDec?: [number, number][];
  wDeg?: number;
  hDeg?: number;
  paDeg?: number;
  dashed?: boolean;
}

export interface StarFieldOpts {
  centerRaDeg: number;
  centerDecDeg: number;
  fovDeg: number; // canvas width in degrees
  stars: StarfieldStar[];
  rects: StarFieldRect[];
  mirrored?: boolean;
}

export function useStarFieldCanvas(
  canvasRef: Ref<HTMLCanvasElement | null>,
  opts: Ref<StarFieldOpts | null>,
) {
  let observer: ResizeObserver | null = null;

  function draw() {
    const canvas = canvasRef.value;
    const o = opts.value;
    if (!canvas || !o) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const cssW = canvas.clientWidth || 300;
    const cssH = canvas.clientHeight || cssW;
    canvas.width = Math.round(cssW * dpr);
    canvas.height = Math.round(cssH * dpr);
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.scale(dpr, dpr);

    const grad = ctx.createLinearGradient(0, 0, 0, cssH);
    grad.addColorStop(0, SKY_MAP.bgTop);
    grad.addColorStop(1, SKY_MAP.bgBottom);
    ctx.fillStyle = grad;
    ctx.fillRect(0, 0, cssW, cssH);

    const scale = cssW / o.fovDeg; // px per degree
    const flip = o.mirrored ? -1 : 1;
    // Sky → canvas: ξ east-positive drawn LEFT (x = cx − ξ), η north-positive drawn UP (y = cy − η).
    const toXY = (raDeg: number, decDeg: number): [number, number] | null => {
      const p = tangentPlane(o.centerRaDeg, o.centerDecDeg, raDeg, decDeg);
      if (!p) return null;
      return [cssW / 2 - p.xi * scale * flip, cssH / 2 - p.eta * scale];
    };

    drawStars(ctx, o, toXY);
    for (const rect of o.rects) drawRect(ctx, o, rect, toXY);
    drawCardinals(ctx, cssW, cssH, flip);
  }

  function drawStars(
    ctx: CanvasRenderingContext2D,
    o: StarFieldOpts,
    toXY: (ra: number, dec: number) => [number, number] | null,
  ) {
    for (const star of o.stars) {
      const xy = toXY(star.ra_deg, star.dec_deg);
      if (!xy) continue;
      const r = Math.max(0.6, 3.6 - 0.35 * star.mag);
      ctx.globalAlpha = Math.max(0.25, Math.min(1, 1.15 - star.mag / 9));
      ctx.fillStyle = SKY_MAP.star;
      ctx.beginPath();
      ctx.arc(xy[0], xy[1], r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
  }

  function drawRect(
    ctx: CanvasRenderingContext2D,
    o: StarFieldOpts,
    rect: StarFieldRect,
    toXY: (ra: number, dec: number) => [number, number] | null,
  ) {
    let corners = rect.cornersRaDec;
    if (!corners && rect.wDeg && rect.hDeg) {
      // Frame corners from a centered wDeg×hDeg footprint rotated by the camera PA (pinned
      // convention: ξ = u·cosPA + v·sinPA, η = −u·sinPA + v·cosPA).
      const pa = ((rect.paDeg ?? 0) * Math.PI) / 180;
      const uv: [number, number][] = [
        [-rect.wDeg / 2, rect.hDeg / 2],
        [rect.wDeg / 2, rect.hDeg / 2],
        [rect.wDeg / 2, -rect.hDeg / 2],
        [-rect.wDeg / 2, -rect.hDeg / 2],
      ];
      corners = uv.map(([u, v]) => {
        const xi = u * Math.cos(pa) + v * Math.sin(pa);
        const eta = -u * Math.sin(pa) + v * Math.cos(pa);
        const sky = tangentSky(o.centerRaDeg, o.centerDecDeg, xi, eta);
        return [sky.ra, sky.dec] as [number, number];
      });
    }
    if (!corners) return;
    const pts = corners
      .map(([ra, dec]) => toXY(ra, dec))
      .filter((p): p is [number, number] => p !== null);
    if (pts.length < 3) return;
    ctx.strokeStyle = rect.dashed ? SKY_MAP.cardinal : SKY_MAP.targetRing;
    ctx.lineWidth = rect.dashed ? 1 : 2;
    ctx.setLineDash(rect.dashed ? [5, 4] : []);
    ctx.beginPath();
    ctx.moveTo(pts[0][0], pts[0][1]);
    for (const p of pts.slice(1)) ctx.lineTo(p[0], p[1]);
    ctx.closePath();
    ctx.stroke();
    ctx.setLineDash([]);
  }

  function drawCardinals(
    ctx: CanvasRenderingContext2D,
    cssW: number,
    cssH: number,
    flip: number,
  ) {
    ctx.fillStyle = SKY_MAP.cardinal;
    ctx.font = "600 11px ui-sans-serif, system-ui";
    ctx.textAlign = "center";
    ctx.fillText("N", cssW / 2, 14);
    ctx.textAlign = flip === 1 ? "left" : "right";
    ctx.fillText("E", flip === 1 ? 6 : cssW - 6, cssH / 2 + 4);
  }

  onMounted(() => {
    observer = new ResizeObserver(draw);
    if (canvasRef.value) observer.observe(canvasRef.value);
    draw();
  });
  onUnmounted(() => observer?.disconnect());
  watch(opts, draw, { deep: true });

  return { redraw: draw };
}
