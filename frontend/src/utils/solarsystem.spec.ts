import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import {
  bodyBasis,
  drawRadius,
  elementsPosition,
  heliocentricAt,
  jdFromMs,
  localPositionAt,
  msFromJd,
  MIN_BODY_PX,
  orbitPath,
  orientationAt,
  radialWarp,
  scenePosition,
  solveKepler,
  SOLAR_MANIFEST_VERSION,
  warpPosition,
} from "@/utils/solarsystem";
import { cross, dot } from "@/utils/mat4";
import type { SolarBody, SolarManifest } from "@/types";

// The engine writes this fixture; see internal/solarsystem/golden_test.go. Reading it here is what
// makes the two propagators one model instead of two: if the Go side changes and this file is
// regenerated, these tests fail until utils/solarsystem.ts is brought back into line.
interface GoldenBody {
  helio_au: [number, number, number];
  local_au: [number, number, number];
  pole_ra_deg: number;
  pole_dec_deg: number;
  w_deg: number;
}
interface Golden {
  manifest: SolarManifest;
  epochs: { iso: string; jd: number; bodies: Record<string, GoldenBody> }[];
}

// Vitest runs from frontend/, so the engine's testdata is one level up. Reading it directly rather
// than copying it in is deliberate: a copy is a fixture that can go stale without anyone noticing.
const golden: Golden = JSON.parse(
  readFileSync(
    resolve(process.cwd(), "../internal/solarsystem/testdata/golden.json"),
    "utf8",
  ),
);

const byKey = new Map<string, SolarBody>(
  golden.manifest.bodies.map((b) => [b.key, b]),
);

describe("the engine's model", () => {
  it("is the version this mirror was written against", () => {
    expect(golden.manifest.version).toBe(SOLAR_MANIFEST_VERSION);
  });

  it("covers 1800–2050 and carries its sources", () => {
    expect(golden.manifest.range_from).toBe(1800);
    expect(golden.manifest.range_to).toBe(2050);
    expect(golden.manifest.sources.length).toBeGreaterThan(0);
  });
});

describe("heliocentric positions match the engine", () => {
  for (const epoch of golden.epochs) {
    it(`at ${epoch.iso}`, () => {
      for (const [key, want] of Object.entries(epoch.bodies)) {
        const body = byKey.get(key);
        expect(body, key).toBeDefined();
        const got = heliocentricAt(byKey, body!, epoch.jd);
        // A tenth of a metre in astronomical units: this is a rewrite of the same arithmetic, so
        // anything looser would let a real divergence through.
        for (let axis = 0; axis < 3; axis++) {
          expect(got[axis], `${key} axis ${axis}`).toBeCloseTo(
            want.helio_au[axis],
            11,
          );
        }
      }
    });
  }
});

describe("local positions match the engine", () => {
  it("puts the Moon where the engine puts it, in every epoch", () => {
    for (const epoch of golden.epochs) {
      const want = epoch.bodies.moon;
      const got = localPositionAt(byKey.get("moon")!, epoch.jd);
      for (let axis = 0; axis < 3; axis++) {
        expect(got[axis], `${epoch.iso} axis ${axis}`).toBeCloseTo(
          want.local_au[axis],
          11,
        );
      }
    }
  });
});

describe("rotation matches the engine", () => {
  for (const epoch of golden.epochs) {
    it(`orients every body at ${epoch.iso}`, () => {
      for (const [key, want] of Object.entries(epoch.bodies)) {
        const o = orientationAt(byKey.get(key)!.pole, epoch.jd);
        expect(o.poleRA, `${key} pole RA`).toBeCloseTo(want.pole_ra_deg, 9);
        expect(o.poleDec, `${key} pole dec`).toBeCloseTo(want.pole_dec_deg, 9);
        // The prime meridian is a fast rate times a long baseline reduced mod 360 — two centuries
        // before the epoch Uranus's W is accumulated from thirty-seven million degrees, and the
        // cancellation in that reduction costs the last few bits in either language. A microdegree
        // is a hundred-thousandth of an arcsecond, so this is float64 itself, not a divergence.
        expect(o.wDeg, `${key} prime meridian`).toBeCloseTo(want.w_deg, 6);
      }
    });
  }
});

describe("bodyBasis", () => {
  it("is orthonormal and right-handed for every body", () => {
    const jd = golden.epochs[golden.epochs.length - 1].jd;
    for (const body of golden.manifest.bodies) {
      const { x, y, z } = bodyBasis(orientationAt(body.pole, jd));
      expect(Math.hypot(...x), body.key).toBeCloseTo(1, 12);
      expect(Math.hypot(...y), body.key).toBeCloseTo(1, 12);
      expect(Math.hypot(...z), body.key).toBeCloseTo(1, 12);
      expect(dot(x, y), body.key).toBeCloseTo(0, 12);
      expect(dot(y, z), body.key).toBeCloseTo(0, 12);
      // Right-handed, or every texture is drawn mirrored.
      expect(dot(cross(x, y), z), body.key).toBeCloseTo(1, 12);
    }
  });
});

describe("solveKepler", () => {
  it("satisfies Kepler's equation", () => {
    for (const e of [0, 0.01, 0.2, 0.6, 0.9]) {
      for (const m of [0, 37, 123, 200, 359]) {
        const ecc = solveKepler(m, e);
        const residual =
          ecc - (180 / Math.PI) * e * Math.sin((ecc * Math.PI) / 180) - m;
        expect(Math.abs(residual), `e=${e} M=${m}`).toBeLessThan(1e-8);
      }
    }
  });

  it("wraps a mean anomaly given outside one turn", () => {
    expect(solveKepler(370, 0.1)).toBeCloseTo(solveKepler(10, 0.1), 10);
    expect(solveKepler(-90, 0.1)).toBeCloseTo(solveKepler(270, 0.1), 10);
  });
});

describe("elementsPosition", () => {
  const el = { a: 2.5, e: 0.2, iDeg: 30, nodeDeg: 40, periDeg: 50, mDeg: 0 };

  it("puts the body at perihelion at M=0 and aphelion at M=180", () => {
    expect(Math.hypot(...elementsPosition(el))).toBeCloseTo(2.5 * 0.8, 10);
    expect(Math.hypot(...elementsPosition({ ...el, mDeg: 180 }))).toBeCloseTo(
      2.5 * 1.2,
      10,
    );
  });

  it("keeps an uninclined orbit in the plane", () => {
    const p = elementsPosition({ ...el, iDeg: 0, mDeg: 123 });
    expect(p[2]).toBeCloseTo(0, 12);
  });
});

describe("orbitPath", () => {
  it("closes, and lies on the ellipse the body is on", () => {
    const earth = byKey.get("earth")!;
    const jd = golden.epochs[golden.epochs.length - 2].jd;
    const path = orbitPath(earth.orbit!, jd, 64);
    expect(path.length).toBe(65 * 3);
    // First and last sample are the same point: an orbit drawn from elements that drift as the
    // sweep proceeds does not close, and the seam shows.
    for (let axis = 0; axis < 3; axis++) {
      expect(path[axis]).toBeCloseTo(path[64 * 3 + axis], 12);
    }
    // Every sample sits between perihelion and aphelion.
    for (let i = 0; i <= 64; i++) {
      const r = Math.hypot(path[i * 3], path[i * 3 + 1], path[i * 3 + 2]);
      expect(r).toBeGreaterThan(0.98);
      expect(r).toBeLessThan(1.02);
    }
  });

  it("passes through the body's own position", () => {
    const mars = byKey.get("mars")!;
    const jd = golden.epochs[golden.epochs.length - 1].jd;
    const here = localPositionAt(mars, jd);
    const path = orbitPath(mars.orbit!, jd, 720);

    let best = Infinity;
    for (let i = 0; i <= 720; i++) {
      best = Math.min(
        best,
        Math.hypot(
          path[i * 3] - here[0],
          path[i * 3 + 1] - here[1],
          path[i * 3 + 2] - here[2],
        ),
      );
    }
    // Half a degree of Mars's orbit is about 0.013 AU, so this is sampling resolution, not error.
    expect(best).toBeLessThan(0.02);
  });
});

describe("radialWarp", () => {
  it("is the identity when it is off", () => {
    for (const r of [0.39, 1, 5.2, 30]) expect(radialWarp(r, 0)).toBe(r);
  });

  it("keeps the ordering of the planets at every setting", () => {
    const radii = [0.39, 0.72, 1, 1.52, 5.2, 9.5, 19.2, 30];
    for (const warp of [0, 0.25, 0.5, 0.75, 1]) {
      const warped = radii.map((r) => radialWarp(r, warp));
      for (let i = 1; i < warped.length; i++) {
        expect(warped[i], `warp ${warp}`).toBeGreaterThan(warped[i - 1]);
      }
    }
  });

  it("compresses the outer system relative to the inner one", () => {
    const spanTrue = radialWarp(30, 0) / radialWarp(1, 0);
    const spanWarped = radialWarp(30, 1) / radialWarp(1, 1);
    expect(spanWarped).toBeLessThan(spanTrue);
  });

  it("leaves a direction untouched", () => {
    const p: [number, number, number] = [3, -4, 12];
    const w = warpPosition(p, 0.7);
    const a = Math.hypot(...p);
    const b = Math.hypot(...w);
    for (let axis = 0; axis < 3; axis++) {
      expect(w[axis] / b).toBeCloseTo(p[axis] / a, 12);
    }
  });
});

describe("scenePosition", () => {
  // A moon 0.0028 AU from a planet 5.1972 AU from the Sun — Callisto's distance from Jupiter.
  const local: [number, number, number] = [0.0028, 0, 0];
  const planetHelio: [number, number, number] = [5.1972, 0, 0];
  const moonHelio: [number, number, number] = [5.1972 + 0.0028, 0, 0];

  it("warps a moon about its planet, not about the Sun", () => {
    const drawn = scenePosition(moonHelio, local, 1, 1);
    const planet = scenePosition(planetHelio, [0, 0, 0], 1, 1);
    // Warping about the Sun would collapse the moon onto the planet, because a log-compressed
    // radial field barely changes over four hundred thousand kilometres. Warping about the planet
    // keeps the system's own geometry intact.
    expect(drawn[0] - planet[0]).toBeCloseTo(local[0], 12);
  });

  it("magnifies a satellite system without moving its planet", () => {
    const planet = scenePosition(planetHelio, [0, 0, 0], 0, 1);
    const near = scenePosition(moonHelio, local, 0, 1);
    const far = scenePosition(moonHelio, local, 0, 40);
    expect(near[0] - planet[0]).toBeCloseTo(local[0], 12);
    expect(far[0] - planet[0]).toBeCloseTo(40 * local[0], 10);
    // The planet itself does not move when the system is blown up.
    expect(scenePosition(planetHelio, [0, 0, 0], 0, 40)[0]).toBeCloseTo(
      planet[0],
      12,
    );
  });
});

describe("drawRadius", () => {
  it("never lets a world fall below a clickable size", () => {
    const earthAU = 6378 / 149597870.7;
    // Earth seen from beyond Neptune: its true disc is far under one pixel.
    const r = drawRadius(earthAU, 30, 0.4, 800);
    const perPx = (2 * 0.4 * 30) / 800;
    expect(r).toBeCloseTo(MIN_BODY_PX * perPx, 12);
  });

  it("hands over to the true size as the camera closes in", () => {
    const jupiterAU = 71492 / 149597870.7;
    const close = drawRadius(jupiterAU, 0.001, 0.4, 800);
    expect(close).toBe(jupiterAU);
  });

  it("is continuous across the handover", () => {
    const rAU = 0.001;
    let previous = drawRadius(rAU, 1e-4, 0.4, 800);
    for (let d = 2e-4; d < 1; d *= 1.2) {
      const now = drawRadius(rAU, d, 0.4, 800);
      expect(now).toBeGreaterThanOrEqual(previous - 1e-12);
      previous = now;
    }
  });
});

describe("time", () => {
  it("round-trips between milliseconds and Julian dates", () => {
    for (const epoch of golden.epochs) {
      const ms = msFromJd(epoch.jd);
      expect(jdFromMs(ms)).toBeCloseTo(epoch.jd, 9);
      expect(new Date(ms).toISOString().slice(0, 10)).toBe(
        epoch.iso.slice(0, 10),
      );
    }
  });
});
