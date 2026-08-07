import { describe, expect, it } from "vitest";
import {
  ARMS,
  DISC_EDGE_KPC,
  DISC_SCALE_HEIGHT_KPC,
  R_SUN_KPC,
  armLocus,
  discBrightness,
  discEdgeFade,
  galactocentricToHeliocentric,
  heliocentricToGalactocentric,
  nearestArm,
} from "@/utils/galaxy";
import { equatorialToGalactic, galacticToCartesian } from "@/utils/galactic";

// A structural model is easy to write and hard to know you got right — the numbers all look
// plausible. So these check it against facts established somewhere other than this file: where the
// Sun sits relative to its own arm, where a well-measured star-forming region falls, and where the
// user's own two photographs land. Anything that only checked the code against itself would pass
// just as happily with a sign error in the azimuth.

const armOf = (key: string) => {
  const a = ARMS.find((x) => x.key === key);
  if (!a) throw new Error(`no arm ${key}`);
  return a;
};

describe("the arm model against known astronomy", () => {
  it("puts the Sun just inside the Local Arm ridge", () => {
    // The accepted picture: the Sun sits on the INNER edge of the Local Arm (Orion Spur), a few
    // hundred parsecs short of the ridge — not on it, and not outside it.
    const ridge = armLocus(armOf("local"), 0);
    expect(ridge).toBeCloseTo(8.53, 1);
    const inside = ridge - R_SUN_KPC;
    expect(inside).toBeGreaterThan(0.2);
    expect(inside).toBeLessThan(0.6);
  });

  it("puts Sagittarius-Carina about 1.3 kpc inside the Sun", () => {
    // The next arm inward, the one that produces the bright summer Milky Way.
    expect(armLocus(armOf("sagittarius"), 0)).toBeCloseTo(6.87, 1);
    expect(R_SUN_KPC - armLocus(armOf("sagittarius"), 0)).toBeCloseTo(1.28, 1);
  });

  it("puts Perseus where W3 actually is", () => {
    // W3 is a maser-measured star-forming region in the Perseus arm: l = 133.95°, d ≈ 2.0 kpc.
    const [x, y, z] = galacticToCartesian(133.95, 1.06, 2000);
    const g = heliocentricToGalactocentric(x, y, z);
    expect(g.rKpc).toBeCloseTo(9.65, 1);
    const match = nearestArm(g);
    expect(match?.arm.key).toBe("perseus");
    // Inside the fitted arm width — the model reproduces a measurement it was not shown here.
    expect(Math.abs(match!.offsetKpc)).toBeLessThan(armOf("perseus").widthKpc);
  });

  it("is continuous across the kink in every arm", () => {
    // Two pitch angles meet at β_kink; a discontinuity there would draw a visible step in the arm.
    for (const arm of ARMS) {
      // Stepped either side of the kink: the two pitch angles must meet, not jump.
      const below = armLocus(arm, arm.betaKinkDeg - 1e-7);
      const above = armLocus(arm, arm.betaKinkDeg + 1e-7);
      expect(below).toBeCloseTo(above, 7);
      expect(armLocus(arm, arm.betaKinkDeg)).toBeCloseTo(arm.rKinkKpc, 9);
    }
  });
});

describe("where the user's own fields fall", () => {
  // M42's plate solution, from output/M42/20260723_180917.
  it("puts the Orion Nebula field on the Local Arm", () => {
    const g0 = equatorialToGalactic(83.8456513962952, -5.441118543410391);
    // The nebula is about 400 pc away.
    const [x, y, z] = galacticToCartesian(g0.l, g0.b, 400);
    const g = heliocentricToGalactocentric(x, y, z);
    const match = nearestArm(g);
    expect(match?.arm.key).toBe("local");
    // Comfortably within the arm, and on the plane where the question is meaningful.
    expect(Math.abs(match!.offsetKpc)).toBeLessThan(0.2);
    expect(match!.offPlane).toBe(false);
    // Orion is below the plane — a well-known fact about the region.
    expect(g.zKpc).toBeLessThan(0);
  });

  it("refuses to claim an arm for the M51 field, which is far off the plane", () => {
    const g0 = equatorialToGalactic(202.517528400277, 47.223093323086296);
    // Its stars reach 1841 pc, and it points nearly at the north galactic pole.
    const [x, y, z] = galacticToCartesian(g0.l, g0.b, 1841);
    const g = heliocentricToGalactocentric(x, y, z);
    expect(g.zKpc).toBeGreaterThan(1.5);
    expect(g.zKpc / DISC_SCALE_HEIGHT_KPC).toBeGreaterThan(5);
    // An arm is still nearest, but the flag is what stops the UI asserting membership.
    expect(nearestArm(g)?.offPlane).toBe(true);
  });
});

describe("the galactocentric frame", () => {
  it("places the Sun at the right radius and just above the plane", () => {
    const g = heliocentricToGalactocentric(0, 0, 0);
    expect(g.rKpc).toBeCloseTo(R_SUN_KPC, 9);
    expect(g.betaDeg).toBeCloseTo(0, 9);
    expect(g.zKpc).toBeCloseTo(0.0208, 9);
  });

  it("puts the galactic centre at the origin", () => {
    // Looking toward l = 0 at exactly R_SUN lands on the centre.
    const [x, y, z] = galacticToCartesian(0, 0, R_SUN_KPC * 1000);
    const g = heliocentricToGalactocentric(x, y, z);
    expect(g.rKpc).toBeCloseTo(0, 6);
  });

  it("puts the anticentre at twice the Sun's radius, opposite the centre", () => {
    const [x, y, z] = galacticToCartesian(180, 0, R_SUN_KPC * 1000);
    const g = heliocentricToGalactocentric(x, y, z);
    expect(g.rKpc).toBeCloseTo(2 * R_SUN_KPC, 6);
    // Azimuth is measured around the CENTRE, so the anticentre sits on the same ray as the Sun —
    // beta 0, twice as far out. It is "opposite" as seen from Earth, not as seen from the centre.
    expect(g.betaDeg).toBeCloseTo(0, 6);
  });

  it("round-trips to heliocentric and back", () => {
    for (const [r, beta, z] of [
      [5, 30, 0.1],
      [8.15, 0, 0],
      [12, -140, -0.4],
    ] as const) {
      const [hx, hy, hz] = galactocentricToHeliocentric(r, beta, z);
      const g = heliocentricToGalactocentric(hx * 1000, hy * 1000, hz * 1000);
      expect(g.rKpc).toBeCloseTo(r, 9);
      expect(g.zKpc).toBeCloseTo(z, 9);
      expect(((g.betaDeg - beta + 540) % 360) - 180).toBeCloseTo(0, 8);
    }
  });
});

describe("the disc profile", () => {
  it("falls off exponentially and vanishes at the drawn edge", () => {
    expect(discBrightness(0, 0)).toBeCloseTo(1, 9);
    // One scale length out is 1/e of the centre.
    expect(discBrightness(2.6, 0) / discBrightness(0, 0)).toBeCloseTo(
      Math.exp(-1),
      3,
    );
    expect(discEdgeFade(DISC_EDGE_KPC)).toBe(0);
    expect(discEdgeFade(0)).toBe(1);
    expect(discBrightness(DISC_EDGE_KPC, 0)).toBe(0);
  });

  it("thins away from the plane", () => {
    expect(discBrightness(8, 0)).toBeGreaterThan(discBrightness(8, 0.3));
    expect(discBrightness(8, 0.3)).toBeGreaterThan(discBrightness(8, 1.0));
    expect(discBrightness(8, 3)).toBeLessThan(discBrightness(8, 0) * 0.01);
  });

  it("never goes negative or NaN anywhere it will be sampled", () => {
    for (let r = 0; r <= DISC_EDGE_KPC; r += 0.37) {
      for (let z = -1.5; z <= 1.5; z += 0.29) {
        const v = discBrightness(r, z);
        expect(Number.isFinite(v)).toBe(true);
        expect(v).toBeGreaterThanOrEqual(0);
      }
    }
  });
});

describe("arms are arcs, not rings", () => {
  // The bug this pins: swept through a full turn, a low-pitch arm closes into a circle. Norma's
  // inner pitch is -1 degree, so over 520 degrees of azimuth its radius barely changes and it draws
  // as a ring — which is what filled the map with concentric bands instead of spiral arms.
  it("keeps every arm inside the azimuth range it was measured over", () => {
    for (const arm of ARMS) {
      expect(arm.betaMaxDeg).toBeGreaterThan(arm.betaMinDeg);
      // Nothing is fitted over more than one turn; extrapolating past that is what produced rings.
      expect(arm.betaMaxDeg - arm.betaMinDeg).toBeLessThan(180);
    }
  });

  it("does not let a swept arm double back over itself", () => {
    // Across its own drawn range (plus the 25-degree margin the mesh uses) an arm must stay a
    // monotonic spiral — never returning to a radius it already occupied.
    for (const arm of ARMS) {
      let prev = -Infinity;
      let increasing = true;
      let first = true;
      for (let b = arm.betaMinDeg - 25; b <= arm.betaMaxDeg + 25; b += 2) {
        const r = armLocus(arm, b);
        if (!first) {
          if (increasing && r < prev) increasing = false;
          // Once it starts falling it must keep falling: no oscillation, no closed loop.
          if (!increasing) expect(r).toBeLessThanOrEqual(prev + 1e-9);
        }
        prev = r;
        first = false;
      }
    }
  });
});
