// Bortle-class colors for the light-pollution legend and the site-quality badge, ordered dark (class 1)
// → bright (class 9), echoing the usual light-pollution-map palette (black/blue/green/yellow/orange/
// red/white). Shared so the legend strip and the badge dot stay in sync.
export const BORTLE_COLORS = [
  "#0b1026", // 1 pristine
  "#13205a", // 2 typical truly dark
  "#1f4ea3", // 3 rural
  "#2e8b57", // 4 rural/suburban transition
  "#c9c43e", // 5 suburban
  "#e0922e", // 6 bright suburban
  "#d8442c", // 7 suburban/urban transition
  "#cf6fae", // 8 city
  "#f3f3f3", // 9 inner city
];

// bortleColor returns the swatch for a Bortle class (1..9), clamped, with a neutral mid fallback.
export function bortleColor(bortle: number): string {
  if (!Number.isFinite(bortle)) return BORTLE_COLORS[4];
  const i = Math.min(9, Math.max(1, Math.round(bortle))) - 1;
  return BORTLE_COLORS[i];
}

// bortleLabel renders a reading at the resolution the data actually supports. The scale is defined in
// nine integer classes, but the sky-brightness model behind it resolves far finer — two sites both
// called "Bortle 4" can sit a third of a class apart — so the fraction is printed whenever the API
// supplies one, with the integer class as the fallback. 0 means "no data" and prints as a dash rather
// than as the flattering pristine 1 it would otherwise round to.
export function bortleLabel(bortle: number, bortleF?: number | null): string {
  const f =
    typeof bortleF === "number" && Number.isFinite(bortleF) ? bortleF : 0;
  if (f >= 1 && f <= 9) return f.toFixed(1);
  return bortle >= 1 && bortle <= 9 ? String(bortle) : "—";
}

// bortleRampColor is the CONTINUOUS analogue of bortleColor: the palette lerped between its two nearest
// stops, mirroring the overlay renderer's gradientColor (internal/lightpollution/render.go) so a marker
// drawn at a fractional class matches the gradient underneath it exactly. bortleColor stays the right
// choice wherever a swatch stands for a whole class (the finder's table, the site badge); this is for
// anything positioned on the ramp itself.
export function bortleRampColor(bortle: number): string {
  if (!Number.isFinite(bortle)) return BORTLE_COLORS[4];
  const p =
    (Math.min(9, Math.max(1, bortle)) - 1) * ((BORTLE_COLORS.length - 1) / 8);
  const lo = Math.floor(p);
  const hi = Math.min(lo + 1, BORTLE_COLORS.length - 1);
  const f = p - lo;
  const ch = (hex: string, i: number) =>
    parseInt(hex.slice(1 + i * 2, 3 + i * 2), 16);
  const mix = (i: number) =>
    Math.round(
      ch(BORTLE_COLORS[lo], i) +
        (ch(BORTLE_COLORS[hi], i) - ch(BORTLE_COLORS[lo], i)) * f,
    );
  return `rgb(${mix(0)}, ${mix(1)}, ${mix(2)})`;
}
