import { describe, it, expect } from "vitest";
import {
  MARKER_MAX_R,
  MARKER_MIN_R,
  MARKER_MIN_SEP_PX,
  MIN_OUTLINE_PX,
  markerRadius,
  outlineFor,
  selectStarMarkers,
} from "@/utils/starOverlay";

// M42 as the engine actually projects it on the reference run: 90′ across at 1.065″/px.
const M42 = { rx_px: 2535, ry_px: 2535, angle_rad: -1.382 };
// A small planetary nebula: ~1′ across on the same plate, well under the outline threshold at fit.
const SMALL = { rx_px: 14, ry_px: 9, angle_rad: 0.5 };

describe("outlineFor", () => {
  it("has nothing to draw without a catalogued extent", () => {
    expect(outlineFor(undefined, 1)).toBeNull();
    expect(outlineFor({ rx_px: 0, ry_px: 0, angle_rad: 0 }, 1)).toBeNull();
  });

  it("scales the footprint by the viewer transform and keeps the engine's angle", () => {
    const o = outlineFor(M42, 0.15); // 15% — the fit zoom on a 4656 px master
    expect(o).not.toBeNull();
    expect(o!.rx).toBeCloseTo(380.25, 2);
    expect(o!.ry).toBeCloseTo(380.25, 2);
    expect(o!.angle).toBe(M42.angle_rad); // uniform scale cannot rotate or shear it
  });

  it("hides an object too small to see, and reveals it once zoomed in", () => {
    // At fit the minor axis is 9 × 0.15 × 2 ≈ 2.7 screen px — a smudge, so no outline.
    expect(outlineFor(SMALL, 0.15)).toBeNull();
    // Zoomed to 100% it is 18 px across: worth outlining.
    const zoomed = outlineFor(SMALL, 1);
    expect(zoomed).not.toBeNull();
    expect(zoomed!.ry * 2).toBeGreaterThanOrEqual(MIN_OUTLINE_PX);
  });

  it("judges the threshold on the MINOR axis, so a thin edge-on sliver stays a dot", () => {
    // 400 px long but only 3 px thin: the long axis alone would pass, the minor axis must not.
    const sliver = { rx_px: 400, ry_px: 3, angle_rad: 0 };
    expect(outlineFor(sliver, 1)).toBeNull();
    expect(outlineFor({ ...sliver, ry_px: 8 }, 1)).not.toBeNull();
  });

  it("switches on exactly at the threshold", () => {
    const k = 1;
    const atLimit = { rx_px: 20, ry_px: MIN_OUTLINE_PX / 2, angle_rad: 0 };
    expect(outlineFor(atLimit, k)).not.toBeNull();
    expect(
      outlineFor({ ...atLimit, ry_px: MIN_OUTLINE_PX / 2 - 0.1 }, k),
    ).toBeNull();
  });

  it("ignores a degenerate transform rather than emitting NaN radii", () => {
    expect(outlineFor(M42, 0)).toBeNull();
    expect(outlineFor(M42, NaN)).toBeNull();
  });
});

describe("selectStarMarkers", () => {
  const view = { w: 400, h: 300 };
  const ident = { k: 1, tx: 0, ty: 0 };
  // A dense diagonal run of stars, brightest first (index order = brightness order).
  const line = Array.from({ length: 200 }, (_, i) => ({ x: i * 2, y: i }));

  it("draws nothing without stars, budget or a usable transform", () => {
    expect(selectStarMarkers(undefined, ident, view, 100)).toEqual([]);
    expect(selectStarMarkers([], ident, view, 100)).toEqual([]);
    expect(selectStarMarkers(line, ident, view, 0)).toEqual([]);
    expect(selectStarMarkers(line, { k: 0, tx: 0, ty: 0 }, view, 100)).toEqual(
      [],
    );
  });

  it("applies the viewer transform", () => {
    const got = selectStarMarkers(
      [{ x: 10, y: 20 }],
      { k: 2, tx: 5, ty: 7 },
      view,
      10,
    );
    expect(got).toHaveLength(1);
    expect([got[0].x, got[0].y]).toEqual([25, 47]);
  });

  it("never exceeds the budget, and spends it on the brightest first", () => {
    const got = selectStarMarkers(line, ident, view, 5);
    expect(got).toHaveLength(5);
    expect([got[0].x, got[0].y]).toEqual([0, 0]); // the first (brightest) entry
  });

  it("skips stars outside the viewport so the budget is not wasted off screen", () => {
    const stars = [
      { x: -500, y: -500 },
      { x: 5000, y: 5000 },
      { x: 200, y: 150 },
    ];
    const got = selectStarMarkers(stars, ident, view, 10);
    expect(got).toHaveLength(1);
    expect([got[0].x, got[0].y]).toEqual([200, 150]);
  });

  it("thins stars that would overlap into a blob", () => {
    // Ten stars one pixel apart: at this scale only a couple can be told apart.
    const cluster = Array.from({ length: 10 }, (_, i) => ({
      x: 100 + i,
      y: 100,
    }));
    const got = selectStarMarkers(cluster, ident, view, 10);
    expect(got.length).toBeLessThan(4);
    for (let i = 1; i < got.length; i++) {
      expect(
        Math.hypot(got[i].x - got[i - 1].x, got[i].y - got[i - 1].y),
      ).toBeGreaterThanOrEqual(MARKER_MIN_SEP_PX);
    }
  });

  it("reveals MORE stars as you zoom in — the point of the feature", () => {
    // Same budget, same star list. Zoomed out the cluster is thinned to a handful; zoomed in the
    // very same stars separate and the budget reaches deeper into the list.
    const cluster = Array.from({ length: 120 }, (_, i) => ({
      x: 100 + (i % 12) * 2,
      y: 100 + Math.floor(i / 12) * 2,
    }));
    const out = selectStarMarkers(cluster, { k: 0.5, tx: 0, ty: 0 }, view, 100);
    const inn = selectStarMarkers(
      cluster,
      { k: 8, tx: -700, ty: -700 },
      view,
      100,
    );
    expect(inn.length).toBeGreaterThan(out.length);
  });
});

describe("markerRadius", () => {
  it("scales the ring to the star's measured size", () => {
    const small = markerRadius({ x: 0, y: 0, r_px: 2 }, 1);
    const big = markerRadius({ x: 0, y: 0, r_px: 9 }, 1);
    expect(big).toBeGreaterThan(small);
    expect(big).toBeCloseTo(9 + 1.6, 5);
  });

  it("grows with zoom, because the star does too", () => {
    const s = { x: 0, y: 0, r_px: 4 };
    expect(markerRadius(s, 2)).toBeGreaterThan(markerRadius(s, 0.5));
  });

  it("keeps a ring readable at both extremes", () => {
    // Zoomed far out a real star is sub-pixel; zoomed far in a bloated one would hoop the frame.
    expect(markerRadius({ x: 0, y: 0, r_px: 1 }, 0.01)).toBe(MARKER_MIN_R);
    expect(markerRadius({ x: 0, y: 0, r_px: 20 }, 40)).toBe(MARKER_MAX_R);
  });

  it("falls back to a sane size when the engine sent no measurement", () => {
    const r = markerRadius({ x: 0, y: 0 }, 1);
    expect(r).toBeGreaterThanOrEqual(MARKER_MIN_R);
    expect(r).toBeLessThan(MARKER_MAX_R);
  });
});

describe("selectStarMarkers colour", () => {
  it("carries each star's own colour and the star itself through to the renderer", () => {
    const got = selectStarMarkers(
      [{ x: 10, y: 10, hex: "#a8c8ff", r_px: 3, mag: 11.2 }],
      { k: 1, tx: 0, ty: 0 },
      { w: 400, h: 300 },
      5,
    );
    expect(got[0].hex).toBe("#a8c8ff");
    expect(got[0].star.mag).toBe(11.2); // hover reads this without a second lookup
  });
});
