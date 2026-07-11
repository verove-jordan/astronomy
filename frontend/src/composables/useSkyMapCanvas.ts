import { onBeforeUnmount, onMounted, ref, watch, type Ref } from "vue";
import { precessFromJ2000, equatorialToHorizontal } from "@/utils/astro";
import { SKY_MAP } from "@/constants/colors";
import type { SkyMapData } from "@/composables/useSkyCatalog";

const DEG = Math.PI / 180;

export interface Observer {
  lat: number;
  lon: number;
  atMs: number;
}
export interface SkyBodyView {
  name: string;
  kind: string; // "moon" | "planet"
  alt: number;
  az: number;
  mag: number;
  phase?: number; // moon illuminated fraction
}
export interface SkyTargetView {
  alt: number;
  az: number;
  label: string;
}

interface Opts {
  canvas: Ref<HTMLCanvasElement | null>;
  data: Ref<SkyMapData | null>;
  observer: () => Observer | null;
  target: () => SkyTargetView | null;
  bodies: () => SkyBodyView[];
  bodyLabel: (name: string) => string;
}

const FOV_MIN = 2;
const FOV_MAX = 140;

// useSkyMapCanvas renders an interactive alt/az sky (stars, constellation figures + names, Moon/planets,
// horizon, target highlight) on a raw canvas, with drag-to-pan and wheel/pinch zoom. It redraws only on
// interaction/state change — no persistent animation loop — so it costs nothing when idle.
export function useSkyMapCanvas(opts: Opts) {
  const fovDeg = ref(50);
  let centerAlt = 45;
  let centerAz = 180;

  // Precomputed per (observer,data): each catalog star's alt/az. Pan/zoom only reproject — no recompute.
  let starAlt: Float32Array | null = null;
  let starAz: Float32Array | null = null;

  let ctx: CanvasRenderingContext2D | null = null;
  let cssW = 0;
  let cssH = 0;
  let raf = 0;

  function recomputeAltAz() {
    const d = opts.data.value;
    const obs = opts.observer();
    if (!d || !obs) {
      starAlt = starAz = null;
      return;
    }
    const n = d.stars.length;
    starAlt = new Float32Array(n);
    starAz = new Float32Array(n);
    for (let i = 0; i < n; i++) {
      const s = d.stars[i];
      const p = precessFromJ2000(s[0], s[1], obs.atMs);
      const h = equatorialToHorizontal(p.ra, p.dec, obs.lat, obs.lon, obs.atMs);
      starAlt[i] = h.alt;
      starAz[i] = h.az;
    }
  }

  // --- projection: stereographic about the view centre ------------------------------------------------
  interface Pt {
    x: number;
    y: number;
    front: boolean;
  }
  function project(altDeg: number, azDeg: number): Pt {
    const a = altDeg * DEG;
    const A = azDeg * DEG;
    const a0 = centerAlt * DEG;
    const A0 = centerAz * DEG;
    const dA = A - A0;
    const cosc =
      Math.sin(a0) * Math.sin(a) + Math.cos(a0) * Math.cos(a) * Math.cos(dA);
    if (cosc < -0.6) return { x: 0, y: 0, front: false }; // behind the view hemisphere
    const k = 2 / (1 + cosc);
    const x = k * Math.cos(a) * Math.sin(dA);
    const y =
      k *
      (Math.cos(a0) * Math.sin(a) - Math.sin(a0) * Math.cos(a) * Math.cos(dA));
    const scale = cssH / 2 / (2 * Math.tan((fovDeg.value / 4) * DEG));
    return { x: cssW / 2 + x * scale, y: cssH / 2 - y * scale, front: true };
  }
  const onScreen = (p: Pt) =>
    p.x > -40 && p.x < cssW + 40 && p.y > -40 && p.y < cssH + 40;

  // --- drawing ----------------------------------------------------------------------------------------
  function scheduleDraw() {
    if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = 0;
      draw();
    });
  }

  function draw() {
    if (!ctx) return;
    const c = ctx;
    const grad = c.createLinearGradient(0, 0, 0, cssH);
    grad.addColorStop(0, SKY_MAP.bgTop);
    grad.addColorStop(1, SKY_MAP.bgBottom);
    c.fillStyle = grad;
    c.fillRect(0, 0, cssW, cssH);

    drawHorizon(c);
    drawConstellations(c);
    drawStars(c);
    drawBodies(c);
    drawLabels(c);
    drawTarget(c);
  }

  function drawHorizon(c: CanvasRenderingContext2D) {
    c.strokeStyle = SKY_MAP.horizon;
    c.lineWidth = 1;
    c.beginPath();
    let started = false;
    for (let az = 0; az <= 360; az += 3) {
      const p = project(0, az);
      if (!p.front) {
        started = false;
        continue;
      }
      if (!started) {
        c.moveTo(p.x, p.y);
        started = true;
      } else c.lineTo(p.x, p.y);
    }
    c.stroke();
    c.fillStyle = SKY_MAP.cardinal;
    c.font = "600 12px system-ui, sans-serif";
    c.textAlign = "center";
    for (const [az, label] of [
      [0, "N"],
      [90, "E"],
      [180, "S"],
      [270, "W"],
    ] as const) {
      const p = project(0, az);
      if (p.front && onScreen(p)) c.fillText(label, p.x, p.y - 6);
    }
  }

  function drawConstellations(c: CanvasRenderingContext2D) {
    const d = opts.data.value;
    if (!d || !starAlt || !starAz) return;
    c.strokeStyle = SKY_MAP.line;
    c.lineWidth = 1;
    c.beginPath();
    for (const [i, j] of d.lines) {
      if (starAlt[i] <= 0 || starAlt[j] <= 0) continue; // don't dangle below the horizon
      const a = project(starAlt[i], starAz[i]);
      const b = project(starAlt[j], starAz[j]);
      if (!a.front || !b.front) continue;
      c.moveTo(a.x, a.y);
      c.lineTo(b.x, b.y);
    }
    c.stroke();
  }

  function drawStars(c: CanvasRenderingContext2D) {
    const d = opts.data.value;
    if (!d || !starAlt || !starAz) return;
    const fov = fovDeg.value;
    const limitMag = fov > 90 ? 4.2 : fov > 45 ? 5.2 : 6.0;
    c.fillStyle = SKY_MAP.star;
    for (let i = 0; i < d.stars.length; i++) {
      const alt = starAlt[i];
      if (alt <= 0) continue;
      const mag = d.stars[i][2];
      if (mag > limitMag) continue;
      const p = project(alt, starAz[i]);
      if (!p.front || !onScreen(p)) continue;
      const r = Math.max(0.6, 3.2 - mag * 0.45);
      c.globalAlpha = Math.max(0.25, Math.min(1, 1.1 - mag * 0.13));
      c.beginPath();
      c.arc(p.x, p.y, r, 0, Math.PI * 2);
      c.fill();
    }
    c.globalAlpha = 1;
  }

  function drawBodies(c: CanvasRenderingContext2D) {
    for (const b of opts.bodies()) {
      if (b.alt <= 0) continue; // (list is already above-horizon; guard anyway)
      const p = project(b.alt, b.az);
      if (!p.front || !onScreen(p)) continue;
      if (b.kind === "moon") drawMoon(c, p.x, p.y, b.phase ?? 1);
      else {
        c.fillStyle = SKY_MAP.planet;
        c.beginPath();
        c.arc(p.x, p.y, 3.2, 0, Math.PI * 2);
        c.fill();
      }
      c.fillStyle = b.kind === "moon" ? SKY_MAP.starLabel : SKY_MAP.planet;
      c.font = "11px system-ui, sans-serif";
      c.textAlign = "left";
      c.fillText(opts.bodyLabel(b.name), p.x + 7, p.y + 4);
    }
  }

  // A small moon disc with an approximate terminator for the illuminated fraction k.
  function drawMoon(
    c: CanvasRenderingContext2D,
    x: number,
    y: number,
    k: number,
  ) {
    const r = 6;
    c.save();
    c.beginPath();
    c.arc(x, y, r, 0, Math.PI * 2);
    c.clip();
    c.fillStyle = SKY_MAP.moonDark;
    c.fillRect(x - r, y - r, 2 * r, 2 * r);
    c.fillStyle = SKY_MAP.moon;
    const tx = r * (2 * k - 1); // terminator semi-axis: +r full, 0 half, -r new
    c.beginPath();
    c.arc(x, y, r, -Math.PI / 2, Math.PI / 2, false); // lit limb
    c.ellipse(x, y, Math.abs(tx), r, 0, Math.PI / 2, -Math.PI / 2, tx < 0);
    c.closePath();
    c.fill();
    c.restore();
  }

  function drawLabels(c: CanvasRenderingContext2D) {
    const d = opts.data.value;
    if (!d || !starAlt || !starAz) return;
    const fov = fovDeg.value;
    // Constellation names (sparse — always show those on-screen and above the horizon).
    c.fillStyle = SKY_MAP.conLabel;
    c.font = "italic 11px system-ui, sans-serif";
    c.textAlign = "center";
    for (const con of d.constellations) {
      const p0 = precessFromJ2000(con.ra, con.dec, opts.observer()?.atMs ?? 0);
      const obs = opts.observer();
      if (!obs) break;
      const h = equatorialToHorizontal(
        p0.ra,
        p0.dec,
        obs.lat,
        obs.lon,
        obs.atMs,
      );
      if (h.alt <= 3) continue;
      const p = project(h.alt, h.az);
      if (p.front && onScreen(p)) c.fillText(con.name.toUpperCase(), p.x, p.y);
    }
    // Named stars — only the brightest, more as you zoom in.
    const labelMag = fov > 90 ? 1.6 : fov > 45 ? 2.6 : 3.6;
    c.fillStyle = SKY_MAP.starLabel;
    c.font = "11px system-ui, sans-serif";
    c.textAlign = "left";
    for (const [idx, name] of d.names) {
      if (starAlt[idx] <= 0 || d.stars[idx][2] > labelMag) continue;
      const p = project(starAlt[idx], starAz[idx]);
      if (p.front && onScreen(p)) c.fillText(name, p.x + 5, p.y - 4);
    }
  }

  function drawTarget(c: CanvasRenderingContext2D) {
    const t = opts.target();
    if (!t) return;
    const p = project(t.alt, t.az);
    if (!p.front) return;
    const glow = c.createRadialGradient(p.x, p.y, 0, p.x, p.y, 16);
    glow.addColorStop(0, SKY_MAP.targetGlow);
    glow.addColorStop(1, "transparent");
    c.globalAlpha = 0.5;
    c.fillStyle = glow;
    c.beginPath();
    c.arc(p.x, p.y, 16, 0, Math.PI * 2);
    c.fill();
    c.globalAlpha = 1;
    c.strokeStyle = SKY_MAP.targetRing;
    c.lineWidth = 1.5;
    c.beginPath();
    c.arc(p.x, p.y, 9, 0, Math.PI * 2);
    c.stroke();
    c.fillStyle = SKY_MAP.targetCore;
    c.beginPath();
    c.arc(p.x, p.y, 4, 0, Math.PI * 2);
    c.fill();
    c.fillStyle = SKY_MAP.targetLabel;
    c.font = "600 13px system-ui, sans-serif";
    c.textAlign = "center";
    c.fillText(t.label, p.x, p.y - 14);
  }

  // --- view controls ----------------------------------------------------------------------------------
  function clampAlt(a: number) {
    return Math.max(-89, Math.min(89, a));
  }
  function wrapAz(a: number) {
    a = a % 360;
    return a < 0 ? a + 360 : a;
  }
  function zoomBy(factor: number) {
    fovDeg.value = Math.max(FOV_MIN, Math.min(FOV_MAX, fovDeg.value * factor));
    scheduleDraw();
  }
  function resetToTarget() {
    const t = opts.target();
    if (t) {
      centerAlt = clampAlt(t.alt);
      centerAz = t.az;
    }
    fovDeg.value = 50;
    scheduleDraw();
  }
  function wholeSky() {
    centerAlt = 55;
    fovDeg.value = FOV_MAX;
    scheduleDraw();
  }

  // --- interaction ------------------------------------------------------------------------------------
  const pointers = new Map<number, { x: number; y: number }>();
  let pinchDist = 0;

  function onPointerDown(e: PointerEvent) {
    (e.target as HTMLElement).setPointerCapture?.(e.pointerId);
    pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pointers.size === 2) pinchDist = pointerSpread();
  }
  function onPointerMove(e: PointerEvent) {
    const prev = pointers.get(e.pointerId);
    if (!prev) return;
    if (pointers.size === 2) {
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
      const d = pointerSpread();
      if (pinchDist > 0 && d > 0) zoomBy(pinchDist / d);
      pinchDist = d;
      return;
    }
    const dx = e.clientX - prev.x;
    const dy = e.clientY - prev.y;
    pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const degPerPx = fovDeg.value / cssH;
    centerAlt = clampAlt(centerAlt - dy * degPerPx);
    centerAz = wrapAz(
      centerAz - (dx * degPerPx) / Math.max(0.2, Math.cos(centerAlt * DEG)),
    );
    scheduleDraw();
  }
  function onPointerUp(e: PointerEvent) {
    pointers.delete(e.pointerId);
    if (pointers.size < 2) pinchDist = 0;
  }
  function pointerSpread(): number {
    const pts = [...pointers.values()];
    if (pts.length < 2) return 0;
    return Math.hypot(pts[0].x - pts[1].x, pts[0].y - pts[1].y);
  }
  function onWheel(e: WheelEvent) {
    e.preventDefault();
    zoomBy(e.deltaY > 0 ? 1.12 : 1 / 1.12);
  }

  // --- lifecycle --------------------------------------------------------------------------------------
  let ro: ResizeObserver | null = null;
  function resize() {
    const el = opts.canvas.value;
    if (!el) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const rect = el.getBoundingClientRect();
    cssW = rect.width;
    cssH = rect.height;
    el.width = Math.round(cssW * dpr);
    el.height = Math.round(cssH * dpr);
    ctx = el.getContext("2d");
    ctx?.setTransform(dpr, 0, 0, dpr, 0, 0);
    scheduleDraw();
  }

  onMounted(() => {
    const el = opts.canvas.value;
    if (!el) return;
    resize();
    ro = new ResizeObserver(() => resize());
    ro.observe(el);
    el.addEventListener("pointerdown", onPointerDown);
    el.addEventListener("pointermove", onPointerMove);
    el.addEventListener("pointerup", onPointerUp);
    el.addEventListener("pointercancel", onPointerUp);
    el.addEventListener("wheel", onWheel, { passive: false });
    recomputeAltAz();
    resetToTarget();
  });
  onBeforeUnmount(() => {
    ro?.disconnect();
    const el = opts.canvas.value;
    el?.removeEventListener("pointerdown", onPointerDown);
    el?.removeEventListener("pointermove", onPointerMove);
    el?.removeEventListener("pointerup", onPointerUp);
    el?.removeEventListener("pointercancel", onPointerUp);
    el?.removeEventListener("wheel", onWheel);
    if (raf) cancelAnimationFrame(raf);
  });

  // Recompute star alt/az when the dataset or observer changes; re-centre when the target star changes.
  watch(
    () => [opts.data.value, opts.observer()?.atMs, opts.observer()?.lat],
    () => {
      recomputeAltAz();
      scheduleDraw();
    },
  );
  watch(
    () => opts.target()?.label,
    () => resetToTarget(),
  );
  watch(() => opts.bodies(), scheduleDraw, { deep: true });

  return { fovDeg, zoomBy, resetToTarget, wholeSky };
}
