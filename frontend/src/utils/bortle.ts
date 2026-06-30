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
