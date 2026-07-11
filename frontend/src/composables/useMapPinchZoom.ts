import type { Map as LMap, Point } from "leaflet";

// macOS-style velocity-sensitive trackpad zoom for a Leaflet map.
//
// The browser reports a trackpad pinch as a stream of `wheel` events with ctrl/⌘ held. The naive
// handler called `setZoomAround` per event with Leaflet's default zoom *animation*: each event kicked
// off a ~250 ms animation that the next event interrupted, so most of the gesture was dropped — you had
// to "spam" the pinch to move. Here we instead accumulate the wheel deltas and apply a single instant
// (`animate:false`) zoom step once per animation frame. Because the applied step scales with the delta
// accumulated in that frame, a fast pinch (many/large deltas per frame) zooms coarsely and quickly while
// a slow pinch zooms finely — one continuous gesture covers both macro and micro, no repeating.
//
// Working in Leaflet's zoom-*level* (log) space means the same finger travel gives the same zoom ratio at
// any scale; the mild super-linear term adds the acceleration a fast flick should feel. A plain two-finger
// scroll (no modifier) pans, unchanged.
//
// Returns a detach function; call it from onBeforeUnmount. `getMap` is a getter so the map can be created
// after this is wired and safely become null on teardown.
export function useMapPinchZoom(
  el: HTMLElement,
  getMap: () => LMap | null,
): () => void {
  let pending = 0; // accumulated -deltaY (zoom-in positive) awaiting the next frame
  let raf = 0;
  let anchor: Point | null = null; // container point to zoom about (the cursor)

  const flush = () => {
    raf = 0;
    const m = getMap();
    if (!m || !anchor) {
      pending = 0;
      return;
    }
    const mag = Math.abs(pending);
    // Tunable feel: base sensitivity 0.02 zoom-level per delta unit, a gentle super-linear boost so fast
    // flicks separate from fine nudges, capped so one frame can't jump absurdly far. Refine on a real
    // trackpad if desired.
    const dz = Math.sign(pending) * Math.min(3, mag * 0.02 * (1 + mag / 200));
    pending = 0;
    m.setZoomAround(anchor, m.getZoom() + dz, { animate: false });
  };

  const onWheel = (e: WheelEvent) => {
    const m = getMap();
    if (!m) return;
    e.preventDefault(); // stop the page from scrolling/zooming under the map
    if (e.ctrlKey || e.metaKey) {
      // Pinch → zoom about the cursor. Normalize line-mode wheels to pixels first.
      const px = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
      pending += -px;
      anchor = m.mouseEventToContainerPoint(e);
      if (!raf) raf = requestAnimationFrame(flush);
    } else {
      m.panBy([e.deltaX, e.deltaY], { animate: false });
    }
  };

  el.addEventListener("wheel", onWheel, { passive: false });
  return () => {
    el.removeEventListener("wheel", onWheel);
    if (raf) cancelAnimationFrame(raf);
  };
}
