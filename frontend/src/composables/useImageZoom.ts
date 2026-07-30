import { computed, onBeforeUnmount, onMounted, ref, type Ref } from "vue";

// useImageZoom is a dependency-free pan/zoom engine over a container element: trackpad pinch
// (ctrl+wheel) and the toolbar zoom about a point, two-finger/drag pans, with bounds clamping,
// fit/reset, keyboard control, a reduced-motion transition class, and a normalized viewport rect for
// the navigator minimap. The image uses transform-origin:0 0 and the returned `transform`.

export interface ZoomOptions {
  // maxZoomFactor caps magnification as a multiple of ACTUAL SIZE (1 image pixel per screen pixel).
  // Browsing a finished image rarely wants more than a few times actual size, but focusing a
  // telescope does: judging whether a star is tight means filling the view with a handful of sensor
  // pixels. Default keeps the file viewer's existing behaviour.
  maxZoomFactor?: number;
}

export function useImageZoom(
  container: Ref<HTMLElement | null>,
  opts: ZoomOptions = {},
) {
  const scale = ref(1);
  const tx = ref(0);
  const ty = ref(0);
  const natW = ref(0);
  const natH = ref(0);
  const cw = ref(0);
  const ch = ref(0);
  const minScale = ref(0.05);
  const maxScale = ref(8);

  const reduced =
    typeof window !== "undefined" &&
    !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  let ro: ResizeObserver | null = null;

  function measure() {
    const el = container.value;
    if (!el) return;
    cw.value = el.clientWidth;
    ch.value = el.clientHeight;
  }

  function recomputeRange() {
    if (!natW.value || !cw.value) return;
    const fit = Math.min(cw.value / natW.value, ch.value / natH.value);
    minScale.value = fit > 0 ? fit : 0.05;
    // The ceiling is expressed as a multiple of actual size (scale 1 = one image pixel per screen
    // pixel), so it means the same thing whatever the image's dimensions — a 1024 px preview and a
    // 4656 px sensor frame both stop at the same real magnification.
    const ceiling = opts.maxZoomFactor ?? 1;
    maxScale.value = Math.max(
      minScale.value,
      Math.min(
        Math.max(16, ceiling),
        Math.max(4 * ceiling, (natW.value / cw.value) * 2 * ceiling),
      ),
    );
  }

  function clampTranslation() {
    const sw = natW.value * scale.value;
    const sh = natH.value * scale.value;
    tx.value =
      sw <= cw.value
        ? (cw.value - sw) / 2
        : Math.min(0, Math.max(cw.value - sw, tx.value));
    ty.value =
      sh <= ch.value
        ? (ch.value - sh) / 2
        : Math.min(0, Math.max(ch.value - sh, ty.value));
  }

  function fit() {
    measure();
    recomputeRange();
    scale.value = minScale.value;
    clampTranslation();
  }
  const reset = fit;

  function setNatural(w: number, h: number) {
    natW.value = w;
    natH.value = h;
    fit();
  }

  function localPoint(e: { clientX: number; clientY: number }) {
    const rect = container.value?.getBoundingClientRect();
    return {
      x: e.clientX - (rect?.left ?? 0),
      y: e.clientY - (rect?.top ?? 0),
    };
  }

  function zoomTo(target: number, cx: number, cy: number) {
    const s = Math.min(maxScale.value, Math.max(minScale.value, target));
    const ix = (cx - tx.value) / scale.value;
    const iy = (cy - ty.value) / scale.value;
    scale.value = s;
    tx.value = cx - ix * s;
    ty.value = cy - iy * s;
    clampTranslation();
  }

  function zoomBy(factor: number) {
    zoomTo(scale.value * factor, cw.value / 2, ch.value / 2);
  }
  function actualSize() {
    zoomTo(1, cw.value / 2, ch.value / 2);
  }

  function onWheel(e: WheelEvent) {
    e.preventDefault();
    const { x, y } = localPoint(e);
    if (e.ctrlKey) {
      zoomTo(scale.value * Math.exp(-e.deltaY * 0.01), x, y); // trackpad pinch / ctrl+wheel
    } else {
      tx.value -= e.deltaX;
      ty.value -= e.deltaY;
      clampTranslation();
    }
  }

  let dragging = false;
  let lastX = 0;
  let lastY = 0;
  function onPointerDown(e: PointerEvent) {
    dragging = true;
    lastX = e.clientX;
    lastY = e.clientY;
    (e.target as Element).setPointerCapture?.(e.pointerId);
  }
  function onPointerMove(e: PointerEvent) {
    if (!dragging) return;
    tx.value += e.clientX - lastX;
    ty.value += e.clientY - lastY;
    lastX = e.clientX;
    lastY = e.clientY;
    clampTranslation();
  }
  function onPointerUp(e: PointerEvent) {
    dragging = false;
    (e.target as Element).releasePointerCapture?.(e.pointerId);
  }

  function onDblClick(e: MouseEvent) {
    const { x, y } = localPoint(e);
    if (scale.value > minScale.value + 1e-3) fit();
    else zoomTo(minScale.value * 2.5, x, y);
  }

  function onKey(e: KeyboardEvent) {
    const step = 40;
    switch (e.key) {
      case "+":
      case "=":
        zoomBy(1.2);
        break;
      case "-":
        zoomBy(1 / 1.2);
        break;
      case "0":
        fit();
        break;
      case "ArrowLeft":
        tx.value += step;
        clampTranslation();
        break;
      case "ArrowRight":
        tx.value -= step;
        clampTranslation();
        break;
      case "ArrowUp":
        ty.value += step;
        clampTranslation();
        break;
      case "ArrowDown":
        ty.value -= step;
        clampTranslation();
        break;
      default:
        return;
    }
    e.preventDefault();
  }

  // centerOnNorm recenters the viewport on a normalized image point (used by the navigator).
  function centerOnNorm(nx: number, ny: number) {
    tx.value = cw.value / 2 - nx * natW.value * scale.value;
    ty.value = ch.value / 2 - ny * natH.value * scale.value;
    clampTranslation();
  }

  onMounted(() => {
    measure();
    ro = new ResizeObserver(() => {
      measure();
      recomputeRange();
      clampTranslation();
    });
    const el = container.value;
    if (el) {
      ro.observe(el);
      el.addEventListener("wheel", onWheel, { passive: false }); // wheel must be non-passive to preventDefault
    }
  });
  onBeforeUnmount(() => {
    ro?.disconnect();
    container.value?.removeEventListener("wheel", onWheel);
  });

  const transform = computed(
    () => `translate(${tx.value}px, ${ty.value}px) scale(${scale.value})`,
  );
  const transitionClass = computed(() =>
    reduced ? "" : "motion-safe:transition-transform motion-safe:duration-150",
  );
  const zoomPercent = computed(() => Math.round(scale.value * 100));
  const canZoom = computed(() => scale.value > minScale.value + 1e-3);

  const viewport = computed(() => {
    if (!natW.value) return { x: 0, y: 0, w: 1, h: 1 };
    return {
      x: Math.max(0, -tx.value / scale.value / natW.value),
      y: Math.max(0, -ty.value / scale.value / natH.value),
      w: Math.min(1, cw.value / scale.value / natW.value),
      h: Math.min(1, ch.value / scale.value / natH.value),
    };
  });

  return {
    transform,
    transitionClass,
    zoomPercent,
    canZoom,
    viewport,
    // Raw frame refs, for overlays that must map image pixels → screen (e.g. star-name labels).
    scale,
    tx,
    ty,
    cw,
    ch,
    natW,
    natH,
    setNatural,
    fit,
    reset,
    zoomBy,
    actualSize,
    centerOnNorm,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onDblClick,
    onKey,
  };
}
