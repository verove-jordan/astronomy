import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import SessionBreakdown from "./SessionBreakdown.vue";
import { testI18n } from "@/test/i18n";
import type { RunPlanPreview, SessionInfo } from "@/types";

const nights: SessionInfo[] = [
  {
    key: "2023-02-27",
    start_ms: 1677535200000,
    end_ms: 1677544200000,
    counts: { LIGHT: 40, DARK: 30, FLAT: 210, BIAS: 53 },
    configs: [
      {
        filter: "L",
        exposure_ms: 30000,
        gain: 250,
        offset: 10,
        bin: 1,
        temp_bucket_c: -25,
        count: 40,
      },
    ],
  },
  {
    key: "2023-03-15",
    counts: { LIGHT: 20 },
    configs: [
      {
        filter: "L",
        exposure_ms: 60000,
        gain: 400,
        offset: 10,
        bin: 1,
        temp_bucket_c: -25,
        count: 20,
      },
    ],
  },
];

const plan: RunPlanPreview = {
  object: "M66",
  has_coords: true,
  channels: [
    {
      filter: "L",
      groups: [
        {
          session_id: 0,
          current: true,
          session: "2023-02-27",
          exposure_ms: 30000,
          gain: 250,
          offset: 10,
          temp_bucket_c: -25,
          bin: 1,
          frames: 40,
          flat: {
            source: "capture",
            master: {
              type: "FLAT",
              filter: "L",
              exposure_ms: 5,
              gain: 0,
              offset: 10,
              temp_milli_c: 0,
              bin: 1,
              frame_count: 46,
              path: "",
            },
          },
          dark: {
            source: "library",
            master: {
              type: "DARK",
              exposure_ms: 30000,
              gain: 250,
              offset: 10,
              temp_milli_c: -25000,
              bin: 1,
              frame_count: 30,
              path: "/lib/dark.fits",
            },
          },
        },
        {
          session_id: 0,
          current: true,
          session: "2023-03-15",
          exposure_ms: 60000,
          gain: 400,
          offset: 10,
          temp_bucket_c: -25,
          bin: 1,
          frames: 20,
          flat: { source: "session-rebuild", raw_flats: 18 },
        },
      ],
    },
  ],
  reuse: { prior_sessions: 0, prior_frames: 0, added_integration_ms: 0 },
};

function mountIt(sessions: SessionInfo[], p: RunPlanPreview | null = plan) {
  return mount(SessionBreakdown, {
    props: { sessions, plan: p },
    global: { plugins: [testI18n()] },
  });
}

describe("SessionBreakdown", () => {
  it("renders one panel per night with counts, filters and calib chips", () => {
    const w = mountIt(nights);
    expect(w.text()).toContain("Night of 2023-02-27");
    expect(w.text()).toContain("Night of 2023-03-15");
    expect(w.text()).toContain("30 DARK");
    expect(w.text()).toContain("210 FLAT");
  });

  it("maps each night's calibration with its provenance pill", () => {
    const w = mountIt(nights);
    expect(w.text()).toContain("from this capture");
    expect(w.text()).toContain("from your library");
    expect(w.text()).toContain("rebuilt from that night's flats");
  });

  it("renders NOTHING for a single-night selection (degradation contract)", () => {
    const w = mountIt([nights[0]]);
    expect(w.html()).not.toContain("Night of");
    expect(w.find("section").exists()).toBe(false);
  });

  it("shows the no-calibration line when the plan is missing", () => {
    const w = mountIt(nights, null);
    expect(w.text()).toContain("No matched calibration for this night.");
  });

  it("marks the anchor night and names each night's missing channels", () => {
    // Task #312's shape: the 2023 night shot L+R (here via configs), the older night only L —
    // the anchor pill sits on the plan's anchor night, and the L-only night says where R comes from.
    const uneven: SessionInfo[] = [
      {
        key: "2023-02-27",
        counts: { LIGHT: 200 },
        configs: [
          {
            filter: "L",
            exposure_ms: 30000,
            gain: 250,
            offset: 10,
            bin: 1,
            temp_bucket_c: -25,
            count: 100,
          },
          {
            filter: "R",
            exposure_ms: 30000,
            gain: 250,
            offset: 10,
            bin: 1,
            temp_bucket_c: -25,
            count: 100,
          },
        ],
      },
      {
        key: "2020-04-26",
        counts: { LIGHT: 30 },
        configs: [
          {
            filter: "L",
            exposure_ms: 90000,
            gain: 250,
            offset: 0,
            bin: 1,
            temp_bucket_c: 0,
            count: 30,
          },
        ],
      },
    ];
    const w = mountIt(uneven, { ...plan, anchor_night: "2023-02-27" });
    expect(w.text()).toContain("Anchor night");
    expect(w.text()).toContain("No R this night");
  });

  it("shows no anchor pill or missing-channel hint when nights are uniform", () => {
    const w = mountIt(nights, plan); // both fixture nights shoot L only, no anchor in the plan
    expect(w.text()).not.toContain("Anchor night");
    expect(w.text()).not.toContain("this night — those channels");
  });
});
