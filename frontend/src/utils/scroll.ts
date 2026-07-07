// Smooth window-scroll helpers. We roll our own rAF animation instead of
// `element.scrollIntoView({ behavior: "smooth" })` because the native smooth scroll gives no
// control over duration — and we want a deliberately quick, consistent motion.

const easeInOutCubic = (t: number): number =>
  t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;

function prefersReducedMotion(): boolean {
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/**
 * Scroll the window so `el` sits at the top of the viewport, `offset` px below the very top
 * (pass the sticky-header height so the element lands just under it). The motion is a quick
 * easeInOutCubic over `durationMs`; `prefers-reduced-motion` jumps instantly.
 */
export function scrollElementToTop(
  el: HTMLElement,
  offset = 0,
  durationMs = 450,
): void {
  const startY = window.scrollY;
  const targetY = Math.max(0, startY + el.getBoundingClientRect().top - offset);
  const delta = targetY - startY;
  if (Math.abs(delta) < 1) return;

  if (prefersReducedMotion() || durationMs <= 0) {
    window.scrollTo(0, targetY);
    return;
  }

  let startTime: number | null = null;
  const step = (now: number) => {
    if (startTime === null) startTime = now;
    const progress = Math.min(1, (now - startTime) / durationMs);
    window.scrollTo(0, startY + delta * easeInOutCubic(progress));
    if (progress < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}
