import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import StagePreviewTimeline from "./StagePreviewTimeline.vue";
import { testI18n } from "@/test/i18n";
import type { PhotomRecord, StagePreview } from "@/types";

const jobStages = vi.fn();
const exportJobStage = vi.fn();
vi.mock("@/services/api", () => ({
  fileUrl: (p: string) => `/api/file?path=${p}`,
  thumbUrl: (p: string) => p,
  jobStages: (...a: unknown[]) => jobStages(...a),
  exportJobStage: (...a: unknown[]) => exportJobStage(...a),
}));

function mountIt(
  live: StagePreview[],
  photom: PhotomRecord[] = [],
  jobId?: number,
) {
  return mount(StagePreviewTimeline, {
    props: { live, photom, jobId },
    global: { plugins: [testI18n()] },
  });
}

beforeEach(() => {
  jobStages.mockReset();
  exportJobStage.mockReset();
});

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

  // Downloads live on the frames themselves. A card whose stage the run still holds gets
  // full-resolution PNG/TIF; a card whose source was reprocessed in place falls back to the
  // half-scale preview rather than offering a full-resolution file that cannot be produced.
  it("offers full-resolution PNG and TIF on a frame the run can still export", async () => {
    jobStages.mockResolvedValue({
      stages: [
        {
          key: "stacked_RGB",
          label: "Stacked master (RGB)",
          path: "/m.fits",
          linear: true,
          order: 100,
        },
      ],
    });
    const w = mountIt(
      [{ index: 100, stage: "stacked", filter: "RGB", png_path: "s.png" }],
      [],
      736,
    );
    await flushPromises();
    const labels = w
      .findAll('[data-demo="stage-download"] button')
      .map((b) => b.text());
    expect(labels).toEqual(["png", "tif"]);
  });

  it("falls back to the preview download on a frame with no preserved source", async () => {
    jobStages.mockResolvedValue({ stages: [] });
    const w = mountIt(
      [{ index: 300, stage: "combined", png_path: "c.png" }],
      [],
      736,
    );
    await flushPromises();
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    await w.find('[data-demo="stage-download"] button').trigger("click");
    expect(exportJobStage).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledWith("/api/file?path=c.png", "_blank");
    open.mockRestore();
  });

  it("renders the requested format from a frame and opens the written file", async () => {
    jobStages.mockResolvedValue({
      stages: [
        {
          key: "final",
          label: "Final image",
          path: "/f.tif",
          linear: false,
          order: 900,
        },
      ],
    });
    exportJobStage.mockResolvedValue({ path: "/run/export/final.tif" });
    const open = vi.spyOn(window, "open").mockImplementation(() => null);

    const w = mountIt(
      [{ index: 900, stage: "final", png_path: "f.png" }],
      [],
      12,
    );
    await flushPromises();
    const buttons = w.findAll('[data-demo="stage-download"] button');
    await buttons[1].trigger("click"); // [png, tif]
    await flushPromises();

    expect(exportJobStage).toHaveBeenCalledWith(12, "final", "tif");
    expect(open).toHaveBeenCalledWith(
      "/api/file?path=/run/export/final.tif",
      "_blank",
    );
    open.mockRestore();
  });

  it("shows the same downloads inside the enlarge modal", async () => {
    jobStages.mockResolvedValue({
      stages: [
        {
          key: "final",
          label: "Final image",
          path: "/f.tif",
          linear: false,
          order: 900,
        },
      ],
    });
    const w = mountIt(
      [{ index: 900, stage: "final", png_path: "f.png" }],
      [],
      12,
    );
    await flushPromises();
    await w.find('[data-demo="stage-preview-frame"]').trigger("click");
    await flushPromises();
    const modal = w.find(".fixed.inset-0");
    expect(modal.exists()).toBe(true);
    const text = modal.text();
    expect(text).toContain("PNG");
    expect(text).toContain("TIF");
  });

  it("lists only stages that have no frame in the strip", async () => {
    jobStages.mockResolvedValue({
      stages: [
        {
          key: "final",
          label: "Final image",
          path: "/f.tif",
          linear: false,
          order: 900,
        },
        {
          key: "denoised",
          label: "Combined, AI colour denoised",
          path: "/d.fits",
          linear: true,
          order: 320,
        },
      ],
    });
    const w = mountIt(
      [{ index: 900, stage: "final", png_path: "f.png" }],
      [],
      12,
    );
    await flushPromises();
    const row = w.find('[data-demo="stage-fullres"]');
    expect(row.text()).toContain("Combined, AI colour denoised");
    expect(row.text()).not.toContain("Final image"); // it already has a frame
  });

  it("shows no download row without a job id (the Runs gallery mounts from run.json)", async () => {
    const w = mountIt([{ index: 900, stage: "final", png_path: "f.png" }]);
    await flushPromises();
    expect(jobStages).not.toHaveBeenCalled();
    expect(w.find('[data-demo="stage-fullres"]').exists()).toBe(false);
  });

  it("surfaces an export failure instead of failing silently", async () => {
    jobStages.mockResolvedValue({
      stages: [
        {
          key: "final",
          label: "Final image",
          path: "/f.tif",
          linear: false,
          order: 900,
        },
      ],
    });
    exportJobStage.mockRejectedValue(new Error("siril wrote no tif"));
    const w = mountIt(
      [{ index: 900, stage: "final", png_path: "f.png" }],
      [],
      12,
    );
    await flushPromises();
    await w.findAll('[data-demo="stage-download"] button')[0].trigger("click");
    await flushPromises();
    expect(w.text()).toContain("siril wrote no tif");
  });
});
