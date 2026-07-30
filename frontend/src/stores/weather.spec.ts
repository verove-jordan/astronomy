import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const apiGet = vi.fn(async (..._args: unknown[]) => ({}) as unknown);
vi.mock("@/services/api", () => ({
  BASE: "",
  apiGet: (...args: unknown[]) => apiGet(...args),
}));

import { useWeatherStore } from "./weather";

function framesPayload(timesteps: number[], warning = "") {
  return {
    bbox: [0, 40, 10, 50],
    timesteps,
    issued_ms: 1,
    warning,
  };
}

describe("weather store frames", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    apiGet.mockClear();
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-17T21:00:00Z"));
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const t0 = new Date("2026-07-17T21:00:00Z").getTime();

  it("sends the map zoom to the frames endpoint", async () => {
    apiGet.mockResolvedValueOnce(framesPayload([t0, t0 + 3600_000]));
    const wx = useWeatherStore();
    await wx.fetchFrames(48.86, 2.35, 7);
    expect(apiGet).toHaveBeenCalledTimes(1);
    const url = apiGet.mock.calls[0][0] as string;
    expect(url).toContain("/api/sky/weather/grid/frames");
    expect(url).toContain("z=7");
    expect(url).not.toContain("radius=");
  });

  it("builds tile URLs only once a time axis is loaded", async () => {
    const wx = useWeatherStore();
    expect(wx.weatherTileUrl("clouds")).toBe("");
    apiGet.mockResolvedValueOnce(framesPayload([t0]));
    await wx.fetchFrames(48.86, 2.35, 7);
    expect(wx.weatherTileUrl("clouds")).toBe(
      `/api/sky/weather/tiles/clouds/${t0}/{z}/{x}/{y}`,
    );
  });

  it("keeps the last good axis and surfaces a warning on a degraded fetch", async () => {
    const wx = useWeatherStore();
    apiGet.mockResolvedValueOnce(framesPayload([t0, t0 + 3600_000]));
    await wx.fetchFrames(48.86, 2.35, 7);
    expect(wx.timesteps).toHaveLength(2);

    // Soft-failed upstream: shaped-but-empty payload must not blank the scrubber.
    apiGet.mockResolvedValueOnce(
      framesPayload([], "cloud map currently unavailable"),
    );
    await wx.fetchFrames(48.86, 2.35, 7, true);
    expect(wx.timesteps).toHaveLength(2);
    expect(wx.warning).not.toBe("");
  });

  it("suppresses automatic refetches during the failure cooldown; force bypasses", async () => {
    const wx = useWeatherStore();
    apiGet.mockResolvedValueOnce(framesPayload([t0]));
    await wx.fetchFrames(48.86, 2.35, 7);
    apiGet.mockResolvedValue(framesPayload([]));
    await wx.fetchFrames(48.86, 2.35, 7, true); // degraded → cooldown armed
    const callsAfterDegrade = apiGet.mock.calls.length;

    await wx.fetchFrames(40.0, 3.0, 7); // automatic, inside the cooldown → skipped
    expect(apiGet.mock.calls.length).toBe(callsAfterDegrade);

    await wx.fetchFrames(40.0, 3.0, 7, true); // explicit force → tries anyway
    expect(apiGet.mock.calls.length).toBe(callsAfterDegrade + 1);

    vi.setSystemTime(new Date(t0 + 61_000)); // cooldown elapsed → automatic refetch allowed again
    await wx.fetchFrames(41.0, 4.0, 7);
    expect(apiGet.mock.calls.length).toBe(callsAfterDegrade + 2);
  });
});
