// Procedural Milky Way band for the GoTo sky map — no data asset. The band is generated once
// (module const) in galactic coordinates and converted to equatorial J2000; the canvas composable
// then treats its vertices exactly like catalogue stars (precess → alt/az per observer/time).

const DEG = Math.PI / 180;
const RAD = 180 / Math.PI;

// IAU J2000 galactic frame: equatorial coordinates of the north galactic pole and the position
// angle of the celestial pole along the galactic equator.
const RA_GP = 192.85948 * DEG;
const DEC_GP = 27.12825 * DEG;
const L_NCP = 122.93192 * DEG;

function norm360(d: number): number {
  d = d % 360;
  return d < 0 ? d + 360 : d;
}

// galacticToEquatorial converts galactic (l, b) to equatorial J2000 (ra, dec), all in degrees.
export function galacticToEquatorial(
  lDeg: number,
  bDeg: number,
): { ra: number; dec: number } {
  const l = lDeg * DEG;
  const b = bDeg * DEG;
  const dl = L_NCP - l;
  const sinDec =
    Math.sin(DEC_GP) * Math.sin(b) +
    Math.cos(DEC_GP) * Math.cos(b) * Math.cos(dl);
  const y = Math.cos(b) * Math.sin(dl);
  const x =
    Math.cos(DEC_GP) * Math.sin(b) -
    Math.sin(DEC_GP) * Math.cos(b) * Math.cos(dl);
  return {
    ra: norm360((RA_GP + Math.atan2(y, x)) * RAD),
    dec: Math.asin(Math.max(-1, Math.min(1, sinDec))) * RAD,
  };
}

// Control points along galactic longitude: [l°, half-width°, brightness 0..1]. Wide + bright at the
// bulge (l≈0/360), bright again at Cygnus (l≈80) and Carina (l≈285), thin + faint at the
// anticentre (l≈180). Linearly interpolated; the last point mirrors l=0 so the band closes.
const PROFILE: [number, number, number][] = [
  [0, 14, 1.0],
  [30, 12, 0.92],
  [60, 10, 0.78],
  [80, 11, 0.88],
  [110, 8, 0.6],
  [140, 7, 0.45],
  [180, 5.5, 0.3],
  [220, 6, 0.35],
  [250, 7.5, 0.55],
  [285, 10, 0.85],
  [310, 11, 0.9],
  [335, 12.5, 0.95],
  [360, 14, 1.0],
];

// profileAt interpolates the band half-width and brightness at galactic longitude l.
function profileAt(lDeg: number): { width: number; brightness: number } {
  const l = norm360(lDeg);
  for (let i = 1; i < PROFILE.length; i++) {
    const [l1, w1, b1] = PROFILE[i - 1];
    const [l2, w2, b2] = PROFILE[i];
    if (l <= l2) {
      const f = (l - l1) / (l2 - l1);
      return { width: w1 + (w2 - w1) * f, brightness: b1 + (b2 - b1) * f };
    }
  }
  return { width: PROFILE[0][1], brightness: PROFILE[0][2] };
}

const STEP_DEG = 3; // l sampling step
const STRIPS = [1.0, 0.6, 0.3]; // nested half-width fractions (outer → inner core)

// MilkyWayBand is the precomputed band geometry in equatorial J2000 degrees: for each nested strip
// s and longitude sample i, the upper (+w·f) and lower (−w·f) edge vertices, plus the per-sample
// brightness the renderer multiplies into its fill alpha.
export interface MilkyWayBand {
  samples: number; // longitude sample count (l = 0..360 inclusive)
  strips: number; // nested strip count
  brightness: Float32Array; // per-sample brightness 0..1
  topRa: Float32Array[]; // [strip][sample]
  topDec: Float32Array[];
  botRa: Float32Array[];
  botDec: Float32Array[];
}

function buildBand(): MilkyWayBand {
  const n = Math.floor(360 / STEP_DEG) + 1;
  const band: MilkyWayBand = {
    samples: n,
    strips: STRIPS.length,
    brightness: new Float32Array(n),
    topRa: STRIPS.map(() => new Float32Array(n)),
    topDec: STRIPS.map(() => new Float32Array(n)),
    botRa: STRIPS.map(() => new Float32Array(n)),
    botDec: STRIPS.map(() => new Float32Array(n)),
  };
  for (let i = 0; i < n; i++) {
    const l = i * STEP_DEG;
    const { width, brightness } = profileAt(l);
    band.brightness[i] = brightness;
    for (let s = 0; s < STRIPS.length; s++) {
      const top = galacticToEquatorial(l, width * STRIPS[s]);
      const bot = galacticToEquatorial(l, -width * STRIPS[s]);
      band.topRa[s][i] = top.ra;
      band.topDec[s][i] = top.dec;
      band.botRa[s][i] = bot.ra;
      band.botDec[s][i] = bot.dec;
    }
  }
  return band;
}

export const MILKY_WAY: MilkyWayBand = buildBand();
