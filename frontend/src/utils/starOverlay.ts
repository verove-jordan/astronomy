import type { DetectedStar, StarLabelExtent } from "@/types";

// Geometry for the star/DSO overlay's object footprints. Kept out of the canvas component so the
// "is this big enough to be worth outlining" rule is unit-testable — happy-dom has no 2D context,
// so anything left inside draw() is untestable by construction.

// MIN_OUTLINE_PX is the on-SCREEN minor axis below which an extended object keeps the plain anchor
// dot instead of an outline. Deliberately judged after the viewer transform rather than in image
// pixels, so the test means exactly "can you see it right now": a small galaxy is a dot at fit and
// grows a real footprint as you zoom in. It also stops a wide field of small Sh2/LDN regions from
// rendering as a nest of tiny rings.
export const MIN_OUTLINE_PX = 16;

export interface ScreenEllipse {
  rx: number;
  ry: number;
  angle: number;
}

// outlineFor converts a label's footprint (final-image pixels, as projected by the engine) to screen
// radii, or null when there is no catalogued size or the object is currently too small to outline.
// `k` is the image-pixel → screen-pixel factor (natW/imageW × viewer scale); it is uniform, so it
// cannot shear the ellipse and the engine's angle passes through untouched.
export function outlineFor(
  extent: StarLabelExtent | undefined,
  k: number,
): ScreenEllipse | null {
  if (!extent || !(k > 0)) return null;
  const { rx_px: rxPx, ry_px: ryPx, angle_rad: angle } = extent;
  if (!(rxPx > 0) || !(ryPx > 0)) return null;
  const rx = rxPx * k;
  const ry = ryPx * k;
  if (Math.min(rx, ry) * 2 < MIN_OUTLINE_PX) return null;
  return { rx, ry, angle };
}

// MARKER_MIN_SEP_PX keeps two markers from merging into a blob. At fit zoom a rich field packs
// thousands of stars into a few hundred screen pixels, where drawing them all is not information —
// it is a grey wash that hides the image.
export const MARKER_MIN_SEP_PX = 5;

export interface ScreenPoint {
  x: number;
  y: number;
  r: number; // marker radius in SCREEN px, scaled from the star's measured half-max radius
  hex?: string; // the star's own colour, when the master had one
  star: DetectedStar; // kept so a hover can answer "what is this?" without a second lookup
}

// MARKER_MIN_R / MARKER_MAX_R bound the drawn radius. A star's measured size scales with zoom like
// everything else, but a ring must stay a ring: below the minimum it is a dot with no readable
// centre, and above the maximum a bloated bright star would draw a hoop across the frame.
export const MARKER_MIN_R = 2.5;
export const MARKER_MAX_R = 26;

// markerRadius turns a star's measured half-max radius into a drawn radius. The +1.6 padding keeps
// the ring just OUTSIDE the star's own disc, so the marker frames the star instead of covering it.
export function markerRadius(star: DetectedStar, k: number): number {
  const measured = (star.r_px ?? 1.5) * k + 1.6;
  return Math.min(MARKER_MAX_R, Math.max(MARKER_MIN_R, measured));
}

export interface MarkerTransform {
  k: number; // image px → screen px
  tx: number;
  ty: number;
}

// selectStarMarkers picks which detected stars to plot right now. `stars` arrives brightest-first,
// so taking the first `limit` that are on screen and far enough apart means the brightest always
// win — and it is what makes zooming reveal MORE stars: zooming in shrinks the on-screen set, so the
// same budget reaches deeper into the list instead of being spent on stars you already saw.
//
// Separation is enforced through a hash grid rather than pairwise distance, so a 5000-star field
// stays O(n) per redraw and can run inside the viewer's rAF loop.
export function selectStarMarkers(
  stars: readonly DetectedStar[] | undefined,
  t: MarkerTransform,
  view: { w: number; h: number },
  limit: number,
  minSepPx: number = MARKER_MIN_SEP_PX,
): ScreenPoint[] {
  if (!stars?.length || limit <= 0 || !(t.k > 0)) return [];
  const cell = Math.max(1, minSepPx);
  const taken = new Set<string>();
  const out: ScreenPoint[] = [];
  const margin = minSepPx;
  for (const s of stars) {
    if (out.length >= limit) break;
    const x = t.tx + s.x * t.k;
    const y = t.ty + s.y * t.k;
    if (
      x < -margin ||
      y < -margin ||
      x > view.w + margin ||
      y > view.h + margin
    ) {
      continue;
    }
    const cx = Math.floor(x / cell);
    const cy = Math.floor(y / cell);
    // Claim a 3×3 block so neighbours in adjacent cells cannot land within one separation either.
    let crowded = false;
    for (let gy = cy - 1; gy <= cy + 1 && !crowded; gy++) {
      for (let gx = cx - 1; gx <= cx + 1; gx++) {
        if (taken.has(`${gx},${gy}`)) {
          crowded = true;
          break;
        }
      }
    }
    if (crowded) continue;
    taken.add(`${cx},${cy}`);
    out.push({ x, y, r: markerRadius(s, t.k), hex: s.hex, star: s });
  }
  return out;
}
