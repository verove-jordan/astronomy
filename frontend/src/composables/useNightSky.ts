import { onBeforeUnmount, onMounted, watch, type Ref } from "vue";
import { NIGHT_SKY } from "@/constants/colors";

// useNightSky drives a subtle, elegant animated night-sky on a full-viewport canvas:
// a sparse field of slowly-twinkling stars plus a rare, slow comet. It is decorative only.
//
// - Dark mode: faint silver stars (up to ~0.7 alpha) + an occasional comet (~once a minute).
// - Light mode: a barely-there grey starfield, no comets, minimal twinkle.
// - prefers-reduced-motion: a single static frame, no RAF loop, no comets.
// - Pauses while the tab is hidden; recomputes density on resize; DPR-aware (capped at 2).
//
// Colors come from constants/colors.ts (the sanctioned JS mirror) — a canvas can't use classes.
interface Star {
  x: number;
  y: number;
  r: number;
  baseAlpha: number;
  phase: number;
  speed: number; // rad/ms — slow twinkle
  warm: boolean;
}
interface Comet {
  x: number;
  y: number;
  vx: number;
  vy: number; // px/ms
  len: number;
  alpha: number;
  ttl: number; // ms remaining
}

const rand = (a: number, b: number) => a + Math.random() * (b - a);

export function useNightSky(
  canvasRef: Ref<HTMLCanvasElement | null>,
  isDark: () => boolean,
) {
  const reduced =
    typeof window !== "undefined" &&
    !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

  let ctx: CanvasRenderingContext2D | null = null;
  let w = 0;
  let h = 0; // CSS px
  let stars: Star[] = [];
  let comet: Comet | null = null;
  let nextCometAt = 0;
  let raf = 0;
  let last = 0;
  let ro: ResizeObserver | null = null;
  let running = false;

  function generate() {
    const dark = isDark();
    const count = Math.max(40, Math.min(130, Math.round((w * h) / 16000)));
    stars = Array.from({ length: count }, () => ({
      x: Math.random() * w,
      y: Math.random() * h,
      r: rand(0.4, 1.4),
      baseAlpha: dark ? rand(0.25, 0.7) : rand(0.05, 0.14),
      phase: Math.random() * Math.PI * 2,
      speed: rand(0.0003, 0.0009), // period ≈ 7–21 s
      warm: Math.random() < 0.15,
    }));
    comet = null;
    nextCometAt = 0;
  }

  function starColor(s: Star, dark: boolean): string {
    if (!dark) return NIGHT_SKY.starLight;
    if (s.warm) return NIGHT_SKY.starWarm;
    return s.r > 1.0 ? NIGHT_SKY.starCore : NIGHT_SKY.starDim;
  }

  function resize() {
    const cv = canvasRef.value;
    if (!cv) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    w = cv.clientWidth;
    h = cv.clientHeight;
    cv.width = Math.round(w * dpr);
    cv.height = Math.round(h * dpr);
    ctx = cv.getContext("2d");
    ctx?.setTransform(dpr, 0, 0, dpr, 0, 0);
    generate();
    if (reduced || !running) drawStatic();
  }

  // Clears the canvas and, in dark mode, lays down a subtle deep-space lift from the top for depth.
  function drawSky(dark: boolean) {
    if (!ctx) return;
    ctx.clearRect(0, 0, w, h);
    if (!dark) return;
    const g = ctx.createRadialGradient(
      w / 2,
      -h * 0.15,
      0,
      w / 2,
      -h * 0.15,
      Math.max(w, h) * 1.15,
    );
    g.addColorStop(0, NIGHT_SKY.skyGlow);
    g.addColorStop(1, "transparent");
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, w, h);
  }

  function drawStatic() {
    if (!ctx) return;
    const dark = isDark();
    drawSky(dark);
    for (const s of stars) {
      ctx.globalAlpha = s.baseAlpha;
      ctx.fillStyle = starColor(s, dark);
      ctx.beginPath();
      ctx.arc(s.x, s.y, s.r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
  }

  function spawnComet() {
    const fromLeft = Math.random() < 0.5;
    const x = fromLeft ? -40 : rand(0, w * 0.6);
    const y = fromLeft ? rand(0, h * 0.5) : -40;
    const angle = rand(Math.PI * 0.12, Math.PI * 0.3); // shallow, down-right
    const speed = rand(0.04, 0.08); // px/ms — slow, crosses over several seconds
    comet = {
      x,
      y,
      vx: Math.cos(angle) * speed,
      vy: Math.sin(angle) * speed,
      len: rand(80, 160),
      alpha: 0,
      ttl: rand(6000, 10000),
    };
  }

  function frame(ts: number) {
    if (!ctx) {
      raf = requestAnimationFrame(frame);
      return;
    }
    if (!last) last = ts;
    const dt = Math.min(ts - last, 50);
    last = ts;
    const dark = isDark();

    drawSky(dark);

    // Stars — gentle low-amplitude twinkle.
    for (const s of stars) {
      const tw = 0.75 + 0.25 * Math.sin(ts * s.speed + s.phase);
      ctx.globalAlpha = Math.max(0, s.baseAlpha * tw);
      ctx.fillStyle = starColor(s, dark);
      ctx.beginPath();
      ctx.arc(s.x, s.y, s.r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;

    // Comet — dark mode only, one at a time, rare.
    if (dark) {
      if (!comet && ts >= nextCometAt) {
        if (nextCometAt === 0) nextCometAt = ts + rand(8000, 20000);
        else spawnComet();
      }
      if (comet) {
        comet.x += comet.vx * dt;
        comet.y += comet.vy * dt;
        comet.ttl -= dt;
        comet.alpha = Math.min(1, comet.alpha + dt / 600);
        const fade = comet.ttl < 1200 ? Math.max(0, comet.ttl / 1200) : 1;
        const speed = Math.hypot(comet.vx, comet.vy) || 1;
        const tailX = comet.x - (comet.vx / speed) * comet.len;
        const tailY = comet.y - (comet.vy / speed) * comet.len;
        const grad = ctx.createLinearGradient(comet.x, comet.y, tailX, tailY);
        grad.addColorStop(0, NIGHT_SKY.cometHead);
        grad.addColorStop(1, "transparent");
        ctx.globalAlpha = comet.alpha * fade * 0.6;
        ctx.strokeStyle = grad;
        ctx.lineWidth = 1.6;
        ctx.lineCap = "round";
        ctx.beginPath();
        ctx.moveTo(tailX, tailY);
        ctx.lineTo(comet.x, comet.y);
        ctx.stroke();
        ctx.fillStyle = NIGHT_SKY.cometHead;
        ctx.beginPath();
        ctx.arc(comet.x, comet.y, 1.5, 0, Math.PI * 2);
        ctx.fill();
        ctx.globalAlpha = 1;
        if (comet.x > w + 60 || comet.y > h + 60 || comet.ttl <= 0) {
          comet = null;
          nextCometAt = ts + rand(45000, 80000); // ~ once a minute
        }
      }
    } else {
      comet = null;
    }

    raf = requestAnimationFrame(frame);
  }

  function start() {
    if (reduced) {
      drawStatic();
      return;
    }
    if (running) return;
    running = true;
    last = 0;
    raf = requestAnimationFrame(frame);
  }
  function stop() {
    running = false;
    if (raf) cancelAnimationFrame(raf);
    raf = 0;
  }
  function onVisibility() {
    if (document.hidden) stop();
    else start();
  }

  onMounted(() => {
    resize();
    ro = new ResizeObserver(() => resize());
    if (canvasRef.value) ro.observe(canvasRef.value);
    document.addEventListener("visibilitychange", onVisibility);
    start();
  });

  // Theme toggle: rebuild the field (alpha ranges differ) and re-enable/disable comets.
  watch(isDark, () => {
    generate();
    if (reduced || !running) drawStatic();
  });

  onBeforeUnmount(() => {
    stop();
    ro?.disconnect();
    ro = null;
    document.removeEventListener("visibilitychange", onVisibility);
  });
}
