import { createTestingPinia } from "@pinia/testing";
import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

import PolarCameraPanel from "./PolarCameraPanel.vue";
import { usePolarCamStore } from "@/stores/polarCam";
import { useCaptureStore } from "@/stores/capture";
import type { PolarCamState } from "@/types";
import { testI18n } from "@/test/i18n";

// The panel is the instruction sheet for a procedure done in the dark, so what is tested is what it
// SAYS, not how it is built: the right step highlighted, the right direction named, and the knob angle
// quoted rather than the sky angle it is so easily confused with.

vi.mock("@/services/api", () => ({
  apiGet: vi.fn(async () => ({ state: idleState() })),
  apiPost: vi.fn(async () => ({ state: idleState() })),
}));

function idleState(): PolarCamState {
  return {
    phase: "idle",
    step: 0,
    points: 4,
    step_arc_deg: 20,
    samples: [],
    busy: false,
    tracking: true,
  };
}

function solvedState(over: Partial<PolarCamState> = {}): PolarCamState {
  return {
    ...idleState(),
    phase: "solved",
    step: 4,
    axis: {
      alt_deg: 49.35,
      az_deg: 0.6,
      radius_deg: 70,
      arc_deg: 60,
      residual_arcsec: 1.2,
      sigma_arcsec: 9,
      samples: 4,
    },
    correction: {
      alt_error_deg: 0.5, // 30′ too high
      az_error_deg: 0.25, // 15′ east on the sky…
      az_knob_deg: 0.5, // …but 30′ of knob at this latitude
      total_arcmin: 33.5,
      alt_move: "lower",
      az_move: "west",
      quality: "poor",
    },
    ...over,
  };
}

function mountPanel(state: PolarCamState, cameraConnected = true) {
  const wrapper = mount(PolarCameraPanel, {
    global: {
      plugins: [
        createTestingPinia({ createSpy: vi.fn, stubActions: true }),
        testI18n(),
      ],
    },
  });
  const polar = usePolarCamStore();
  const capture = useCaptureStore();
  polar.state = state;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (capture as any).camera = { connected: cameraConnected };
  return wrapper;
}

describe("PolarCameraPanel", () => {
  beforeEach(() => vi.clearAllMocks());

  it("will not start without a camera, because there is nothing to measure with", async () => {
    const wrapper = mountPanel(idleState(), false);
    await wrapper.vm.$nextTick();

    const start = wrapper.findAll("button")[0];
    expect(start.attributes("disabled")).toBeDefined();
  });

  it("asks for the turn between frames while measuring", async () => {
    const wrapper = mountPanel({ ...idleState(), phase: "measuring", step: 2 });
    await wrapper.vm.$nextTick();

    // The instruction has to name the RA axis and the angle; turning the wrong axis silently ruins
    // the measurement, and this is the only place the user is told which one.
    expect(wrapper.text()).toContain("2");
    expect(wrapper.text().toLowerCase()).toContain("ascension");
    expect(wrapper.text()).toContain("20");
  });

  // The trap this whole panel exists to avoid: the azimuth adjuster turns through MORE than the error
  // it removes. Quoting the sky angle makes every user undershoot by cos(latitude), every time.
  it("quotes the KNOB angle for azimuth, not the sky angle", async () => {
    const wrapper = mountPanel(solvedState());
    await wrapper.vm.$nextTick();

    const text = wrapper.text();
    expect(text).toContain("30′00″"); // az_knob_deg 0.5° — what the bolt turns through
    expect(text).not.toContain("15′00″"); // az_error_deg 0.25° — the sky angle, which would undershoot
  });

  // Which compass direction to move the axis depends on the hemisphere, so the panel renders the
  // backend's WORD and never reasons from the sign of a number.
  it("renders the direction words the backend chose", async () => {
    const wrapper = mountPanel(solvedState());
    await wrapper.vm.$nextTick();

    const text = wrapper.text().toLowerCase();
    expect(text).toContain("lower");
    expect(text).toContain("west");
    expect(text).not.toContain("raise");
    expect(text).not.toContain("east");
  });

  it("says there is nothing to do when the mount is already aligned", async () => {
    const wrapper = mountPanel(
      solvedState({
        correction: {
          alt_error_deg: 0.002,
          az_error_deg: 0.001,
          az_knob_deg: 0.002,
          total_arcmin: 0.2,
          alt_move: "ok",
          az_move: "ok",
          quality: "excellent",
        },
      }),
    );
    await wrapper.vm.$nextTick();

    expect(wrapper.text().toLowerCase()).toContain("nothing to do");
  });

  it("warns when the marker is off the frame, which is normal on a first measurement", async () => {
    const wrapper = mountPanel({
      ...solvedState(),
      phase: "adjusting",
      live: {
        target: {
          ra_deg: 10,
          dec_deg: 20,
          x: -300,
          y: 100,
          nx: -0.06,
          ny: 0.03,
          offset_px: 3200,
          off_frame: true,
          offset_arcmin: 56,
        },
        remaining_arcmin: 33,
        quality: "poor",
      },
    });
    await wrapper.vm.$nextTick();

    expect(wrapper.text().toLowerCase()).toContain("off the edge");
  });

  it("says so when the mount appears to have been knocked", async () => {
    const wrapper = mountPanel({
      ...solvedState(),
      phase: "adjusting",
      live: {
        target: {
          ra_deg: 10,
          dec_deg: 20,
          x: 100,
          y: 100,
          nx: 0.5,
          ny: 0.5,
          offset_px: 10,
          off_frame: false,
          offset_arcmin: 1,
        },
        remaining_arcmin: 2,
        quality: "good",
        suspect: true,
      },
    });
    await wrapper.vm.$nextTick();

    expect(wrapper.text().toLowerCase()).toContain("measure again");
  });

  it("translates the fit's warning codes instead of showing them raw", async () => {
    const wrapper = mountPanel(solvedState({ warnings: ["weak_arc"] }));
    await wrapper.vm.$nextTick();

    expect(wrapper.text()).not.toContain("weak_arc");
    expect(wrapper.text()).toContain("60");
  });
});
