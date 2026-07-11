import { onBeforeUnmount, onMounted, watch, type Ref } from "vue";
import { NIGHT_SKY } from "@/constants/colors";

// useNightSky drives a subtle, elegant animated night-sky on a full-viewport canvas:
// a sparse field of slowly-twinkling, naturally-tinted stars plus a slow comet that drifts past.
// It is decorative only.
//
// - Dark mode: faint stars with subtle spectral tints (orange/blue/yellow/white) at varied sizes,
//   plus a comet every ~20–35s that enters from a random edge, crosses through the center, and
//   stays fully visible until its head AND tail have left a different edge ("just passing by"),
//   and a fast shooting star (meteor) every ~45–90s that flares in mid-sky and burns out where it dies.
// - Light mode: a barely-there grey starfield, no comets or meteors, minimal twinkle.
// - prefers-reduced-motion: a single static frame, no RAF loop, no comets or meteors.
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
  color: string; // baked once at generation (dark = spectral tint, light = faint grey)
  feature: boolean; // bright feature star → larger radius + faint halo (dark only)
}
interface Comet {
  x: number;
  y: number;
  vx: number;
  vy: number; // px/ms
  len: number; // tail length, px
  alpha: number; // 0→1 fade-IN only (never fades out mid-screen)
  ttl: number; // ms — safety cap only; never reached in a normal crossing
  entered: boolean; // head has been inside the viewport at least once
  scheme: number; // index into NIGHT_SKY.cometSchemes
}
// A shooting star: a fast, short-lived streak that flares and burns out in mid-sky (it does NOT
// cross the screen like the comet). Brightness follows an ablation light-curve — a quick rise,
// scintillating flicker, occasional bright flares, then a fade to nothing as it disintegrates.
interface Meteor {
  x: number;
  y: number;
  vx: number;
  vy: number; // px/ms — fast
  len: number; // train (wake) length, px
  life: number; // ms elapsed
  dur: number; // ms total lifetime (short)
  scheme: number; // index into NIGHT_SKY.meteorSchemes
  flares: number[]; // life-fractions (0..1) where brief brightness bursts peak
  seed: number; // flicker phase
}

const rand = (a: number, b: number) => a + Math.random() * (b - a);
const TAU = Math.PI * 2;
const pick = <T>(a: readonly T[]): T => a[(Math.random() * a.length) | 0];
// pickStarTint draws a spectral tint with a realistic distribution — most stars near-white, a
// minority warm/blue — so the field reads as natural rather than uniformly silver.
function pickStarTint(): string {
  const r = Math.random();
  if (r < 0.65) return pick(NIGHT_SKY.starWhite); // ~65% white / blue-white
  if (r < 0.82) return pick(NIGHT_SKY.starWarm); // ~17% warm orange (K)
  if (r < 0.93) return pick(NIGHT_SKY.starYellow); // ~11% yellow-white (G)
  return pick(NIGHT_SKY.starBlue); // ~7% distinct blue (B)
}

// FRAME_MIN_MS throttles the loop to ~30fps — imperceptible for a slow starfield/comet, half the work.
const FRAME_MIN_MS = 1000 / 30;

// active is an optional reactive predicate: while it returns false (e.g. on a live-work route) the loop
// is stopped, composed with the tab-visibility pause. Omitted → always run when visible.
export function useNightSky(
  canvasRef: Ref<HTMLCanvasElement | null>,
  isDark: () => boolean,
  active?: () => boolean,
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
  let meteor: Meteor | null = null;
  let nextMeteorAt = 0;
  let raf = 0;
  let last = 0;
  let lastDraw = 0; // ts of the last drawn frame, for the fps throttle
  let ro: ResizeObserver | null = null;
  let running = false;

  function generate() {
    const dark = isDark();
    const count = Math.max(40, Math.min(130, Math.round((w * h) / 16000)));
    stars = Array.from({ length: count }, () => {
      const feature = dark && Math.random() < 0.06; // ~6% bright feature stars, dark only
      const r = feature ? rand(1.9, 3.0) : rand(0.4, 1.7); // widened size range
      let baseAlpha: number;
      if (!dark)
        baseAlpha = rand(0.05, 0.14); // light mode unchanged
      else if (feature) baseAlpha = rand(0.7, 0.95);
      else baseAlpha = rand(0.22, 0.62);
      return {
        x: Math.random() * w,
        y: Math.random() * h,
        r,
        baseAlpha,
        phase: Math.random() * TAU,
        speed: rand(0.0003, 0.0009), // period ≈ 7–21 s
        // Color is baked once (theme is known here; watch(isDark) re-runs generate() on toggle),
        // so the per-frame draw just reads s.color. Light mode = single faint grey.
        color: dark ? pickStarTint() : NIGHT_SKY.starLight,
        feature,
      };
    });
    comet = null;
    nextCometAt = 0;
    meteor = null;
    nextMeteorAt = 0;
  }

  // drawStar renders one star (plus a faint halo for feature stars). Shared by the animated frame
  // and the static (reduced-motion) frame. Kept allocation-free — no per-star gradients.
  function drawStar(s: Star, base: number) {
    if (!ctx) return;
    ctx.fillStyle = s.color;
    if (s.feature) {
      ctx.globalAlpha = Math.max(0, base * 0.16);
      ctx.beginPath();
      ctx.arc(s.x, s.y, s.r * 2.6, 0, TAU);
      ctx.fill();
    }
    ctx.globalAlpha = Math.max(0, base);
    ctx.beginPath();
    ctx.arc(s.x, s.y, s.r, 0, TAU);
    ctx.fill();
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
    for (const s of stars) drawStar(s, s.baseAlpha);
    ctx.globalAlpha = 1;
  }

  // spawnComet launches a comet from just outside a random edge, aimed at a jittered point in the
  // central region. The spawn point is exterior and the target interior, so the velocity component
  // normal to the entry edge is constant and inward — the head moves monotonically away from that
  // edge and must exit through a different one. len is clamped < 0.4·min(w,h) so the tail can never
  // span the viewport (keeps the "both ends out ⇒ fully gone" despawn check correct on small screens).
  function spawnComet() {
    const M = 64; // spawn this far outside the edge (head starts off-screen)
    const edge = (Math.random() * 4) | 0; // 0=L 1=R 2=T 3=B
    let sx: number;
    let sy: number;
    if (edge === 0) {
      sx = -M;
      sy = rand(h * 0.12, h * 0.88);
    } else if (edge === 1) {
      sx = w + M;
      sy = rand(h * 0.12, h * 0.88);
    } else if (edge === 2) {
      sx = rand(w * 0.12, w * 0.88);
      sy = -M;
    } else {
      sx = rand(w * 0.12, w * 0.88);
      sy = h + M;
    }
    const tx = w / 2 + rand(-w * 0.18, w * 0.18); // jittered central target
    const ty = h / 2 + rand(-h * 0.18, h * 0.18);
    const dx = tx - sx;
    const dy = ty - sy;
    const dist = Math.hypot(dx, dy) || 1;
    const speed = rand(0.15, 0.25); // px/ms → ~8–15 s to cross a 1080p screen
    const len = Math.min(rand(90, 200), Math.min(w, h) * 0.4);
    comet = {
      x: sx,
      y: sy,
      vx: (dx / dist) * speed,
      vy: (dy / dist) * speed,
      len,
      alpha: 0,
      ttl: (Math.hypot(w, h) / speed) * 1.7 + 3000, // safety only; scales with screen size
      entered: false,
      scheme: (Math.random() * NIGHT_SKY.cometSchemes.length) | 0,
    };
  }

  // spawnMeteor launches a shooting star high in the sky on a steep downward path. It is fast and
  // short-lived: it travels only a fraction of the screen (it burns out, it does not cross it).
  function spawnMeteor() {
    const minDim = Math.min(w, h);
    const ang = rand(Math.PI * 0.15, Math.PI * 0.85); // always downward; full left↔right spread
    const travel = rand(0.28, 0.6) * minDim; // distance the head streaks before burning out
    const dur = rand(380, 900); // ms — very brief (the "very fast" feel)
    const speed = travel / dur; // px/ms
    const r = Math.random(); // mostly blue-white, fewer green/yellow (realistic ablation colours)
    const scheme = r < 0.6 ? 0 : r < 0.82 ? 1 : 2;
    const flares: number[] = [];
    if (Math.random() < 0.7) flares.push(rand(0.25, 0.7));
    if (Math.random() < 0.33) flares.push(rand(0.15, 0.85));
    meteor = {
      x: rand(w * 0.12, w * 0.88),
      y: rand(h * 0.06, h * 0.5), // start in the upper sky
      vx: Math.cos(ang) * speed,
      vy: Math.sin(ang) * speed,
      len: Math.min(rand(70, 160), travel * 0.5),
      life: 0,
      dur,
      scheme,
      flares,
      seed: Math.random() * TAU,
    };
  }

  // meteorBrightness is the ablation light-curve at life-fraction p (0..1): a fast rise, a tumbling
  // scintillation, plus brief flares that can spike well above 1 (fireball bursts), all fading to 0.
  function meteorBrightness(m: Meteor, p: number): number {
    const env = Math.min(1, p / 0.1) * Math.pow(1 - p, 0.6); // quick brighten → ablation fade-out
    const flicker =
      0.8 + 0.2 * Math.sin(p * 34 + m.seed) * Math.sin(p * 11 + m.seed * 1.7);
    let b = env * flicker;
    for (const tf of m.flares) {
      const d = (p - tf) / 0.06; // narrow Gaussian burst
      b += 1.15 * Math.exp(-d * d);
    }
    return b;
  }

  // drawMeteor renders the wake + flaring head additively. burst (brightness over 1) swells the
  // head glow and widens the streak, so a flare visibly bursts.
  function drawMeteor(m: Meteor) {
    if (!ctx) return;
    const b = meteorBrightness(m, m.life / m.dur);
    const a = Math.max(0, Math.min(1, b));
    const burst = Math.max(0, b - 1);
    const sp = Math.hypot(m.vx, m.vy) || 1;
    const tailX = m.x - (m.vx / sp) * m.len;
    const tailY = m.y - (m.vy / sp) * m.len;
    const sc = NIGHT_SKY.meteorSchemes[m.scheme];

    ctx.save();
    ctx.globalCompositeOperation = "lighter";
    const grad = ctx.createLinearGradient(m.x, m.y, tailX, tailY);
    grad.addColorStop(0, sc.core);
    grad.addColorStop(0.3, sc.train);
    grad.addColorStop(1, "transparent");
    ctx.globalAlpha = a * 0.75;
    ctx.strokeStyle = grad;
    ctx.lineWidth = 1.4 + burst * 1.2;
    ctx.lineCap = "round";
    ctx.beginPath();
    ctx.moveTo(tailX, tailY);
    ctx.lineTo(m.x, m.y);
    ctx.stroke();
    const glowR = 2.5 + burst * 9;
    const cg = ctx.createRadialGradient(m.x, m.y, 0, m.x, m.y, glowR);
    cg.addColorStop(0, sc.glow);
    cg.addColorStop(1, "transparent");
    ctx.globalAlpha = a * 0.9;
    ctx.fillStyle = cg;
    ctx.beginPath();
    ctx.arc(m.x, m.y, glowR, 0, TAU);
    ctx.fill();
    ctx.restore();
    // Crisp hot head — swells slightly on a flare.
    ctx.globalAlpha = a;
    ctx.fillStyle = sc.core;
    ctx.beginPath();
    ctx.arc(m.x, m.y, 1.1 + burst * 1.3, 0, TAU);
    ctx.fill();
    ctx.globalAlpha = 1;
  }

  function frame(ts: number) {
    if (!ctx) {
      raf = requestAnimationFrame(frame);
      return;
    }
    // Throttle to ~30fps: skip drawing this frame if too soon, but keep the loop alive.
    if (ts - lastDraw < FRAME_MIN_MS) {
      raf = requestAnimationFrame(frame);
      return;
    }
    lastDraw = ts;
    if (!last) last = ts;
    const dt = Math.min(ts - last, 50);
    last = ts;
    const dark = isDark();

    drawSky(dark);

    // Stars — gentle low-amplitude twinkle.
    for (const s of stars) {
      const tw = 0.75 + 0.25 * Math.sin(ts * s.speed + s.phase);
      drawStar(s, s.baseAlpha * tw);
    }
    ctx.globalAlpha = 1;

    // Comet — dark mode only, one at a time. Enters from a random edge, crosses the center, and
    // stays fully visible until head AND tail have left a different edge ("just passing by").
    if (dark) {
      if (!comet && ts >= nextCometAt) {
        // First comet soon after load (~4–10 s); the steady ~20–35 s gap is set on despawn below.
        if (nextCometAt === 0) nextCometAt = ts + rand(4000, 10000);
        else spawnComet();
      }
      if (comet) {
        comet.x += comet.vx * dt;
        comet.y += comet.vy * dt;
        comet.ttl -= dt;
        comet.alpha = Math.min(1, comet.alpha + dt / 600); // fade-IN only — never mid-screen
        const sp = Math.hypot(comet.vx, comet.vy) || 1;
        const tailX = comet.x - (comet.vx / sp) * comet.len;
        const tailY = comet.y - (comet.vy / sp) * comet.len;
        // Mark "entered" once the head is inside — gates the despawn so the pre-entry approach
        // (both ends also outside, on the entry edge) doesn't despawn it immediately.
        if (
          !comet.entered &&
          comet.x >= 0 &&
          comet.x <= w &&
          comet.y >= 0 &&
          comet.y <= h
        ) {
          comet.entered = true;
        }

        const sc = NIGHT_SKY.cometSchemes[comet.scheme];
        const op = comet.alpha;
        // Tail + coma glow additively (luminous on the near-black sky); core stays crisp on top.
        ctx.save();
        ctx.globalCompositeOperation = "lighter";
        const grad = ctx.createLinearGradient(comet.x, comet.y, tailX, tailY);
        grad.addColorStop(0, sc.tailHead);
        grad.addColorStop(0.45, sc.tail);
        grad.addColorStop(1, "transparent");
        ctx.globalAlpha = op * 0.6;
        ctx.strokeStyle = grad;
        ctx.lineWidth = 1.8;
        ctx.lineCap = "round";
        ctx.beginPath();
        ctx.moveTo(tailX, tailY);
        ctx.lineTo(comet.x, comet.y);
        ctx.stroke();
        const comaR = 7 + comet.len * 0.05;
        const cg = ctx.createRadialGradient(
          comet.x,
          comet.y,
          0,
          comet.x,
          comet.y,
          comaR,
        );
        cg.addColorStop(0, sc.coma);
        cg.addColorStop(1, "transparent");
        ctx.globalAlpha = op * 0.8;
        ctx.fillStyle = cg;
        ctx.beginPath();
        ctx.arc(comet.x, comet.y, comaR, 0, TAU);
        ctx.fill();
        ctx.restore();
        // Bright near-white nucleus.
        ctx.globalAlpha = op;
        ctx.fillStyle = sc.core;
        ctx.beginPath();
        ctx.arc(comet.x, comet.y, 1.8, 0, TAU);
        ctx.fill();
        ctx.globalAlpha = 1;

        // Despawn only once the whole comet (head AND trailing tail tip) has left the viewport.
        const P = 2;
        const headOut =
          comet.x < -P || comet.x > w + P || comet.y < -P || comet.y > h + P;
        const tailOut =
          tailX < -P || tailX > w + P || tailY < -P || tailY > h + P;
        if ((comet.entered && headOut && tailOut) || comet.ttl <= 0) {
          comet = null;
          nextCometAt = ts + rand(20000, 35000); // lively cadence (~20–35 s between comets)
        }
      }

      // Shooting star (meteor) — much faster and rarer than the comet; one at a time. It appears in
      // mid-sky, streaks briefly while flaring, then burns out (fades) where it is.
      if (!meteor && ts >= nextMeteorAt) {
        if (nextMeteorAt === 0)
          nextMeteorAt = ts + rand(8000, 25000); // first one a bit after load
        else spawnMeteor();
      }
      if (meteor) {
        meteor.life += dt;
        meteor.x += meteor.vx * dt;
        meteor.y += meteor.vy * dt;
        if (meteor.life >= meteor.dur) {
          meteor = null;
          nextMeteorAt = ts + rand(45000, 90000); // 45 s – 1:30 between shooting stars
        } else {
          drawMeteor(meteor);
        }
      }
    } else {
      comet = null;
      meteor = null;
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
  // Run only when the tab is visible AND the caller's predicate (route) allows it.
  function shouldRun(): boolean {
    return !document.hidden && (active ? active() : true);
  }
  function apply() {
    if (shouldRun()) start();
    else stop();
  }
  function onVisibility() {
    apply();
  }

  onMounted(() => {
    resize();
    ro = new ResizeObserver(() => resize());
    if (canvasRef.value) ro.observe(canvasRef.value);
    document.addEventListener("visibilitychange", onVisibility);
    if (active) watch(active, apply);
    apply();
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
