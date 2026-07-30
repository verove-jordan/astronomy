import { describe, expect, it } from "vitest";
import { decToDMS, decToHC, raToHC, raToHMS } from "./sexagesimal";

describe("raToHMS", () => {
  it.each([
    [0, "00h 00m 00.0s"],
    [83.633, "05h 34m 31.9s"], // M1
    [10.6847, "00h 42m 44.3s"], // M31
    [359.9999999, "00h 00m 00.0s"], // rounding carries past 24h → wraps
    [-0.5, "23h 58m 00.0s"], // negative input normalized
  ])("formats %f as %s", (deg, want) => {
    expect(raToHMS(deg)).toBe(want);
  });
});

describe("decToDMS", () => {
  it.each([
    [22.0145, "+22° 00′ 52″"],
    [41.2687, "+41° 16′ 07″"],
    [-5.391, "-05° 23′ 28″"],
    [-0.2083, "-00° 12′ 30″"], // sign survives on −0°
    [89.99999, "+90° 00′ 00″"], // carry clamps at the pole
  ])("formats %f as %s", (deg, want) => {
    expect(decToDMS(deg)).toBe(want);
  });
});

describe("hand-controller variants", () => {
  it("are bare zero-padded keypad strings", () => {
    expect(raToHC(10.6847)).toBe("00 42 44");
    expect(decToHC(41.2687)).toBe("+41 16 07");
    expect(decToHC(-0.2083)).toBe("-00 12 30");
  });
  it("carries integer-second rounding", () => {
    expect(raToHC(14.999999)).toBe("01 00 00");
  });
});
