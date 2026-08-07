import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

vi.mock("@/services/api", () => ({ apiGet: vi.fn() }));

import { apiGet } from "@/services/api";
import { useDarkSkyStore } from "./darksky";
import type { DarkSite, DarkSitesResult, SkyNightsResult } from "@/types";

const mockGet = vi.mocked(apiGet);

function site(over: Partial<DarkSite> = {}): DarkSite {
  return {
    lat: 44,
    lon: 5,
    sqm: 21.5,
    bortle: 2,
    distance_km: 40,
    score: 0.8,
    sub: { darkness: 0.9, openness: 0.8, weather: 0.7, weather_known: true },
    ...over,
  };
}

function result(over: Partial<DarkSitesResult> = {}): DarkSitesResult {
  return {
    count: 1,
    cells_scanned: 100,
    weather_weight: 0.3,
    candidates: [site()],
    warnings: [],
    ...over,
  };
}

function baseQuery() {
  return {
    minLat: 44,
    minLon: 5,
    maxLat: 45,
    maxLon: 6,
    maxBortle: 4,
    horizon: true,
  };
}

function lastUrl(): string {
  return String(mockGet.mock.calls.at(-1)?.[0] ?? "");
}

describe("darksky store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    mockGet.mockReset();
  });

  it("sends the night, weather and weight the finder asked for", async () => {
    mockGet.mockResolvedValue(result());
    const store = useDarkSkyStore();

    await store.find({ ...baseQuery(), night: 3, weatherWeight: 0.55 });

    const url = lastUrl();
    expect(url).toContain("night=3");
    expect(url).toContain("weather_weight=0.55");
    expect(url).not.toContain("weather=0");
  });

  // Weather is the server's default, so the parameter only ever appears to switch it OFF. Sending
  // weather=1 as well would make the default impossible to change server-side later.
  it("only names the weather parameter when disabling it", async () => {
    mockGet.mockResolvedValue(result());
    const store = useDarkSkyStore();

    await store.find({ ...baseQuery(), weather: true });
    expect(lastUrl()).not.toContain("weather=");

    await store.find({ ...baseQuery(), weather: false });
    expect(lastUrl()).toContain("weather=0");
  });

  it("keeps the night and weight the server actually applied", async () => {
    mockGet.mockResolvedValue(
      result({
        weather_weight: 0.3,
        night: {
          index: 2,
          start_ms: 1,
          end_ms: 2,
          kind: "astronomical",
          dark_hours: 6.5,
          moon_illum: 0.4,
          moon_up_hours: 2,
        },
      }),
    );
    const store = useDarkSkyStore();

    await store.find(baseQuery());

    expect(store.night?.index).toBe(2);
    expect(store.weatherWeight).toBe(0.3);
    expect(store.weatherAvailable).toBe(false); // the stub candidate carries no forecast hours
  });

  // A ranking with no forecast behind it must be reported as such, so the UI can say "terrain only"
  // instead of implying the weather was consulted and found fine.
  it("reports weather as available only when a candidate carries forecast hours", async () => {
    const withHours = site({
      weather: {
        start_ms: 1,
        end_ms: 2,
        sample_hours: 8,
        score: 80,
        cloud_pct: 10,
        cloud_low_pct: 5,
        cloud_high_pct: 5,
        clear_hours: 7,
        seeing_arcsec: 1.4,
        transparency: 0.8,
        dew_risk: "low",
        min_temp_c: 8,
        wind_kmh: 5,
        elevation_m: 900,
      },
    });
    mockGet.mockResolvedValue(result({ candidates: [withHours] }));
    const store = useDarkSkyStore();

    await store.find(baseQuery());

    expect(store.weatherAvailable).toBe(true);
  });

  it("surfaces a failed search without wiping the night list", async () => {
    mockGet.mockRejectedValue(new Error("upstream down"));
    const store = useDarkSkyStore();

    await store.find(baseQuery());

    expect(store.error).toBe("upstream down");
    expect(store.candidates).toEqual([]);
    expect(store.searched).toBe(true);
    expect(store.loading).toBe(false);
  });

  it("caches the night list per location", async () => {
    const nights: SkyNightsResult = {
      timezone: "Europe/Paris",
      nights: [
        {
          index: 0,
          start_ms: 1,
          end_ms: 2,
          start_local: "22:00",
          end_local: "05:00",
          date_local: "2026-08-04",
          kind: "astronomical",
          dark_hours: 7,
          moon_illum: 0.2,
          moon_up_hours: 1,
          moon_phase: "waxing_crescent",
          low_confidence: false,
        },
      ],
    };
    mockGet.mockResolvedValue(nights);
    const store = useDarkSkyStore();

    await store.loadNights(44, 5);
    await store.loadNights(44, 5);
    expect(mockGet).toHaveBeenCalledTimes(1);
    expect(store.nights).toHaveLength(1);

    await store.loadNights(48, 2); // a different observer is a different set of nights
    expect(mockGet).toHaveBeenCalledTimes(2);
  });

  // The picker must survive a missing endpoint: it falls back to plain day offsets.
  it("leaves the night list empty when it cannot be loaded", async () => {
    mockGet.mockRejectedValue(new Error("404"));
    const store = useDarkSkyStore();

    await store.loadNights(44, 5);

    expect(store.nights).toEqual([]);
    expect(store.error).toBe("");
  });
});
