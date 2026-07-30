// The canonical filter set, mirroring internal/filters (Go). Every list of filters in the UI — chip
// colours, sort orders, "next unused filter" pickers, capture-plan seeds — must come from here.
//
// It exists because these lists used to be copy-pasted into seven components and drifted: two of them
// stopped at Ha, so a narrowband row could never be auto-suggested. Keep in sync with
// internal/filters/filters.go.

// FILTERS is wheel/display order: luminance, the RGB broadband trio, then the narrowband lines.
export const FILTERS = ["L", "R", "G", "B", "Ha", "OIII", "SII"] as const;

export type Filter = (typeof FILTERS)[number];

// NARROWBAND is the emission-line subset: these share the emission-screen knobs and the narrowband
// palettes, and are the ones a broadband-only rig will not have.
export const NARROWBAND: readonly string[] = ["Ha", "OIII", "SII"];

export function isNarrowband(filter: string): boolean {
  return NARROWBAND.includes(filter);
}

// filterRank is a filter's position in FILTERS, or FILTERS.length for a custom one — so unknown
// filters sort after the known set rather than interleaving with it.
export function filterRank(filter: string): number {
  const i = (FILTERS as readonly string[]).indexOf(filter);
  return i === -1 ? FILTERS.length : i;
}

// compareFilters orders two filter names canonically, unknown ones alphabetically at the end.
export function compareFilters(a: string, b: string): number {
  return filterRank(a) - filterRank(b) || a.localeCompare(b);
}

// nextUnusedFilter picks the first canonical filter not already taken — the "add a row" default for
// the capture sequencer and the mosaic capture plan.
export function nextUnusedFilter(used: Iterable<string>): string {
  const taken = new Set(used);
  return FILTERS.find((f) => !taken.has(f)) ?? "";
}
