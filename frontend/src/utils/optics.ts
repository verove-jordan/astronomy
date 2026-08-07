import type { SkyEyepiece } from "@/types";

// Optics math mirrored from the engine so the eyepiece table can update WHILE you type, ahead of the
// 400 ms debounce and the round-trip that produce the server echo. Mirrored, not re-derived: these are
// line-for-line ports of internal/skyplan/optics.go (EffectiveFocalMM) and internal/skyplan/eyepiece.go
// (Optics.View), and optics.spec.ts pins them to the same numbers the Go tests pin — the same
// mirrored-and-pinned arrangement constants/filters.ts has with internal/filters.
//
// Anything the SERVER already computes for the whole rig (field of view, ″/px, f-ratio) still comes
// from the echo. Only the per-eyepiece view lives here.

// Exit-pupil window: below the minimum the magnification is empty and the image too dim, above it the
// dark-adapted eye clips the light cone and the sky washes out. Mirrors eyepiece.go's exitPupilMin /
// exitPupilMax.
export const EXIT_PUPIL_MIN = 0.5;
export const EXIT_PUPIL_MAX = 7.0;

// effectiveFocalMm folds the imaging train's multipliers into the focal length. Barlow and reducer are
// independent and compose (740 × 2 × 0.66); either at ≤0 — which is what a blank field parses to —
// means "not fitted" (×1). Mirrors skyplan.Optics.EffectiveFocalMM.
export function effectiveFocalMm(
  focalMm: number,
  barlowX?: number,
  reducerX?: number,
): number {
  let f = focalMm > 0 ? focalMm : 0;
  if (barlowX && barlowX > 0) f *= barlowX;
  if (reducerX && reducerX > 0) f *= reducerX;
  return f;
}

export interface EyepieceView {
  magX: number;
  trueFovDeg: number;
  exitPupilMm: number;
}

// eyepieceView evaluates one eyepiece against the scope: magnification = scopeFocal/epFocal,
// true field = apparent field / magnification, exit pupil = aperture / magnification. Returns a zero
// view (magX 0) for an unusable eyepiece or scope so callers can render a blank row rather than NaN or
// Infinity. Mirrors skyplan.Optics.View.
export function eyepieceView(
  effFocalMm: number,
  apertureMm: number,
  ep: Pick<SkyEyepiece, "focal_mm" | "afov_deg">,
): EyepieceView {
  if (!(ep.focal_mm > 0) || !(effFocalMm > 0)) {
    return { magX: 0, trueFovDeg: 0, exitPupilMm: 0 };
  }
  const magX = effFocalMm / ep.focal_mm;
  return {
    magX,
    trueFovDeg: ep.afov_deg > 0 ? ep.afov_deg / magX : 0,
    exitPupilMm: apertureMm > 0 ? apertureMm / magX : 0,
  };
}

// exitPupilOutOfRange flags a view the eye cannot use comfortably. A zero view (unusable row) is not
// "out of range" — it simply has nothing to say yet.
export function exitPupilOutOfRange(v: EyepieceView): boolean {
  if (v.magX <= 0 || v.exitPupilMm <= 0) return false;
  return v.exitPupilMm < EXIT_PUPIL_MIN || v.exitPupilMm > EXIT_PUPIL_MAX;
}
