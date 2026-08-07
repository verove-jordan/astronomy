// The Milky Way, as a thing you can draw around a photograph.
//
// Everything here is in GALACTOCENTRIC coordinates and kiloparsecs: the origin is the galactic
// centre, x runs toward the Sun, y in the direction of galactic rotation, z toward the north
// galactic pole. Galactocentric azimuth β is measured from the Sun's direction, increasing the way
// the Galaxy turns — Reid et al.'s convention, and the single easiest sign here to get backwards.
//
// Two honesty rules the rest of the app already follows and this must too:
//
//   - Structure is a MODEL, not a measurement. The arm loci are fitted to a few hundred masers over
//     a limited range of azimuth; drawing them right round the Galaxy is extrapolation and the UI
//     should say so. The distances of the user's own stars are measured; these are not.
//   - Nothing here reads anything off the drawn picture. A mirrored field (right_handed: false,
//     which both real runs are) draws the Galaxy chirally flipped — correct, because the photograph
//     is itself a mirror — so every number quoted to the user is computed in this frame, never
//     recovered from the image.

const DEG = Math.PI / 180;

// --- scalars -----------------------------------------------------------------------------------

/**
 * R_SUN_KPC is the Sun's distance from the galactic centre.
 *
 * 8.15 rather than the more precise 8.178 ± 0.026 from GRAVITY 2019 (A&A 625, L10), because this is
 * the value Reid et al. 2019 (ApJ 885, 131) assumed when fitting the arm loci below. The difference
 * is 28 pc — a fifth of the narrowest arm's width — so internal consistency with the arms beats
 * absolute accuracy by a wide margin. Mixing the two would push every arm off by that much.
 */
export const R_SUN_KPC = 8.15;

/** Z_SUN_PC is the Sun's height above the galactic plane (Bennett & Bovy 2019, MNRAS 482, 1417). */
export const Z_SUN_PC = 20.8;

/** Exponential scale length of the stellar disc (Bland-Hawthorn & Gerhard 2016, 2.6 ± 0.5 kpc). */
export const DISC_SCALE_LENGTH_KPC = 2.6;

/** Thin-disc scale height (Bland-Hawthorn & Gerhard 2016; the 220–450 pc range's middle). */
export const DISC_SCALE_HEIGHT_KPC = 0.3;

/**
 * Where the drawn disc ends, with a cosine fade before it so the rim is not a cut circle. The
 * stellar break is near 13–15 kpc; the gas disc runs considerably further, which is worth saying in
 * the legend rather than implying the Galaxy simply stops.
 */
export const DISC_EDGE_KPC = 15;
export const DISC_FADE_KPC = 2;

/** Boxy/peanut bulge semi-axes (Wegg & Gerhard 2013, MNRAS 435, 1874). */
export const BULGE_SEMI_KPC: readonly [number, number, number] = [
  2.2, 1.4, 1.2,
];

/** Long-bar semi-axes (Wegg, Gerhard & Portail 2015, MNRAS 450, 4050). */
export const BAR_SEMI_KPC: readonly [number, number, number] = [5.0, 0.9, 0.18];

/**
 * Angle of the bar to the Sun–centre line, near end at positive galactic longitude, so β_bar = +27°
 * (Bland-Hawthorn & Gerhard 2016 give 27° ± 2° for the boxy bulge).
 */
export const BAR_ANGLE_DEG = 27;

// --- spiral arms -------------------------------------------------------------------------------

/**
 * Arm is one log-spiral, from Reid et al. 2019 Table 2.
 *
 * The locus is ln(R / R_kink) = −(β − β_kink) · tan ψ, with a different pitch angle either side of
 * the kink. `width` is the fitted Gaussian arm width — real, and much narrower than most pictures of
 * the Galaxy suggest.
 */
export interface Arm {
  key: string;
  rKinkKpc: number;
  betaKinkDeg: number;
  psiLowDeg: number;
  psiHighDeg: number;
  widthKpc: number;
  /**
   * The azimuth range Reid et al. actually MEASURED this arm over. Drawing outside it is
   * extrapolation, and for the low-pitch arms it is ruinous: Norma's inner pitch is −1°, so swept
   * through a full turn its "spiral" closes into a circle and the map fills with concentric rings
   * instead of arms. The sweep is this range with a modest margin, and no more.
   */
  betaMinDeg: number;
  betaMaxDeg: number;
  /** betaLabelDeg is where to hang the name — chosen for legibility, not measured. */
  betaLabelDeg: number;
}

/**
 * The four major arms plus the Local Spur (where the Sun is) and the Outer arm.
 *
 * The 3-kpc arm is deliberately absent: it is a bar-driven ring, and drawing it as a spiral would
 * misrepresent what it is.
 */
export const ARMS: readonly Arm[] = [
  {
    key: "norma",
    betaMinDeg: 5,
    betaMaxDeg: 54,
    rKinkKpc: 4.46,
    betaKinkDeg: 18,
    psiLowDeg: -1.0,
    psiHighDeg: 19.5,
    widthKpc: 0.14,
    betaLabelDeg: 40,
  },
  {
    key: "scutum",
    betaMinDeg: 0,
    betaMaxDeg: 104,
    rKinkKpc: 4.91,
    betaKinkDeg: 23,
    psiLowDeg: 14.1,
    psiHighDeg: 12.1,
    widthKpc: 0.23,
    betaLabelDeg: 60,
  },
  {
    key: "sagittarius",
    betaMinDeg: 2,
    betaMaxDeg: 97,
    rKinkKpc: 6.04,
    betaKinkDeg: 24,
    psiLowDeg: 17.1,
    psiHighDeg: 1.0,
    widthKpc: 0.27,
    betaLabelDeg: 55,
  },
  {
    key: "local",
    betaMinDeg: -8,
    betaMaxDeg: 34,
    rKinkKpc: 8.26,
    betaKinkDeg: 9,
    psiLowDeg: 11.4,
    psiHighDeg: 11.4,
    widthKpc: 0.31,
    betaLabelDeg: 12,
  },
  {
    key: "perseus",
    betaMinDeg: -23,
    betaMaxDeg: 115,
    rKinkKpc: 8.87,
    betaKinkDeg: 40,
    psiLowDeg: 10.3,
    psiHighDeg: 8.7,
    widthKpc: 0.35,
    betaLabelDeg: 70,
  },
  {
    key: "outer",
    betaMinDeg: -16,
    betaMaxDeg: 71,
    rKinkKpc: 12.24,
    betaKinkDeg: 18,
    psiLowDeg: 3.0,
    psiHighDeg: 9.4,
    widthKpc: 0.65,
    betaLabelDeg: 45,
  },
];

/** armLocus is the galactocentric radius of an arm's ridge at azimuth β (degrees). */
export function armLocus(arm: Arm, betaDeg: number): number {
  const psi =
    (betaDeg < arm.betaKinkDeg ? arm.psiLowDeg : arm.psiHighDeg) * DEG;
  return (
    arm.rKinkKpc * Math.exp(-(betaDeg - arm.betaKinkDeg) * DEG * Math.tan(psi))
  );
}

/** Galactocentric cylindrical coordinates: radius, azimuth from the Sun's direction, height. */
export interface Galactocentric {
  rKpc: number;
  betaDeg: number;
  zKpc: number;
}

/**
 * heliocentricToGalactocentric converts a point given in HELIOCENTRIC galactic cartesian parsecs
 * (x toward the centre, y toward rotation, z toward the north pole) into the galactocentric
 * cylindrical frame the arm model lives in.
 */
export function heliocentricToGalactocentric(
  xPc: number,
  yPc: number,
  zPc: number,
): Galactocentric {
  // The Sun sits at x = +R_SUN from the centre, slightly above the plane.
  const x = R_SUN_KPC - xPc / 1000;
  // And here is the sign that is easiest in the whole file to get wrong, because getting it wrong
  // produces a Galaxy that still looks like a Galaxy — with every arm's azimuth mirrored.
  //
  // β increases in the direction of galactic ROTATION (Reid et al.'s convention, which the arm table
  // is fitted in), and the Sun moves toward l = 90°, i.e. toward +y in heliocentric galactic
  // coordinates. So the axis β is measured around must be +y, NOT −y — which means the triple
  // (toward the Sun, rotation, north pole) is LEFT-handed. Squaring it up into a right-handed frame,
  // which is the natural instinct, reflects every arm.
  //
  // Pinned by the W3 test: it is a maser-measured region in Perseus, and the wrong sign puts it
  // 0.70 kpc off the ridge instead of 0.15.
  const y = yPc / 1000;
  const z = (zPc + Z_SUN_PC) / 1000;
  return {
    rKpc: Math.hypot(x, y),
    betaDeg: (Math.atan2(y, x) * 180) / Math.PI,
    zKpc: z,
  };
}

/** ArmMatch is how close a point lies to an arm ridge, and how far it is off the plane. */
export interface ArmMatch {
  arm: Arm;
  /** offsetKpc is the radial distance from the ridge — negative inside it. */
  offsetKpc: number;
  /** widths is that offset in units of the arm's own fitted width. */
  widths: number;
  zKpc: number;
  /** offPlane is true when |z| is so large that arm membership means nothing. */
  offPlane: boolean;
}

/**
 * nearestArm finds the arm a point sits closest to.
 *
 * It reports `offPlane` rather than hiding it, because the arms are a thin-disc structure and a
 * point well above the disc has no meaningful arm at all. One of the two real runs demonstrates
 * exactly this: M51's field reaches 1.7 kpc off the plane — nearly six scale heights — where naming
 * an arm would be a confident answer to a question that does not apply.
 */
export function nearestArm(g: Galactocentric): ArmMatch | null {
  let best: ArmMatch | null = null;
  for (const arm of ARMS) {
    const offset = g.rKpc - armLocus(arm, g.betaDeg);
    if (best === null || Math.abs(offset) < Math.abs(best.offsetKpc)) {
      best = {
        arm,
        offsetKpc: offset,
        widths: offset / arm.widthKpc,
        zKpc: g.zKpc,
        offPlane: Math.abs(g.zKpc) > 2 * DISC_SCALE_HEIGHT_KPC,
      };
    }
  }
  return best;
}

/** discBrightness is the disc's surface brightness at (R, z), normalised to 1 at the centre. */
export function discBrightness(rKpc: number, zKpc: number): number {
  const radial = Math.exp(-rKpc / DISC_SCALE_LENGTH_KPC);
  // sech² is the isothermal-sheet profile; at z = 0 it is 1.
  const s = 1 / Math.cosh(zKpc / DISC_SCALE_HEIGHT_KPC);
  return radial * s * s * discEdgeFade(rKpc);
}

/** discEdgeFade takes the disc smoothly to nothing over the last DISC_FADE_KPC. */
export function discEdgeFade(rKpc: number): number {
  const start = DISC_EDGE_KPC - DISC_FADE_KPC;
  if (rKpc <= start) return 1;
  if (rKpc >= DISC_EDGE_KPC) return 0;
  return 0.5 * (1 + Math.cos((Math.PI * (rKpc - start)) / DISC_FADE_KPC));
}

/**
 * galactocentricToHeliocentric is the inverse of heliocentricToGalactocentric, in kiloparsecs — the
 * form the mesh builder needs, since everything is ultimately drawn relative to the observer.
 */
export function galactocentricToHeliocentric(
  rKpc: number,
  betaDeg: number,
  zKpc: number,
): [number, number, number] {
  const b = betaDeg * DEG;
  return [
    R_SUN_KPC - rKpc * Math.cos(b),
    rKpc * Math.sin(b),
    zKpc - Z_SUN_PC / 1000,
  ];
}
