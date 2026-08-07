// Canonical stacking modes, mirrored from internal/mode/preset.go ParseMode.
//
// This list lives here rather than inline in the launch view so it can be pinned by a spec against
// the Go enum. That drift is not hypothetical: the filter list was duplicated in two places for
// months and SII ended up half-supported because one copy stopped at Ha.

export const MODES = [
  "deepsky",
  "nebula",
  "milkyway",
  "planetary",
  "comet",
  "mosaic",
  "sun",
] as const;

export type Mode = (typeof MODES)[number];

// STAR_MODES are the modes whose results carry a star analysis. Solar and planetary subjects have
// no stars to count, so their result panels omit it.
export const STAR_MODES: readonly string[] = [
  "deepsky",
  "nebula",
  "livestack",
  "comet",
  "milkyway",
];

// PAUSABLE_MODES are the modes with a safe mid-run boundary to pause at.
export const PAUSABLE_MODES: readonly string[] = ["deepsky", "nebula"];
