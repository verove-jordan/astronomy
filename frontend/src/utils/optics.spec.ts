import { describe, it, expect } from "vitest";
import {
  EXIT_PUPIL_MAX,
  effectiveFocalMm,
  exitPupilOutOfRange,
  eyepieceView,
} from "@/utils/optics";

// These cases deliberately pin the SAME numbers the Go tests pin, so the TS mirror cannot drift from
// internal/skyplan: skyplan_test.go TestOptics_FOV (740 mm / 3.8 µm → 1.059″/px, f/7.4),
// eyepiece_test.go TestOptics_View (10 mm/60° → 74×, 0.811°, 1.351 mm) and TestOptics_BarlowAndReducer
// (740 × 2 × 0.66 = 976.8 mm). If one side changes, one of these fails.
const FC100 = { focalMm: 740, apertureMm: 100 };

describe("effectiveFocalMm", () => {
  it("returns the native focal when no multiplier is fitted", () => {
    expect(effectiveFocalMm(740)).toBe(740);
    expect(effectiveFocalMm(740, undefined, undefined)).toBe(740);
  });

  it("treats a blank, zero or negative multiplier as not fitted (×1)", () => {
    expect(effectiveFocalMm(740, 0, 0)).toBe(740);
    expect(effectiveFocalMm(740, -1, -1)).toBe(740);
    expect(effectiveFocalMm(740, NaN, NaN)).toBe(740);
  });

  it("applies a Barlow and a reducer independently, and both together", () => {
    expect(effectiveFocalMm(740, 2)).toBeCloseTo(1480, 6);
    expect(effectiveFocalMm(740, undefined, 0.66)).toBeCloseTo(488.4, 6);
    expect(effectiveFocalMm(740, 2, 0.66)).toBeCloseTo(976.8, 6);
  });

  it("is zero for an unusable focal length", () => {
    expect(effectiveFocalMm(0, 2, 0.66)).toBe(0);
    expect(effectiveFocalMm(-740)).toBe(0);
  });
});

describe("eyepieceView", () => {
  it("matches the engine for the reference rig", () => {
    const v = eyepieceView(FC100.focalMm, FC100.apertureMm, {
      focal_mm: 10,
      afov_deg: 60,
    });
    expect(v.magX).toBeCloseTo(74.0, 1);
    expect(v.trueFovDeg).toBeCloseTo(0.811, 3);
    expect(v.exitPupilMm).toBeCloseTo(1.351, 3);
  });

  it("halves magnification and doubles exit pupil under a ×0.5 reducer", () => {
    const base = eyepieceView(740, 100, { focal_mm: 10, afov_deg: 60 });
    const reduced = eyepieceView(effectiveFocalMm(740, undefined, 0.5), 100, {
      focal_mm: 10,
      afov_deg: 60,
    });
    expect(reduced.magX).toBeCloseTo(base.magX / 2, 6);
    expect(reduced.exitPupilMm).toBeCloseTo(base.exitPupilMm * 2, 6);
    expect(reduced.trueFovDeg).toBeCloseTo(base.trueFovDeg * 2, 6);
  });

  it("produces the kit read-out the table shows at ×0.66", () => {
    const eff = effectiveFocalMm(740, undefined, 0.66); // 488.4 mm
    const kit = [
      { focal_mm: 30, afov_deg: 68 },
      { focal_mm: 18, afov_deg: 65 },
      { focal_mm: 10, afov_deg: 60 },
      { focal_mm: 6, afov_deg: 60 },
    ];
    const rows = kit.map((ep) => eyepieceView(eff, 100, ep));
    expect(rows.map((r) => Math.round(r.magX))).toEqual([16, 27, 49, 81]);
    expect(rows.map((r) => +r.trueFovDeg.toFixed(2))).toEqual([
      4.18, 2.4, 1.23, 0.74,
    ]);
    expect(rows.map((r) => +r.exitPupilMm.toFixed(1))).toEqual([
      6.1, 3.7, 2.0, 1.2,
    ]);
    // All four stay inside the comfortable window at this focal length.
    expect(rows.some(exitPupilOutOfRange)).toBe(false);
  });

  it("returns a zero view rather than NaN for an unusable row", () => {
    expect(eyepieceView(740, 100, { focal_mm: 0, afov_deg: 60 })).toEqual({
      magX: 0,
      trueFovDeg: 0,
      exitPupilMm: 0,
    });
    expect(eyepieceView(0, 100, { focal_mm: 10, afov_deg: 60 })).toEqual({
      magX: 0,
      trueFovDeg: 0,
      exitPupilMm: 0,
    });
  });
});

describe("exitPupilOutOfRange", () => {
  it("flags a too-wide exit pupil but not an empty row", () => {
    // A 40 mm eyepiece behind a ×0.66 reducer: 12.2× → 8.2 mm, past the dark-adapted eye's limit.
    const wide = eyepieceView(effectiveFocalMm(740, undefined, 0.66), 100, {
      focal_mm: 40,
      afov_deg: 70,
    });
    expect(wide.exitPupilMm).toBeGreaterThan(EXIT_PUPIL_MAX);
    expect(exitPupilOutOfRange(wide)).toBe(true);
    expect(
      exitPupilOutOfRange({ magX: 0, trueFovDeg: 0, exitPupilMm: 0 }),
    ).toBe(false);
  });

  it("flags a too-small exit pupil", () => {
    const tight = eyepieceView(740, 100, { focal_mm: 3, afov_deg: 60 });
    expect(exitPupilOutOfRange(tight)).toBe(true);
  });
});
