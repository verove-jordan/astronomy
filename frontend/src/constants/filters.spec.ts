import { describe, it, expect } from "vitest";
import {
  FILTERS,
  NARROWBAND,
  isNarrowband,
  filterRank,
  compareFilters,
  nextUnusedFilter,
} from "./filters";
import { FILTER_HEX } from "./colors";
import { filterChip } from "./styles";

describe("canonical filter set", () => {
  // This is the Go mirror check. internal/filters/filters.go Canonical must match exactly, or the
  // backend and the UI disagree about which filters exist — the drift that left SII half-wired.
  it("matches internal/filters.Canonical", () => {
    expect([...FILTERS]).toEqual(["L", "R", "G", "B", "Ha", "OIII", "SII"]);
  });

  it("treats only the emission lines as narrowband", () => {
    expect([...NARROWBAND]).toEqual(["Ha", "OIII", "SII"]);
    for (const f of NARROWBAND) expect(isNarrowband(f)).toBe(true);
    for (const f of ["L", "R", "G", "B", "Baader"])
      expect(isNarrowband(f)).toBe(false);
  });

  it("has a chip colour and a hex for every canonical filter", () => {
    for (const f of FILTERS) {
      expect(FILTER_HEX[f], `hex for ${f}`).toBeTruthy();
      expect(filterChip[f], `chip class for ${f}`).toBeTruthy();
    }
  });
});

describe("ordering", () => {
  it("ranks canonically and pushes unknown filters to the end", () => {
    expect(filterRank("L")).toBe(0);
    expect(filterRank("SII")).toBe(6);
    expect(filterRank("Baader")).toBe(FILTERS.length);
  });

  it("sorts canonically, then unknown filters alphabetically", () => {
    const got = ["SII", "zeta", "B", "Ha", "alpha", "L", "OIII", "R", "G"];
    got.sort(compareFilters);
    expect(got).toEqual([
      "L",
      "R",
      "G",
      "B",
      "Ha",
      "OIII",
      "SII",
      "alpha",
      "zeta",
    ]);
  });
});

describe("nextUnusedFilter", () => {
  it("walks the canonical order", () => {
    expect(nextUnusedFilter([])).toBe("L");
    expect(nextUnusedFilter(["L", "R", "G", "B"])).toBe("Ha");
    // The regression this guards: the mosaic capture plan used to stop at Ha, so a narrowband
    // panel could never be auto-suggested.
    expect(nextUnusedFilter(["L", "R", "G", "B", "Ha"])).toBe("OIII");
    expect(nextUnusedFilter(["L", "R", "G", "B", "Ha", "OIII"])).toBe("SII");
  });

  it("returns empty when every filter is taken", () => {
    expect(nextUnusedFilter(FILTERS)).toBe("");
  });
});
