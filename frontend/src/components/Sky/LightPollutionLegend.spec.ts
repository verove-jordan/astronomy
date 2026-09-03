import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import LightPollutionLegend from "./LightPollutionLegend.vue";
import { testI18n } from "@/test/i18n";

function mountIt(bortle?: number | null) {
  return mount(LightPollutionLegend, {
    props: { bortle },
    global: { plugins: [testI18n()] },
  });
}

describe("LightPollutionLegend", () => {
  it("marks the selected site's class on the ramp", () => {
    const w = mountIt(4);
    const marker = w.find('[data-testid="bortle-marker"]');
    expect(marker.exists()).toBe(true);
    // class 1 sits at 0%, class 9 at 100% — class 4 is three eighths along.
    expect(marker.attributes("style")).toContain("left: 37.5%");
    expect(w.find('[data-testid="bortle-value"]').text()).toContain("4");
  });

  it("puts class 1 at the dark end and class 9 at the bright end", () => {
    expect(
      mountIt(1).find('[data-testid="bortle-marker"]').attributes("style"),
    ).toContain("left: 0%");
    expect(
      mountIt(9).find('[data-testid="bortle-marker"]').attributes("style"),
    ).toContain("left: 100%");
  });

  it("shows no marker without a reading", () => {
    expect(mountIt().find('[data-testid="bortle-marker"]').exists()).toBe(
      false,
    );
    expect(mountIt(null).find('[data-testid="bortle-marker"]').exists()).toBe(
      false,
    );
  });

  it("treats 0 as unknown rather than as a pristine sky", () => {
    // The API returns 0 when it has no data. Rendering that as a class would plant the marker at the
    // dark end of the ramp and claim a Bortle 1 site — the most flattering possible lie.
    const w = mountIt(0);
    expect(w.find('[data-testid="bortle-marker"]').exists()).toBe(false);
    expect(w.find('[data-testid="bortle-value"]').exists()).toBe(false);
  });

  it("places a fractional class between the class stops", () => {
    // The whole point of carrying the decimal: 4.5 must not land on the class-4 stop (37.5%) — the ramp
    // is continuous, so a reading half a class brighter has to look half a class brighter.
    const w = mountIt(4.5);
    expect(
      w.find('[data-testid="bortle-marker"]').attributes("style"),
    ).toContain("left: 43.75%");
    expect(w.find('[data-testid="bortle-value"]').text()).toContain("4.5");
  });

  it("ignores an out-of-range class", () => {
    expect(mountIt(12).find('[data-testid="bortle-marker"]').exists()).toBe(
      false,
    );
  });
});
