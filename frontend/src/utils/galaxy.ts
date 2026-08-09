// The three Milky Way numbers the viewer needs for itself.
//
// This is a PINNED MIRROR, not a model. The Galaxy's structure — the Reid et al. 2019 arm loci, the
// disc and thick-disc profiles, the boxy bulge and long bar, the halo's broken power law, and the
// blackbody colour of each population — lives in `internal/scene3d/galaxymodel.go` and reaches the
// browser as a sampled point cloud (see `galaxycloud.ts`). Nothing here recomputes any of it.
//
// What is left is geometry the camera needs: where the Sun stands, how high above the plane, and how
// big the drawn disc is, so the journey can frame it and the reference rings can be drawn around it.
// `galaxy.spec.ts` pins these against the Go constants, exactly as `utils/optics.ts` is pinned — one
// canonical source, one mirror, and a test that fails if they drift.

/**
 * R_SUN_KPC is the Sun's distance from the galactic centre.
 *
 * 8.15 rather than the more precise 8.178 ± 0.026 from GRAVITY 2019 (A&A 625, L10), because 8.15 is
 * what Reid et al. 2019 (ApJ 885, 131) assumed when fitting the arm loci the cloud is sampled from.
 * Internal consistency with the arms beats absolute accuracy here by a wide margin.
 */
export const R_SUN_KPC = 8.15;

/** Z_SUN_PC is the Sun's height above the galactic plane (Bennett & Bovy 2019, MNRAS 482, 1417). */
export const Z_SUN_PC = 20.8;

/**
 * DISC_EDGE_KPC is where the drawn stellar disc ends. The stellar break is near 13–15 kpc; the gas
 * disc runs considerably further, which the legend says rather than implying the Galaxy simply stops.
 */
export const DISC_EDGE_KPC = 15;

/**
 * galactocentricToHeliocentric maps galactocentric cylindrical coordinates — radius, azimuth from the
 * Sun's direction increasing the way the Galaxy turns, height — to heliocentric galactic cartesian
 * kiloparsecs, with x toward the centre, y toward rotation and z toward the north galactic pole.
 *
 * The frame that triple describes is LEFT-handed, which is Reid et al.'s convention and the one the
 * point cloud is stored in. Squaring it up into a right-handed frame, which is the natural instinct,
 * mirrors the whole Galaxy.
 */
export function galactocentricToHeliocentric(
  rKpc: number,
  betaDeg: number,
  zKpc: number,
): [number, number, number] {
  const b = (betaDeg * Math.PI) / 180;
  return [
    R_SUN_KPC - rKpc * Math.cos(b),
    rKpc * Math.sin(b),
    zKpc - Z_SUN_PC / 1000,
  ];
}
