import { describe, it, expect, afterEach } from "vitest";
import { mount } from "@vue/test-utils";
import TourModal from "./TourModal.vue";
import { testI18n } from "@/test/i18n";
import { tourSteps } from "@/constants/tour";
import en from "@/i18n/en.json";

// The modal teleports to <body>, so its markup is never inside the wrapper: assertions read the
// document. attachTo keeps the teleport target and the window keydown listener in the same document.
function mountIt(page = "runs") {
  return mount(TourModal, {
    props: { page },
    global: { plugins: [testI18n()] },
    attachTo: document.body,
  });
}

const shown = () => document.body.textContent ?? "";
const press = (key: string) =>
  window.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));

const runsStep = (i: number) => {
  const key = tourSteps("runs")[i] as keyof typeof en.tour.runs.steps;
  return en.tour.runs.steps[key];
};

afterEach(() => {
  document.body.innerHTML = "";
});

describe("TourModal", () => {
  it("opens on the first step and shows its real copy", () => {
    const w = mountIt("runs");
    expect(shown()).toContain(runsStep(0).title);
    expect(shown()).toContain(en.tour.runs.title);
    w.unmount();
  });

  it("advances and goes back through the steps", async () => {
    const w = mountIt("runs");
    press("ArrowRight");
    await w.vm.$nextTick();
    expect(shown()).toContain(runsStep(1).title);

    press("ArrowLeft");
    await w.vm.$nextTick();
    expect(shown()).toContain(runsStep(0).title);
    w.unmount();
  });

  it("wraps around at the ends", async () => {
    const w = mountIt("runs");
    // Going back from the first step lands on the last, so arrowing never dead-ends.
    press("ArrowLeft");
    await w.vm.$nextTick();
    expect(shown()).toContain(runsStep(tourSteps("runs").length - 1).title);
    w.unmount();
  });

  it("closes on Escape", async () => {
    const w = mountIt();
    press("Escape");
    await w.vm.$nextTick();
    expect(w.emitted("close")).toBeTruthy();
    w.unmount();
  });

  it("closes on a backdrop click", async () => {
    const w = mountIt();
    const backdrop = document.body.querySelector<HTMLElement>(
      '[aria-hidden="true"]',
    );
    backdrop?.click();
    await w.vm.$nextTick();
    expect(w.emitted("close")).toBeTruthy();
    w.unmount();
  });

  it("stops listening for keys once closed", () => {
    const w = mountIt();
    w.unmount();
    // A stale window listener would keep stepping an unmounted tour and throw on the next render.
    expect(() => press("ArrowRight")).not.toThrow();
  });

  it("shows a caption instead of a broken image when a shot is missing", async () => {
    const w = mountIt("runs");
    const img = document.body.querySelector<HTMLImageElement>("img");
    expect(img?.getAttribute("src")).toBe("/tour/en/runs-gallery.webp");
    // The English shot IS the fallback here, so one failure exhausts both and the frame becomes
    // text. The copy carries the step on its own; a broken-image icon would carry nothing.
    img?.dispatchEvent(new Event("error"));
    await w.vm.$nextTick();
    expect(document.body.querySelector("img")).toBeNull();
    expect(shown()).toContain(en.tour.noShot);
    w.unmount();
  });
});
