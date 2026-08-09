import { describe, expect, it } from "vitest";
import {
  DISC_EDGE_KPC,
  R_SUN_KPC,
  Z_SUN_PC,
  galactocentricToHeliocentric,
} from "@/utils/galaxy";
import { equatorialToGalactic, galacticToCartesian } from "@/utils/galactic";

// utils/galaxy.ts is a MIRROR. The Milky Way's structure — arms, disc, bulge, bar, halo, colours —
// lives in internal/scene3d/galaxymodel.go and is tested there against real astronomy: where W3 falls
// in Perseus, where the Sun sits relative to its own arm, that the low-pitch arms stay arcs rather than
// closing into rings. What is left here is the handful of numbers the CAMERA needs, and the one
// coordinate helper that turns them into positions.
//
// So these tests do two jobs: pin the mirrored constants against their Go originals, and check the
// helper against facts established somewhere other than this file.

describe("the mirrored constants", () => {
  // If one of these fails, the Go constant of the same name has moved and the two are now describing
  // different galaxies — the camera would frame a disc the point cloud does not fill.
  it("matches internal/scene3d/galaxymodel.go", () => {
    expect(R_SUN_KPC).toBe(8.15); // RSunKpc — Reid et al. 2019's assumed value, not GRAVITY's 8.178
    expect(Z_SUN_PC).toBe(20.8); // ZSunPc — Bennett & Bovy 2019
    expect(DISC_EDGE_KPC).toBe(15); // DiscEdgeKpc — where the drawn stellar disc ends
  });
});

describe("the galactocentric frame", () => {
  it("puts the Sun at its own radius, just above the plane", () => {
    // Azimuth 0 at the Sun's radius IS the Sun, and the midplane is below it.
    const [x, y, z] = galactocentricToHeliocentric(R_SUN_KPC, 0, 0);
    expect(x).toBeCloseTo(0, 12);
    expect(y).toBeCloseTo(0, 12);
    expect(z).toBeCloseTo(-Z_SUN_PC / 1000, 12);
  });

  it("puts the galactic centre where the sky says it is", () => {
    // Radius 0 is the centre, and it must land on the l = 0, b = 0 ray at the Sun's own distance —
    // which is the only check here that the frame is anchored to the real sky and not just to itself.
    const [x, y, z] = galactocentricToHeliocentric(0, 0, 0);
    const g0 = equatorialToGalactic(266.4049962, -28.9361724); // Sgr A*
    const [sx, sy, sz] = galacticToCartesian(g0.l, g0.b, R_SUN_KPC * 1000);
    const cos =
      (x * sx + y * sy + (z + Z_SUN_PC / 1000) * sz) /
      (Math.hypot(x, y, z + Z_SUN_PC / 1000) * Math.hypot(sx, sy, sz));
    expect(cos).toBeCloseTo(1, 4);
  });

  it("walks the azimuth the way the Galaxy turns", () => {
    // Beta increases toward galactic rotation, which the Sun moves toward at l = 90° — so a point a
    // little past the Sun in azimuth must have a positive y in heliocentric galactic coordinates. The
    // opposite sign here mirrors the whole Galaxy, and it is the single easiest thing to get wrong.
    const [, y] = galactocentricToHeliocentric(R_SUN_KPC, 5, 0);
    expect(y).toBeGreaterThan(0);
  });

  it("keeps the drawn disc inside the frame the camera fits to", () => {
    // Every point of the disc's rim has to be within one disc radius of the centre — the journey's
    // framing assumes exactly that.
    const [cx, cy] = galactocentricToHeliocentric(0, 0, 0);
    for (let beta = 0; beta < 360; beta += 17) {
      const [x, y] = galactocentricToHeliocentric(DISC_EDGE_KPC, beta, 0);
      expect(Math.hypot(x - cx, y - cy)).toBeCloseTo(DISC_EDGE_KPC, 9);
    }
  });
});
