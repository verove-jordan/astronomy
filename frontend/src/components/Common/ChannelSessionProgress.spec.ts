import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import ChannelSessionProgress from "./ChannelSessionProgress.vue";
import { testI18n } from "@/test/i18n";
import type { PhotomRecord, StagePreview } from "@/types";

function mountIt(
  previews: StagePreview[] = [],
  photom: PhotomRecord[] = [],
  currentSession = "",
) {
  return mount(ChannelSessionProgress, {
    props: {
      sessions: ["2023-02-27", "2023-03-15"],
      channels: ["L"],
      previews,
      photom,
      currentSession,
    },
    global: { plugins: [testI18n()] },
  });
}

const successChip = (w: ReturnType<typeof mountIt>, label: string) =>
  w
    .findAll("span")
    .filter((s) => s.text() === label)
    .map((s) => s.classes().join(" "));

describe("ChannelSessionProgress", () => {
  it("renders pending rows for the expected matrix", () => {
    const w = mountIt();
    expect(w.text()).toContain("2023-02-27");
    expect(w.text()).toContain("2023-03-15");
    const calibChips = successChip(w, "Calibrate");
    expect(calibChips).toHaveLength(2);
    calibChips.forEach((cls) => expect(cls).not.toContain("text-success"));
  });

  it("a prenorm preview flips that night's Calibrate to done", () => {
    const w = mountIt([
      {
        index: 1400,
        stage: "prenorm",
        filter: "L",
        session: "2023-02-27",
        png_path: "p.png",
      },
    ]);
    const calibChips = successChip(w, "Calibrate");
    expect(calibChips[0]).toContain("text-success");
    expect(calibChips[1]).not.toContain("text-success");
  });

  it("a photom record flips Normalize and shows its numbers + state pill", () => {
    const w = mountIt(
      [],
      [
        {
          label: "L g250 o10 30000ms t-25C",
          session: "2023-02-27",
          scale: 5.62,
          offset: 0.001,
          resid: 0.012,
          frames: 40,
          applied: true,
        },
      ],
    );
    expect(w.text()).toContain("×5.62");
    expect(w.text()).toContain("Applied");
    const normChips = successChip(w, "Normalize");
    expect(normChips[0]).toContain("text-success");
  });

  it("a stacked preview completes the channel-level chips", () => {
    const w = mountIt([
      { index: 100, stage: "stacked", filter: "L", png_path: "s.png" },
    ]);
    for (const label of ["Register", "Stack"]) {
      const chips = successChip(w, label);
      expect(chips[0]).toContain("text-success");
    }
  });

  it("an uncovered night×channel cell renders a dash, not forever-pending chips", () => {
    // The task #312 shape: G exists only on the second night — the first night's G row must say
    // "no data" instead of waiting for stages that will never come.
    const w = mount(ChannelSessionProgress, {
      props: {
        sessions: ["2023-02-27", "2023-03-15"],
        channels: ["G"],
        previews: [],
        photom: [],
        coverage: { G: ["2023-03-15"] },
      },
      global: { plugins: [testI18n()] },
    });
    const rows = w.findAll(".space-y-1 > div");
    expect(rows[0].text()).toContain("—");
    expect(rows[0].text()).not.toContain("Calibrate");
    expect(rows[1].text()).toContain("Calibrate");
  });

  it("an unknown coverage map keeps every cell active (no plan yet)", () => {
    const w = mountIt();
    expect(w.text()).not.toContain("—");
  });
});
