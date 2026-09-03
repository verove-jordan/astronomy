import { describe, it, expect } from "vitest";
import {
  bortleColor,
  bortleLabel,
  bortleRampColor,
  BORTLE_COLORS,
} from "./bortle";

describe("bortleColor", () => {
  it("maps each class to its swatch", () => {
    expect(bortleColor(1)).toBe(BORTLE_COLORS[0]);
    expect(bortleColor(9)).toBe(BORTLE_COLORS[8]);
  });

  it("rounds a fractional class to the nearest swatch", () => {
    expect(bortleColor(4.2)).toBe(BORTLE_COLORS[3]);
    expect(bortleColor(4.8)).toBe(BORTLE_COLORS[4]);
  });
});

describe("bortleLabel", () => {
  it("prints the continuous class when the engine supplies one", () => {
    expect(bortleLabel(4, 4.24)).toBe("4.2");
    expect(bortleLabel(3, 2.95)).toBe("3.0");
  });

  it("separates two sites the integer class calls identical", () => {
    // Both are "Bortle 4" — the decimal is the only thing that ranks them.
    expect(bortleLabel(4, 4.1)).not.toBe(bortleLabel(4, 4.8));
  });

  it("falls back to the integer class when no fraction is sent", () => {
    expect(bortleLabel(5)).toBe("5");
    expect(bortleLabel(5, undefined)).toBe("5");
    expect(bortleLabel(5, 0)).toBe("5");
  });

  it("shows a dash for no data rather than a flattering pristine sky", () => {
    // The API reports 0 when it knows nothing; printing that as 1 would claim the darkest sky there is.
    expect(bortleLabel(0)).toBe("—");
    expect(bortleLabel(0, 0)).toBe("—");
  });

  it("ignores an out-of-range fraction", () => {
    expect(bortleLabel(6, 12)).toBe("6");
    expect(bortleLabel(6, Number.NaN)).toBe("6");
  });
});

// The engine paints the overlay by lerping the same palette (internal/lightpollution/render.go
// gradientColor). A marker sitting ON that gradient has to be coloured the same way, or a fractional
// reading shows a swatch the ramp under it never contains.
describe("bortleRampColor", () => {
  const rgb = (hex: string) =>
    `rgb(${parseInt(hex.slice(1, 3), 16)}, ${parseInt(hex.slice(3, 5), 16)}, ${parseInt(hex.slice(5, 7), 16)})`;

  it("lands exactly on the palette at whole classes", () => {
    for (let b = 1; b <= 9; b++) {
      expect(bortleRampColor(b)).toBe(rgb(BORTLE_COLORS[b - 1]));
    }
  });

  it("blends between the two neighbouring stops", () => {
    // Halfway from class 3 (#1f4ea3) to class 4 (#2e8b57).
    expect(bortleRampColor(3.5)).toBe("rgb(39, 109, 125)");
  });

  it("clamps outside the scale instead of wrapping", () => {
    expect(bortleRampColor(0)).toBe(rgb(BORTLE_COLORS[0]));
    expect(bortleRampColor(99)).toBe(rgb(BORTLE_COLORS[8]));
  });
});
