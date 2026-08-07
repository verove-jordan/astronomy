import { describe, expect, it } from "vitest";
import {
  hasConditions,
  integrationMs,
  moonPhaseKey,
  moonPenalty,
  nightKey,
  perFilterCounts,
  skyScore,
} from "./logbook";
import type { CaptureFrameStat, ConditionsSummary } from "@/types";

function stat(over: Partial<CaptureFrameStat> = {}): CaptureFrameStat {
  return {
    filter: "L",
    frame_type: "light",
    frames: 10,
    total_exposure_us: 600_000_000,
    min_exposure_us: 60_000_000,
    max_exposure_us: 60_000_000,
    min_gain: 100,
    max_gain: 100,
    min_bin: 1,
    max_bin: 1,
    min_temp_milli_c: -10_000,
    max_temp_milli_c: -10_000,
    avg_temp_milli_c: -10_000,
    first_ms: 0,
    last_ms: 0,
    ...over,
  };
}

function summary(over: Partial<ConditionsSummary> = {}): ConditionsSummary {
  const empty = { min: 0, median: 0, max: 0, n: 0 };
  return {
    samples: 4,
    first_ms: 0,
    last_ms: 0,
    cloud_pct: empty,
    cloud_low: empty,
    cloud_mid: empty,
    cloud_high: empty,
    seeing_arcsec: empty,
    transparency: empty,
    humidity_pct: empty,
    dew_spread_c: empty,
    temp_c: empty,
    wind_kmh: empty,
    gust_kmh: empty,
    precip_pct: empty,
    aod: empty,
    verdict: empty,
    moon_illum_max: 0,
    moon_alt_max_deg: 0,
    moon_up: false,
    moon_sep_min_deg: 0,
    moon_phase_angle_deg: 0,
    target_valid: false,
    target_alt_min_deg: 0,
    target_alt_max_deg: 0,
    target_airmass_min: 0,
    sqm: 0,
    bortle: 0,
    dew_risk_worst: "",
    kp_max: 0,
    aurora_max: "",
    source_counts: {},
    ...over,
  };
}

describe("moonPhaseKey", () => {
  const cases: [number, string][] = [
    [0, "new"],
    [45, "waxing_crescent"],
    [90, "first_quarter"],
    [135, "waxing_gibbous"],
    [180, "full"],
    [225, "waning_gibbous"],
    [270, "last_quarter"],
    [315, "waning_crescent"],
  ];
  it.each(cases)("%i° is %s", (angle, want) => {
    expect(moonPhaseKey(angle)).toBe(want);
  });

  it("wraps past a full turn instead of falling off the end", () => {
    expect(moonPhaseKey(360)).toBe("new");
    expect(moonPhaseKey(-45)).toBe("waning_crescent");
    expect(moonPhaseKey(725)).toBe("new"); // 725 − 720 = 5°
  });

  // The quarter names must not claim the whole half: 338° is still a waning crescent, 339° is new.
  it("buckets at the ±22.5° boundaries", () => {
    expect(moonPhaseKey(22)).toBe("new");
    expect(moonPhaseKey(23)).toBe("waxing_crescent");
  });
});

describe("nightKey", () => {
  // The whole point: an after-midnight sub belongs to the EVENING it started, which is what the
  // stacker's own grouping does. Built from local components so the test is timezone-independent.
  function localMs(y: number, m: number, d: number, h: number): number {
    return new Date(y, m - 1, d, h, 0, 0, 0).getTime();
  }

  it("puts an evening sub on its own date", () => {
    expect(nightKey(localMs(2026, 8, 2, 22))).toBe("2026-08-02");
  });

  it("puts an after-midnight sub on the evening before", () => {
    expect(nightKey(localMs(2026, 8, 3, 3))).toBe("2026-08-02");
  });

  it("switches night at local noon", () => {
    expect(nightKey(localMs(2026, 8, 3, 11))).toBe("2026-08-02");
    expect(nightKey(localMs(2026, 8, 3, 13))).toBe("2026-08-03");
  });

  it("has no answer for an undated session", () => {
    expect(nightKey(0)).toBe("");
  });
});

describe("integrationMs", () => {
  it("counts only the lights", () => {
    const ms = integrationMs([
      stat({ total_exposure_us: 600_000_000 }),
      stat({ frame_type: "dark", total_exposure_us: 300_000_000 }),
      stat({ frame_type: "flat", total_exposure_us: 5_000_000 }),
    ]);
    expect(ms).toBe(600_000);
  });

  it("accepts either spelling of the frame type", () => {
    expect(integrationMs([stat({ frame_type: "LIGHT" })])).toBe(600_000);
  });
});

describe("perFilterCounts", () => {
  it("prefers the frame rows and merges duplicate buckets", () => {
    const got = perFilterCounts([
      stat({ filter: "L", frames: 30 }),
      stat({ filter: "R", frames: 10 }),
      stat({ filter: "L", frames: 12, min_gain: 200, max_gain: 200 }),
      stat({ filter: "L", frame_type: "dark", frames: 20 }),
    ]);
    expect(got).toEqual([
      ["L", 42],
      ["R", 10],
    ]);
  });

  it("falls back to the sequencer counters when no frames were aggregated", () => {
    expect(perFilterCounts([], { L: 12, R: 0, Ha: 4 })).toEqual([
      ["L", 12],
      ["Ha", 4],
    ]);
  });

  it("has nothing to report for a session with neither", () => {
    expect(perFilterCounts([], undefined)).toEqual([]);
  });
});

describe("skyScore", () => {
  it("is the median hourly verdict", () => {
    expect(
      skyScore(summary({ verdict: { min: 20, median: 68, max: 90, n: 6 } })),
    ).toBe(68);
  });

  // A session with no weather record must read as "not recorded", never as a score of zero — the
  // two mean opposite things to someone choosing which nights to stack.
  it("is null when nothing was recorded", () => {
    expect(skyScore(null)).toBeNull();
    expect(skyScore(summary())).toBeNull();
  });
});

describe("moonPenalty", () => {
  it("is worst for a full Moon right next to the target", () => {
    const got = moonPenalty(
      summary({
        target_valid: true,
        moon_up: true,
        moon_illum_max: 1,
        moon_sep_min_deg: 0,
      }),
    );
    expect(got).toBe(1);
  });

  it("is mild for a full Moon on the far side of the sky", () => {
    const got = moonPenalty(
      summary({
        target_valid: true,
        moon_up: true,
        moon_illum_max: 1,
        moon_sep_min_deg: 180,
      }),
    );
    expect(got).toBe(0);
  });

  it("is nothing when the Moon never rose", () => {
    expect(
      moonPenalty(
        summary({ target_valid: true, moon_up: false, moon_illum_max: 1 }),
      ),
    ).toBeNull();
  });

  it("cannot be computed without a target to measure against", () => {
    expect(
      moonPenalty(summary({ moon_up: true, moon_illum_max: 1 })),
    ).toBeNull();
  });
});

describe("hasConditions", () => {
  it("rejects the empty object a pre-logbook session carries", () => {
    expect(hasConditions({})).toBe(false);
    expect(hasConditions(undefined)).toBe(false);
  });

  it("rejects a summary that was created but never sampled", () => {
    expect(hasConditions(summary({ samples: 0 }))).toBe(false);
  });

  it("accepts a real record", () => {
    expect(hasConditions(summary({ samples: 3 }))).toBe(true);
  });
});
