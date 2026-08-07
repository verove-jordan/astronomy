import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import SessionConditions from "./SessionConditions.vue";
import { testI18n } from "@/test/i18n";
import type { ConditionsSummary } from "@/types";

const empty = { min: 0, median: 0, max: 0, n: 0 };

function summary(over: Partial<ConditionsSummary> = {}): ConditionsSummary {
  return {
    samples: 6,
    first_ms: 0,
    last_ms: 0,
    cloud_pct: { min: 5, median: 18, max: 60, n: 6 },
    cloud_low: empty,
    cloud_mid: empty,
    cloud_high: empty,
    seeing_arcsec: { min: 1.8, median: 2.4, max: 3.6, n: 6 },
    transparency: { min: 0.6, median: 0.8, max: 0.9, n: 6 },
    humidity_pct: { min: 50, median: 62, max: 80, n: 6 },
    dew_spread_c: { min: 2, median: 4, max: 7, n: 6 },
    temp_c: { min: 4, median: 8, max: 12, n: 6 },
    wind_kmh: { min: 2, median: 6, max: 14, n: 6 },
    gust_kmh: empty,
    precip_pct: empty,
    aod: empty,
    verdict: { min: 40, median: 72, max: 90, n: 6 },
    moon_illum_max: 0.34,
    moon_alt_max_deg: 21,
    moon_up: true,
    moon_sep_min_deg: 47,
    moon_phase_angle_deg: 90,
    target_valid: true,
    target_alt_min_deg: 34,
    target_alt_max_deg: 71,
    target_airmass_min: 1.06,
    sqm: 20.4,
    bortle: 5,
    dew_risk_worst: "moderate",
    kp_max: 3,
    aurora_max: "unlikely",
    source_counts: { live: 4, cached: 2 },
    ...over,
  };
}

function render(s: ConditionsSummary) {
  return mount(SessionConditions, {
    global: { plugins: [testI18n()] },
    props: { summary: s },
  });
}

describe("SessionConditions", () => {
  it("leads with the sky verdict and the sample count", () => {
    const text = render(summary()).text();
    expect(text).toContain("72");
    expect(text).toContain("6 hourly samples");
  });

  it("shows the median with its range for each weather metric", () => {
    const text = render(summary()).text();
    expect(text).toContain("18 %"); // cloud median
    expect(text).toContain("5 % … 60 %"); // cloud range
    expect(text).toContain("2.4″"); // seeing
  });

  // Zero is a real reading for temperature and cloud, but "the feed did not supply it" for seeing —
  // and the panel must say which, not print a confident 0.
  it("says a metric was not supplied instead of printing a zero", () => {
    const text = render(summary({ seeing_arcsec: empty })).text();
    expect(text).toContain("not supplied");
  });

  it("names the moon phase using the Tonight page's own wording", () => {
    expect(render(summary({ moon_phase_angle_deg: 180 })).text()).toContain(
      "Full moon",
    );
    expect(render(summary({ moon_phase_angle_deg: 0 })).text()).toContain(
      "New moon",
    );
  });

  it("reports the moon's closest approach, not its average", () => {
    expect(render(summary({ moon_sep_min_deg: 12 })).text()).toContain("12°");
  });

  it("says the moon stayed down rather than reporting a negative altitude", () => {
    const text = render(
      summary({ moon_up: false, moon_alt_max_deg: -30 }),
    ).text();
    expect(text).toContain("below horizon");
    expect(text).not.toContain("-30");
  });

  it("hides the target section for a session that carried no coordinates", () => {
    const text = render(summary({ target_valid: false })).text();
    expect(text).not.toContain("Airmass");
  });

  // Airmass 0 means "never cleared the horizon"; showing 1.00 would claim a perfect overhead pass.
  it("does not invent an airmass for a target that never rose", () => {
    const text = render(summary({ target_airmass_min: 0 })).text();
    expect(text).toContain("Airmass");
    expect(text).not.toContain("1.00");
  });

  it("flags the hours whose feeds were down", () => {
    const text = render(
      summary({ source_counts: { live: 3, unavailable: 3 } }),
    ).text();
    expect(text).toContain("3 with no weather data");
  });

  it("omits the geomagnetic cell when nothing was reported", () => {
    const text = render(summary({ kp_max: 0 })).text();
    expect(text).not.toContain("Kp");
  });
});
