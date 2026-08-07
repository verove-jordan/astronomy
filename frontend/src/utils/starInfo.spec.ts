import { describe, expect, it } from "vitest";
import type { StarCatalogInfo } from "@/types";
import {
  compact,
  effectiveTemperatureK,
  formatDec,
  formatRA,
  lightYears,
  solarLuminosity,
  spectralClass,
} from "@/utils/starInfo";

// Vega as ATHYG actually ships it.
const VEGA: StarCatalogInfo = {
  name: "Vega",
  mag: 0.03,
  ra_deg: 279.2349,
  dec_deg: 38.78369,
  dist_pc: 7.6748,
  absmag: 0.6,
  ci: 0.003,
  rv_km_s: -13.5,
  spect: "A0V",
  con: "Lyr",
};

describe("lightYears", () => {
  it("converts the catalogue's parsecs", () => {
    // Vega is famously 25 light years away.
    expect(lightYears(VEGA)).toBeCloseTo(25.03, 1);
  });

  it("treats a missing or impossible distance as unknown, not as zero", () => {
    expect(lightYears(undefined)).toBeNull();
    expect(lightYears({})).toBeNull();
    expect(lightYears({ dist_pc: 0 })).toBeNull();
    expect(lightYears({ dist_pc: -5 })).toBeNull();
  });
});

describe("solarLuminosity", () => {
  it("puts Vega at about 50 suns", () => {
    // 10^((4.83 − 0.60)/2.5) — Vega is the textbook ~50 L☉ star.
    expect(solarLuminosity(VEGA)).toBeCloseTo(49.2, 1);
  });

  it("returns exactly 1 for a star as bright as the Sun", () => {
    expect(solarLuminosity({ absmag: 4.83 })).toBeCloseTo(1, 6);
  });

  it("keeps absolute magnitude 0 — a real, very luminous value — distinct from unknown", () => {
    expect(solarLuminosity({ absmag: 0 })).toBeCloseTo(85.5, 1);
    expect(solarLuminosity({ absmag: null })).toBeNull();
    expect(solarLuminosity({})).toBeNull();
  });
});

describe("effectiveTemperatureK", () => {
  it("reproduces the Sun from its colour index", () => {
    // B−V 0.65 is the Sun, whose accepted effective temperature is 5772 K. Ballesteros' formula
    // was fitted to land here, and it does — to within about 7 K.
    expect(effectiveTemperatureK({ ci: 0.65 })).toBeCloseTo(5778, -1);
  });

  it("ranks a blue star hotter than a red one", () => {
    // The approximation compresses the hot end — a real B0 at B−V = -0.3 is nearer 30 000 K than
    // the 16 600 K this returns — so the card calls it approximate. The ORDERING, which is what a
    // reader takes from it, holds across the whole range.
    const blue = effectiveTemperatureK({ ci: -0.3 })!;
    const red = effectiveTemperatureK({ ci: 1.6 })!;
    expect(blue).toBeGreaterThan(15000);
    expect(red).toBeLessThan(4000);
    expect(blue).toBeGreaterThan(effectiveTemperatureK({ ci: 0.65 })!);
  });

  it("keeps B−V = 0 — a real A0 star — distinct from unknown", () => {
    expect(effectiveTemperatureK({ ci: 0 })).toBeCloseTo(10125, -2);
    expect(effectiveTemperatureK({ ci: null })).toBeNull();
    expect(effectiveTemperatureK({})).toBeNull();
  });

  it("refuses colour indices outside the range the formula was fitted on", () => {
    // The approximation has poles near B−V ≈ -0.67 and -1.85; a negative or absurd temperature is
    // not a measurement and must not reach the card.
    expect(effectiveTemperatureK({ ci: -0.7 })).toBeNull();
    expect(effectiveTemperatureK({ ci: -2.4 })).toBeNull();
  });
});

describe("spectralClass", () => {
  it("takes the Harvard letter off a full MK type", () => {
    expect(spectralClass({ spect: "A0V" })).toBe("A");
    expect(spectralClass({ spect: "B7 III/IV" })).toBe("B");
    expect(spectralClass({ spect: "G2 V" })).toBe("G");
  });

  it("says nothing for a type it does not recognise", () => {
    expect(spectralClass({ spect: "" })).toBe("");
    expect(spectralClass({ spect: "DA3" })).toBe(""); // white dwarf — no colour word applies
    expect(spectralClass(undefined)).toBe("");
  });
});

describe("coordinates", () => {
  it("renders RA in hours and Dec in signed degrees", () => {
    // Vega: 18h 36m 56s, +38° 47′ 01″.
    expect(formatRA(VEGA.ra_deg)).toBe("18h 36m 56.4s");
    expect(formatDec(VEGA.dec_deg)).toBe("+38° 47′ 01″");
  });

  it("keeps a southern declination negative and wraps RA into 0–24h", () => {
    expect(formatDec(-5.4409)).toMatch(/^−05°/);
    expect(formatRA(370)).toBe(formatRA(10));
  });

  it("renders nothing without a position", () => {
    expect(formatRA(undefined)).toBe("");
    expect(formatDec(undefined)).toBe("");
  });
});

describe("compact", () => {
  it("keeps small numbers precise and large ones readable", () => {
    expect(compact(0)).toBe("0");
    expect(compact(0.0004)).toBe("0.0004");
    expect(compact(1.7)).toBe("1.7");
    expect(compact(48.53)).toBe("49");
    expect(compact(1246)).toBe("1 250");
    expect(compact(25000)).toBe("25 000");
  });
});
