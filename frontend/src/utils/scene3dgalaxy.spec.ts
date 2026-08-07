import { describe, expect, it } from "vitest";
import { newBasis } from "@/utils/skyframe";
import { R_SUN_KPC } from "@/utils/galaxy";
import {
  GALAXY_FRAME_HALF_KPC,
  GALAXY_VANTAGE_ELEVATION_DEG,
  GALAXY_VANTAGE_KPC,
  buildGalaxyLines,
  buildGalaxyMesh,
  frameSpanPc,
  galacticToScene,
  galaxyDistance,
  galaxyOrbit,
  galaxyTanScale,
} from "@/utils/scene3dgalaxy";
import { eyePosition } from "@/utils/scene3d";
import type { SkyFrame } from "@/types";

const M42_FRAME: SkyFrame = {
  center_ra: 83.8456513962952,
  center_dec: -5.441118543410391,
  x_edge_ra: 83.19548576680775,
  x_edge_dec: -5.318528562532911,
  y_edge_ra: 83.93785351682925,
  y_edge_dec: -4.959728943508528,
};
const TAN_H = 0.008553504394858668;
const basis = () => {
  const b = newBasis(M42_FRAME);
  if (!b) throw new Error("basis");
  return b;
};
const ctx = () => {
  const m = galacticToScene(basis());
  const unit = (v: readonly number[]) => {
    const n = Math.hypot(v[0], v[1], v[2]);
    return [v[0] / n, v[1] / n, v[2] / n] as [number, number, number];
  };
  return {
    medianPc: 763,
    tanHalfH: TAN_H,
    toGalacticCentre: unit(m[0]),
    toNorthPole: unit(m[2]),
  };
};

describe("the journey", () => {
  it("starts with the eye exactly at Earth, through the run's own lens", () => {
    // The anchor: at zoom zero the galaxy view IS the photograph, so the toggle never jumps.
    const v = galaxyOrbit(0, ctx());
    expect(v.tanScale).toBeCloseTo(1, 12);
    expect(v.orbit.yaw).toBeCloseTo(0, 12);
    expect(v.orbit.pitch).toBeCloseTo(0, 12);
    expect(v.orbit.roll).toBe(0);
    const eye = eyePosition(v.orbit);
    for (const c of eye) expect(Math.abs(c)).toBeLessThan(1e-12);
  });

  it("ends outside the disc with the whole Galaxy framed", () => {
    const v = galaxyOrbit(1, ctx());
    expect(v.orbit.distance).toBeCloseTo(GALAXY_VANTAGE_KPC, 6);
    // The lens opened just far enough to frame the disc from there.
    expect(v.tanScale * TAN_H * GALAXY_VANTAGE_KPC).toBeCloseTo(
      GALAXY_FRAME_HALF_KPC,
      6,
    );
    // Elevation is asserted against the GALACTIC plane, not against the scene's pitch angle.
    //
    // The old assertion here was `pitch > 0.8`, and it passed the whole time the camera was sitting
    // sixteen degrees BELOW the galactic plane — because pitch is measured against the photograph's
    // axes, which have nothing to do with the Galaxy. A test that cannot tell those apart cannot
    // catch the only thing that matters about a vantage.
    const c = ctx();
    const eye = eyePosition(v.orbit);
    const gcPos = c.toGalacticCentre.map((x) => x * R_SUN_KPC) as [
      number,
      number,
      number,
    ];
    const rel: [number, number, number] = [
      eye[0] - gcPos[0],
      eye[1] - gcPos[1],
      eye[2] - gcPos[2],
    ];
    const len = Math.hypot(rel[0], rel[1], rel[2]);
    const alongPole =
      (rel[0] * c.toNorthPole[0] +
        rel[1] * c.toNorthPole[1] +
        rel[2] * c.toNorthPole[2]) /
      len;
    const elevationDeg = (Math.asin(alongPole) * 180) / Math.PI;
    expect(elevationDeg).toBeCloseTo(GALAXY_VANTAGE_ELEVATION_DEG, 0);
  });

  it("moves monotonically, so the slider never doubles back", () => {
    let d = 0;
    let s = 0;
    for (let t = 0; t <= 1.0001; t += 0.05) {
      const v = galaxyOrbit(t, ctx());
      expect(v.orbit.distance).toBeGreaterThanOrEqual(d - 1e-9);
      expect(v.tanScale).toBeGreaterThanOrEqual(s - 1e-9);
      d = v.orbit.distance;
      s = v.tanScale;
    }
  });

  it("reports an honest frame width at both ends", () => {
    const c = ctx();
    // A degree-wide field a few hundred parsecs off is tens of parsecs across...
    expect(frameSpanPc(0, c, 0.0115)).toBeGreaterThan(5);
    expect(frameSpanPc(0, c, 0.0115)).toBeLessThan(100);
    // ...and the far end is tens of kiloparsecs.
    expect(frameSpanPc(1, c, 0.0115)).toBeGreaterThan(30000);
  });

  it("clamps a nonsense slider instead of losing the camera", () => {
    for (const t of [-5, 2, Number.NaN]) {
      const v = galaxyOrbit(t, ctx());
      expect(Number.isFinite(v.orbit.distance)).toBe(true);
      expect(Number.isFinite(v.tanScale)).toBe(true);
      expect(v.orbit.distance).toBeGreaterThan(0);
    }
    expect(galaxyDistance(0.5, 0)).toBeGreaterThan(0);
    expect(galaxyTanScale(0.5, 0)).toBeGreaterThan(0);
  });
});

describe("the galactic transform", () => {
  it("is orthogonal and uniformly scaled", () => {
    const m = galacticToScene(basis());
    const len = (v: readonly number[]) => Math.hypot(v[0], v[1], v[2]);
    // One kiloparsec of galaxy is one scene unit, on every axis.
    for (const a of m) expect(len(a)).toBeCloseTo(1, 9);
    const dot = (a: readonly number[], b: readonly number[]) =>
      a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
    expect(dot(m[0], m[1])).toBeCloseTo(0, 9);
    expect(dot(m[0], m[2])).toBeCloseTo(0, 9);
    expect(dot(m[1], m[2])).toBeCloseTo(0, 9);
  });

  it("carries the field's parity, so a mirrored run mirrors the Galaxy too", () => {
    const m = galacticToScene(basis());
    // det < 0 on a mirrored field. Both real runs are right_handed: false, and the Galaxy MUST be
    // mirrored with the photograph — flipping only one of them would put them in the wrong
    // relationship, which is the entire point of drawing them together.
    const det =
      m[0][0] * (m[1][1] * m[2][2] - m[1][2] * m[2][1]) -
      m[1][0] * (m[0][1] * m[2][2] - m[0][2] * m[2][1]) +
      m[2][0] * (m[0][1] * m[1][2] - m[0][2] * m[1][1]);
    expect(Math.abs(det)).toBeCloseTo(1, 9);
    expect(det < 0).toBe(!basis().rightHanded);
  });

  it("puts the galactic centre behind an Orion field", () => {
    // M42 looks 29° from the anticentre, so the centre must be behind the camera (scene z < 0).
    expect(galacticToScene(basis())[0][2]).toBeLessThan(0);
  });
});

describe("the mesh", () => {
  it("fits the 16-bit index space and has no NaN", () => {
    const mesh = buildGalaxyMesh(galacticToScene(basis()));
    const verts = mesh.positions.length / 3;
    expect(verts).toBeGreaterThan(1000);
    expect(verts).toBeLessThan(65536);
    for (const i of mesh.indices) expect(i).toBeLessThan(verts);
    expect(mesh.indices.length % 3).toBe(0);
    for (const v of mesh.positions) expect(Number.isFinite(v)).toBe(true);
    for (const c of mesh.colors) {
      expect(Number.isFinite(c)).toBe(true);
      expect(c).toBeGreaterThanOrEqual(0);
    }
  });

  it("stays inside the drawn disc", () => {
    const mesh = buildGalaxyMesh(galacticToScene(basis()));
    // Everything is within a disc radius of the galactic centre, which sits R_SUN from the origin.
    for (let i = 0; i < mesh.positions.length; i += 3) {
      const r = Math.hypot(
        mesh.positions[i],
        mesh.positions[i + 1],
        mesh.positions[i + 2],
      );
      expect(r).toBeLessThan(R_SUN_KPC + 16);
    }
  });

  it("draws reference rings and the Sun-centre line", () => {
    const lines = buildGalaxyLines(galacticToScene(basis()));
    expect(lines.positions.length).toBeGreaterThan(0);
    expect(lines.positions.length % 6).toBe(0); // whole segments
    expect(lines.colors.length).toBe(lines.positions.length);
  });
});
