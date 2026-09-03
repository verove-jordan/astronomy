import { describe, expect, it } from "vitest";

import { MAX_RATE, MIN_RATE, RATES } from "@/composables/useSimClock";

// The clock's own behaviour needs a mounted component (it runs on requestAnimationFrame and tears
// down in onBeforeUnmount), but the rate table is a plain constant — and it is the part that was
// silently wrong, so it is the part worth pinning.

const SECOND = 1000;

describe("RATES", () => {
  // The unit is simulated milliseconds per REAL millisecond, so a named speed of "one day per
  // second" must advance exactly one day when multiplied by one second of real time. Getting this
  // wrong by the 1000 between milliseconds and seconds is invisible in the code and glaring on
  // screen: the label says a year and the planets cross a millennium.
  const spans: Record<string, number> = {
    realtime: SECOND,
    minute: 60 * SECOND,
    hour: 3600 * SECOND,
    day: 86_400 * SECOND,
    week: 7 * 86_400 * SECOND,
    month: 30 * 86_400 * SECOND,
    year: 365.25 * 86_400 * SECOND,
  };

  for (const { key, rate } of RATES) {
    it(`advances one ${key} per second of real time`, () => {
      expect(rate * SECOND).toBeCloseTo(spans[key], -2);
    });
  }

  it("is ordered, and spans real time to a century a second", () => {
    for (let i = 1; i < RATES.length; i++) {
      expect(RATES[i].rate).toBeGreaterThan(RATES[i - 1].rate);
    }
    expect(RATES[0].rate).toBe(MIN_RATE);
    // The ceiling is a century per second, which is a hundred times the fastest named rate.
    const year = RATES.find((r) => r.key === "year")!.rate;
    expect(MAX_RATE).toBeCloseTo(year * 100, -4);
    expect(MAX_RATE).toBeGreaterThan(year);
  });

  it("crosses the whole 1800–2050 model in a sane number of seconds at full speed", () => {
    const span = Date.UTC(2050, 11, 31) - Date.UTC(1800, 0, 1);
    const seconds = span / (MAX_RATE * SECOND);
    // Fast enough to be worth having, slow enough that the far end is reachable by aiming rather
    // than by luck.
    expect(seconds).toBeGreaterThan(1);
    expect(seconds).toBeLessThan(10);
  });
});
