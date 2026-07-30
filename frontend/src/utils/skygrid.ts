import { tangentPlane, tangentSky } from "@/utils/astro";

// Pure geometry for hand-framing a mosaic on the sky map: hit-testing tile footprints and
// translating/rotating them about the grid centre while the user drags. All angles are degrees,
// J2000; ξ is east-positive and η north-positive, matching the Go planner (internal/mosaicplan).
//
// The drag preview is computed client-side so it tracks the pointer at 60 fps; the server recomputes
// the authoritative plan once, on release.

export type Corner = [number, number]; // [raDeg, decDeg]

export interface SkyPoint {
  ra: number;
  dec: number;
}

/** norm180 wraps an RA difference into (−180, 180]. */
export function norm180(deg: number): number {
  const r = ((deg % 360) + 360) % 360;
  return r > 180 ? r - 360 : r;
}

/**
 * pointInPolygon ray-casts a sky position against a footprint, in a locally-flat frame around the
 * point itself (RA deltas wrap-normalised and cos-dec scaled). Local flattening is what keeps this
 * correct near the pole and across the 0h meridian.
 */
export function pointInPolygon(pos: SkyPoint, corners: Corner[]): boolean {
  const cosDec = Math.max(Math.cos((pos.dec * Math.PI) / 180), 1e-3);
  const poly = corners.map(([cra, cdec]) => ({
    x: norm180(cra - pos.ra) * cosDec,
    y: cdec - pos.dec,
  }));
  let inside = false;
  for (let a = 0, b = poly.length - 1; a < poly.length; b = a++) {
    const crosses =
      poly[a].y > 0 !== poly[b].y > 0 &&
      0 <
        poly[b].x +
          ((0 - poly[b].y) / (poly[a].y - poly[b].y)) * (poly[a].x - poly[b].x);
    if (crosses) inside = !inside;
  }
  return inside;
}

/** hitTile returns the index of the first footprint containing pos, or −1. */
export function hitTile(pos: SkyPoint, tiles: { corners: Corner[] }[]): number {
  for (let i = 0; i < tiles.length; i++) {
    if (pointInPolygon(pos, tiles[i].corners)) return i;
  }
  return -1;
}

/**
 * rotateXi applies the planner's pinned rotation to a tangent-plane offset: a camera position angle
 * of `paDeg` maps a frame offset onto ξ = u·cos + v·sin, η = −u·sin + v·cos, so rotating the drawn
 * grid by a PA delta uses exactly the same law.
 */
function rotate(
  xi: number,
  eta: number,
  paDeg: number,
): { xi: number; eta: number } {
  if (!paDeg) return { xi, eta };
  const rad = (paDeg * Math.PI) / 180;
  const s = Math.sin(rad);
  const c = Math.cos(rad);
  return { xi: xi * c + eta * s, eta: -xi * s + eta * c };
}

/**
 * offsetCorners previews a drag: each corner is projected into the tangent plane at the grid centre,
 * rotated by the PA delta, shifted by the translation, and projected back. Corners that cannot be
 * projected (antipodal to the centre — impossible for a real grid) are passed through unchanged.
 */
export function offsetCorners(
  corners: Corner[],
  center: SkyPoint,
  dXiDeg: number,
  dEtaDeg: number,
  dPaDeg = 0,
): Corner[] {
  return corners.map(([cra, cdec]) => {
    const flat = tangentPlane(center.ra, center.dec, cra, cdec);
    if (!flat) return [cra, cdec] as Corner;
    const spun = rotate(flat.xi, flat.eta, dPaDeg);
    const moved = tangentSky(
      center.ra,
      center.dec,
      spun.xi + dXiDeg,
      spun.eta + dEtaDeg,
    );
    return [moved.ra, moved.dec] as Corner;
  });
}

/** movedCenter is where the grid centre lands after a drag of (dXi, dEta) degrees. */
export function movedCenter(
  center: SkyPoint,
  dXiDeg: number,
  dEtaDeg: number,
): SkyPoint {
  const p = tangentSky(center.ra, center.dec, dXiDeg, dEtaDeg);
  return { ra: p.ra, dec: p.dec };
}

/**
 * ellipseCorners approximates an object's catalogued ellipse as a closed polygon, so the planner can
 * draw what it is actually trying to cover underneath the tile grid. Sizes are arcminutes, the
 * position angle is degrees east of north (the OpenNGC convention).
 *
 * The mapping is written out rather than reusing the frame rotation above, because the two differ by
 * a sign and getting it wrong draws the object MIRRORED — an ellipse that crosses the galaxy instead
 * of lying along it. Position angle is measured east of north, so the major axis runs
 * (sin PA, cos PA) in (ξ east, η north): at PA 0 it points north, at PA 90 it points EAST.
 */
export function ellipseCorners(
  center: SkyPoint,
  majorArcmin: number,
  minorArcmin: number,
  paDeg: number,
  points = 48,
): Corner[] {
  const a = majorArcmin / 120; // semi-major, degrees
  const b = (minorArcmin > 0 ? minorArcmin : majorArcmin) / 120;
  const rad = (paDeg * Math.PI) / 180;
  const sin = Math.sin(rad);
  const cos = Math.cos(rad);
  const out: Corner[] = [];
  for (let i = 0; i < points; i++) {
    const th = (2 * Math.PI * i) / points;
    const along = a * Math.cos(th); // distance along the major axis
    const across = b * Math.sin(th); // …and across it
    const xi = along * sin + across * cos;
    const eta = along * cos - across * sin;
    const p = tangentSky(center.ra, center.dec, xi, eta);
    out.push([p.ra, p.dec]);
  }
  return out;
}
