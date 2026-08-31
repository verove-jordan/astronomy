import { describe, it, expect } from "vitest";
import en from "@/i18n/en.json";
import { MODES, STAR_MODES } from "./modes";
import { groupsForMode } from "./paramDocs";

describe("canonical mode set", () => {
  // The Go mirror check. internal/mode/preset.go ParseMode must accept exactly these, or the
  // backend and the UI disagree about which modes exist. livestack is deliberately absent: it is
  // launched from its own view rather than from the mode picker.
  it("matches internal/mode.ParseMode", () => {
    expect([...MODES]).toEqual([
      "deepsky",
      "nebula",
      "milkyway",
      "nightpano",
      "planetary",
      "comet",
      "mosaic",
      "sun",
      "eclipse",
    ]);
  });

  it("every mode has a display name in en.json", () => {
    for (const m of MODES) {
      expect(
        (en as Record<string, any>).run.modes[m],
        `run.modes.${m}`,
      ).toBeTruthy();
    }
  });

  it("every mode resolves to a knob-group set", () => {
    for (const m of MODES) {
      expect(groupsForMode(m).length, `groupsForMode(${m})`).toBeGreaterThan(0);
    }
  });

  it("star analysis is limited to modes that have stars", () => {
    expect(STAR_MODES).not.toContain("sun");
    expect(STAR_MODES).not.toContain("eclipse");
    expect(STAR_MODES).not.toContain("planetary");
  });
});
