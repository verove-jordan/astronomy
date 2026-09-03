import { describe, expect, it } from "vitest";
import { newBasis } from "@/utils/skyframe";
import { DISC_EDGE_KPC, R_SUN_KPC } from "@/utils/galaxy";
import {
  GALAXY_FRAME_HALF_KPC,
  GALAXY_VANTAGE_ELEVATION_DEG,
  GALAXY_VANTAGE_KPC,
  OUTER_FRAME_HALF,
  OUTER_STANDOFF,
  buildGalaxyLines,
  decadeLadderKpc,
  frameSpanPc,
  galacticToScene,
  galaxyMaxOrbitDistance,
  galaxyTanScale,
  galaxyView,
  journeyEndKpc,
  journeySplit,
  type JourneyContext,
} from "@/utils/scene3dgalaxy";
import { MAX_ORBIT_DISTANCE, eyePosition } from "@/utils/scene3d";
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
const ctx = (): JourneyContext => {
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

// M51's real distance, from output/M51/20260722_191432 — the case that made the zoom-out ceiling a bug.
const M51_PC = 7052000;
const farCtx = (): JourneyContext => ({
  ...ctx(),
  medianPc: 388,
  farthestPc: M51_PC,
  // Straight down the barrel, which is where a run's target actually is.
  toFarthest: [0, 0, 1],
});

describe("the journey", () => {
  it("starts with the eye exactly at Earth, through the run's own lens", () => {
    // The anchor: at zoom zero the galaxy view IS the photograph, so the toggle never jumps.
    const v = galaxyView(0, ctx());
    expect(v.tanScale).toBeCloseTo(1, 12);
    expect(v.orbit.yaw).toBeCloseTo(0, 12);
    expect(v.orbit.pitch).toBeCloseTo(0, 12);
    expect(v.orbit.roll).toBe(0);
    const eye = eyePosition(v.orbit);
    for (const c of eye) expect(Math.abs(c)).toBeLessThan(1e-12);
  });

  it("ends outside the disc with the whole Galaxy framed", () => {
    const v = galaxyView(1, ctx());
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
    expect(elevationAtDeg(v.orbit, ctx())).toBeCloseTo(
      GALAXY_VANTAGE_ELEVATION_DEG,
      0,
    );
  });

  it("moves monotonically, so the slider never doubles back", () => {
    // The two things that must only ever grow are how far the eye stands off and how much sky is in
    // frame — that is what "zoom out" means to whoever is dragging the slider.
    //
    // Not the lens: on the outer leg it narrows slightly, because the standoff has to grow a little
    // faster than the pair it is framing. The frame span still grows by two hundred-fold across that
    // leg, so the movement reads as one continuous pull-back.
    for (const c of [ctx(), farCtx()]) {
      let d = 0;
      let span = 0;
      for (let t = 0; t <= 1.0001; t += 0.02) {
        const v = galaxyView(t, c);
        expect(v.orbit.distance).toBeGreaterThanOrEqual(d - 1e-9);
        const s = frameSpanPc(t, c, 0.0115);
        expect(s).toBeGreaterThanOrEqual(span * (1 - 1e-9));
        d = v.orbit.distance;
        span = s;
      }
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
      const v = galaxyView(t, ctx());
      expect(Number.isFinite(v.orbit.distance)).toBe(true);
      expect(Number.isFinite(v.tanScale)).toBe(true);
      expect(v.orbit.distance).toBeGreaterThan(0);
    }
    expect(galaxyTanScale(0.5, { ...ctx(), tanHalfH: 0 })).toBeGreaterThan(0);
  });
});

describe("the journey past the Galaxy", () => {
  // The bug: the journey stopped at the 35 kpc galactic vantage whatever the run held, so a field whose
  // target is seven megaparsecs out could never be seen with the Milky Way — and the manual zoom-out was
  // capped at 400 kpc, seventeen times too near.
  it("ends at the Galaxy when nothing in the scene reaches past it", () => {
    expect(journeyEndKpc(ctx())).toBe(GALAXY_VANTAGE_KPC);
    expect(journeySplit(ctx())).toBe(1);
    // An object INSIDE the disc does not extend the journey either: it is already framed.
    const inside: JourneyContext = {
      ...ctx(),
      farthestPc: 8000,
      toFarthest: [0, 0, 1],
    };
    expect(journeyEndKpc(inside)).toBe(GALAXY_VANTAGE_KPC);
  });

  it("flies out far enough to see the Milky Way and a distant galaxy together", () => {
    const c = farCtx();
    const end = journeyEndKpc(c);
    expect(end).toBeCloseTo(OUTER_STANDOFF * (M51_PC / 1000), 6);
    const v = galaxyView(1, c);
    expect(v.orbit.distance).toBeCloseTo(end, 3);

    // The object and the Sun must BOTH be inside the frame — which is the whole point of going out
    // there, and what standing at the galactic vantage could never do.
    const half = v.tanScale * TAN_H * v.orbit.distance;
    expect(half).toBeGreaterThan(0.5 * (M51_PC / 1000));
    expect(half).toBeCloseTo(OUTER_FRAME_HALF * (M51_PC / 1000), 3);
  });

  it("still passes through the Galaxy on the way out", () => {
    const c = farCtx();
    const split = journeySplit(c);
    // Log-proportional: not a sliver of the slider, and not most of it.
    expect(split).toBeGreaterThan(0.25);
    expect(split).toBeLessThan(0.75);
    const atGalaxy = galaxyView(split, c);
    expect(atGalaxy.orbit.distance).toBeCloseTo(GALAXY_VANTAGE_KPC, 3);
    expect(elevationAtDeg(atGalaxy.orbit, c)).toBeCloseTo(
      GALAXY_VANTAGE_ELEVATION_DEG,
      0,
    );
  });

  it("raises the manual zoom-out ceiling to match", () => {
    expect(galaxyMaxOrbitDistance(ctx())).toBe(MAX_ORBIT_DISTANCE);
    // Far enough to hold the whole journey, so the wheel can go wherever the slider can.
    expect(galaxyMaxOrbitDistance(farCtx())).toBeGreaterThan(
      journeyEndKpc(farCtx()),
    );
  });

  it("keeps the Milky Way in frame at every point of the way out", () => {
    // The bug: with the pivot easing linearly toward the midpoint while the camera pulled back
    // logarithmically, the middle of the slider showed empty space — the pivot had run 360 kpc down the
    // line of sight with the camera 62 kpc behind it and 45 kpc of frame, so the Galaxy was four hundred
    // kiloparsecs off screen and M51 was still megaparsecs away.
    const c = farCtx();
    for (let t = journeySplit(c); t <= 1.0001; t += 0.02) {
      const v = galaxyView(Math.min(1, t), c);
      const eye = eyePosition(v.orbit);
      const toTarget = sub(v.orbit.target, eye);
      const toGalaxy = sub([0, 0, 0], eye);
      const cos =
        dot(toTarget, toGalaxy) / (norm(toTarget) * norm(toGalaxy) || 1);
      const offAxisDeg = (Math.acos(Math.min(1, cos)) * 180) / Math.PI;
      const halfAngleDeg = (Math.atan(v.tanScale * TAN_H) * 180) / Math.PI;
      expect(offAxisDeg).toBeLessThan(halfAngleDeg);
    }
  });

  it("looks at the pair side-on rather than down the line joining them", () => {
    const c = farCtx();
    const v = galaxyView(1, c);
    const eye = eyePosition(v.orbit);
    // The eye must be well off the Sun-to-object axis; standing on it would stack them in one spot and
    // show no separation at all.
    const alongAxis = eye[0] * 0 + eye[1] * 0 + eye[2] * 1;
    const len = Math.hypot(eye[0], eye[1], eye[2]);
    expect(Math.abs(alongAxis) / len).toBeLessThan(0.5);
  });
});

type V3 = readonly [number, number, number] | readonly number[];
const sub = (a: V3, b: V3): [number, number, number] => [
  a[0] - b[0],
  a[1] - b[1],
  a[2] - b[2],
];
const dot = (a: V3, b: V3) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
const norm = (a: V3) => Math.hypot(a[0], a[1], a[2]);

/** elevationAtDeg is how far above the GALACTIC plane the eye stands, seen from the galactic centre. */
function elevationAtDeg(
  orbit: ReturnType<typeof galaxyView>["orbit"],
  c: JourneyContext,
): number {
  const eye = eyePosition(orbit);
  const gc = c.toGalacticCentre.map((x) => x * R_SUN_KPC);
  const rel = [eye[0] - gc[0], eye[1] - gc[1], eye[2] - gc[2]];
  const len = Math.hypot(rel[0], rel[1], rel[2]);
  const alongPole =
    (rel[0] * c.toNorthPole[0] +
      rel[1] * c.toNorthPole[1] +
      rel[2] * c.toNorthPole[2]) /
    len;
  return (Math.asin(alongPole) * 180) / Math.PI;
}

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

describe("the reference rings", () => {
  it("draws whole segments with a colour each, and no NaN", () => {
    const lines = buildGalaxyLines(galacticToScene(basis()));
    expect(lines.positions.length).toBeGreaterThan(0);
    expect(lines.positions.length % 6).toBe(0); // whole segments
    expect(lines.colors.length).toBe(lines.positions.length);
    for (const v of lines.positions) expect(Number.isFinite(v)).toBe(true);
  });

  it("stays inside the drawn disc when the scene does", () => {
    const lines = buildGalaxyLines(galacticToScene(basis()));
    for (let i = 0; i < lines.positions.length; i += 3) {
      const r = Math.hypot(
        lines.positions[i],
        lines.positions[i + 1],
        lines.positions[i + 2],
      );
      expect(r).toBeLessThan(R_SUN_KPC + DISC_EDGE_KPC + 1);
    }
  });

  it("adds a decade ladder out to a distant object", () => {
    // Without it the far end of the journey is two bright patches on black with no way to tell whether
    // they are a kiloparsec or a megaparsec apart.
    expect(decadeLadderKpc(0)).toEqual([]);
    expect(decadeLadderKpc(DISC_EDGE_KPC)).toEqual([]);
    expect(decadeLadderKpc(M51_PC / 1000)).toEqual([100, 1000, 10000]);
    // The object itself lands inside the outermost ring, not on it.
    const ladder = decadeLadderKpc(M51_PC / 1000);
    expect(ladder[ladder.length - 1]).toBeGreaterThan(M51_PC / 1000);

    const far = buildGalaxyLines(galacticToScene(basis()), M51_PC);
    const near = buildGalaxyLines(galacticToScene(basis()));
    expect(far.positions.length).toBeGreaterThan(near.positions.length);
  });
});
