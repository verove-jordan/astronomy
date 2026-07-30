import { describe, expect, it } from "vitest";
import {
  ellipseCorners,
  hitTile,
  movedCenter,
  norm180,
  offsetCorners,
  pointInPolygon,
  type Corner,
} from "@/utils/skygrid";
import { tangentPlane } from "@/utils/astro";

// A 1°×1° footprint around (10°, 41°), corners in TL/TR/BR/BL order like the planner emits.
function square(ra: number, dec: number, sizeDeg = 1): Corner[] {
  const hw = sizeDeg / 2 / Math.cos((dec * Math.PI) / 180);
  const hh = sizeDeg / 2;
  return [
    [ra - hw, dec + hh],
    [ra + hw, dec + hh],
    [ra + hw, dec - hh],
    [ra - hw, dec - hh],
  ];
}

describe("norm180", () => {
  it.each([
    [0, 0],
    [10, 10],
    [359, -1],
    [-359, 1],
    [180, 180],
    [181, -179],
    [720 + 5, 5],
  ])("wraps %d to %d", (input, want) => {
    expect(norm180(input)).toBeCloseTo(want, 9);
  });
});

describe("pointInPolygon", () => {
  const poly = square(10, 41);

  it("accepts the centre", () => {
    expect(pointInPolygon({ ra: 10, dec: 41 }, poly)).toBe(true);
  });

  it("rejects a point outside", () => {
    expect(pointInPolygon({ ra: 12, dec: 41 }, poly)).toBe(false);
    expect(pointInPolygon({ ra: 10, dec: 43 }, poly)).toBe(false);
  });

  it("works across the 0h meridian", () => {
    const wrapped = square(0.2, 20);
    expect(pointInPolygon({ ra: 359.9, dec: 20 }, wrapped)).toBe(true);
    expect(pointInPolygon({ ra: 355, dec: 20 }, wrapped)).toBe(false);
  });
});

describe("hitTile", () => {
  const tiles = [
    { corners: square(10, 41) },
    { corners: square(12, 41) },
    { corners: square(14, 41) },
  ];

  it("returns the containing tile index", () => {
    expect(hitTile({ ra: 12, dec: 41 }, tiles)).toBe(1);
  });

  it("returns -1 when nothing is hit", () => {
    expect(hitTile({ ra: 20, dec: 41 }, tiles)).toBe(-1);
  });
});

describe("offsetCorners", () => {
  const center = { ra: 10, dec: 41 };

  it("translates by the requested tangent-plane offset", () => {
    const moved = offsetCorners(square(10, 41), center, 0.5, -0.25);
    for (let i = 0; i < 4; i++) {
      const before = tangentPlane(
        center.ra,
        center.dec,
        square(10, 41)[i][0],
        square(10, 41)[i][1],
      );
      const after = tangentPlane(
        center.ra,
        center.dec,
        moved[i][0],
        moved[i][1],
      );
      expect(before).not.toBeNull();
      expect(after).not.toBeNull();
      expect(after!.xi - before!.xi).toBeCloseTo(0.5, 6);
      expect(after!.eta - before!.eta).toBeCloseTo(-0.25, 6);
    }
  });

  it("is a no-op for a zero drag", () => {
    const original = square(10, 41);
    const moved = offsetCorners(original, center, 0, 0, 0);
    for (let i = 0; i < 4; i++) {
      expect(moved[i][0]).toBeCloseTo(original[i][0], 6);
      expect(moved[i][1]).toBeCloseTo(original[i][1], 6);
    }
  });

  it("rotating by 360° returns to the start", () => {
    const original = square(10, 41);
    const spun = offsetCorners(original, center, 0, 0, 360);
    for (let i = 0; i < 4; i++) {
      expect(spun[i][0]).toBeCloseTo(original[i][0], 6);
      expect(spun[i][1]).toBeCloseTo(original[i][1], 6);
    }
  });

  it("rotation preserves each corner's distance from the centre", () => {
    const original = square(10, 41);
    const spun = offsetCorners(original, center, 0, 0, 37);
    for (let i = 0; i < 4; i++) {
      const a = tangentPlane(
        center.ra,
        center.dec,
        original[i][0],
        original[i][1],
      )!;
      const b = tangentPlane(center.ra, center.dec, spun[i][0], spun[i][1])!;
      expect(Math.hypot(b.xi, b.eta)).toBeCloseTo(Math.hypot(a.xi, a.eta), 6);
    }
  });
});

describe("movedCenter", () => {
  it("moves east for a positive xi", () => {
    const moved = movedCenter({ ra: 10, dec: 41 }, 0.5, 0);
    expect(moved.ra).toBeGreaterThan(10);
    // A pure-east tangent-plane step follows a great circle, which dips a few arcseconds in
    // declination — that is the projection behaving correctly, not drift.
    expect(moved.dec).toBeCloseTo(41, 2);
  });

  it("moves north for a positive eta", () => {
    const moved = movedCenter({ ra: 10, dec: 41 }, 0, 0.5);
    expect(moved.dec).toBeCloseTo(41.5, 3);
  });

  it("wraps RA past 0h instead of going negative", () => {
    const moved = movedCenter({ ra: 0.1, dec: 0 }, -0.5, 0);
    expect(moved.ra).toBeGreaterThan(359);
    expect(moved.ra).toBeLessThan(360);
  });
});

describe("ellipseCorners", () => {
  const center = { ra: 10, dec: 41 };

  function flatten(poly: Corner[]) {
    return poly.map(
      ([ra, dec]) => tangentPlane(center.ra, center.dec, ra, dec)!,
    );
  }

  it("puts the major axis north-south at PA 0", () => {
    const flat = flatten(ellipseCorners(center, 120, 30, 0, 64));
    expect(Math.max(...flat.map((p) => Math.abs(p.eta)))).toBeCloseTo(1, 2); // 120′ → ±1°
    expect(Math.max(...flat.map((p) => Math.abs(p.xi)))).toBeCloseTo(0.25, 2); // 30′ → ±0.25°
  });

  // Position angle is measured EAST of north. Getting this sign wrong draws the object mirrored,
  // which on a galaxy like M31 (PA 35°) puts the ellipse across the galaxy instead of along it.
  it("points the major axis east at PA 90", () => {
    const flat = flatten(ellipseCorners(center, 120, 30, 90, 64));
    expect(Math.max(...flat.map((p) => Math.abs(p.xi)))).toBeCloseTo(1, 2);
    expect(Math.max(...flat.map((p) => Math.abs(p.eta)))).toBeCloseTo(0.25, 2);
  });

  it("tilts toward the north-east at PA 35, not the north-west", () => {
    const flat = flatten(ellipseCorners(center, 178, 70, 35, 180));
    // The point furthest from the centre IS the tip of the major axis; at PA 35 it must sit north
    // AND east of the centre (ξ > 0, η > 0).
    const tip = flat.reduce((best, p) =>
      Math.hypot(p.xi, p.eta) > Math.hypot(best.xi, best.eta) ? p : best,
    );
    expect(tip.eta * tip.xi).toBeGreaterThan(0);
    const paBack =
      (Math.atan2(Math.abs(tip.xi), Math.abs(tip.eta)) * 180) / Math.PI;
    expect(paBack).toBeCloseTo(35, 0);
  });
});
