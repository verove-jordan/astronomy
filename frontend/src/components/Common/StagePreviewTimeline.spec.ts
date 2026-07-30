import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import StagePreviewTimeline from "./StagePreviewTimeline.vue";
import { testI18n } from "@/test/i18n";
import type { PhotomRecord, StagePreview } from "@/types";

function mountIt(live: StagePreview[], photom: PhotomRecord[] = []) {
  return mount(StagePreviewTimeline, {
    props: { live, photom },
    global: { plugins: [testI18n()] },
  });
}

describe("StagePreviewTimeline", () => {
  it("session-less previews render one strip with no night headers (today's layout)", () => {
    const w = mountIt([
      { index: 300, stage: "combined", png_path: "c.png" },
      { index: 100, stage: "stacked", filter: "L", png_path: "s.png" },
    ]);
    expect(w.text()).not.toContain("Night of");
    // Sorted by index: stacked (100) before combined (300).
    const labels = w.findAll("span.truncate").map((s) => s.text());
    expect(labels[0]).toContain("Stacked");
  });

  it("session-tagged previews group into one row per night, run-level row last", () => {
    const w = mountIt([
      { index: 900, stage: "final", png_path: "f.png" },
      {
        index: 1402,
        stage: "prenorm",
        filter: "L",
        session: "2023-03-15",
        png_path: "b.png",
      },
      {
        index: 1400,
        stage: "prenorm",
        filter: "L",
        session: "2023-02-27",
        png_path: "a.png",
      },
    ]);
    const text = w.text();
    expect(text).toContain("Night of 2023-02-27");
    expect(text).toContain("Night of 2023-03-15");
    const i27 = text.indexOf("Night of 2023-02-27");
    const i15 = text.indexOf("Night of 2023-03-15");
    expect(i27).toBeLessThan(i15);
  });

  it("captions a normalized card with its photom numbers", () => {
    const w = mountIt(
      [
        {
          index: 1401,
          stage: "normalized",
          filter: "L",
          session: "2023-03-15",
          png_path: "n.png",
        },
      ],
      [
        {
          label: "L g250 o10 30000ms t-25C",
          session: "2023-03-15",
          scale: 5.62,
          offset: 0.001,
          resid: 0.01,
          frames: 20,
          applied: true,
        },
      ],
    );
    expect(w.text()).toContain("×5.62");
  });
});
