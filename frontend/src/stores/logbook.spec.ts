import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const apiGet = vi.fn(async (_url: string): Promise<unknown> => ({}));
const apiPost = vi.fn(
  async (_url: string, _body?: unknown): Promise<unknown> => ({}),
);
vi.mock("@/services/api", () => ({
  ApiError: class extends Error {},
  apiGet: (url: string) => apiGet(url),
  apiPost: (url: string, body?: unknown) => apiPost(url, body),
  withS3: (u: string) => u,
}));

import { canResumeSession, useLogbookStore } from "./logbook";

describe("logbook store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    apiGet.mockReset();
    apiGet.mockResolvedValue({ sessions: [], total: 0 });
  });

  it("asks for the default page on first load", async () => {
    await useLogbookStore().listSessions();
    expect(apiGet).toHaveBeenCalledWith("/api/capture/sessions?limit=50");
  });

  it("does not refetch a list it already has", async () => {
    const store = useLogbookStore();
    await store.listSessions();
    await store.listSessions();
    expect(apiGet).toHaveBeenCalledTimes(1);
  });

  it("refetches when forced", async () => {
    const store = useLogbookStore();
    await store.listSessions();
    await store.listSessions(true);
    expect(apiGet).toHaveBeenCalledTimes(2);
  });

  it("dedupes concurrent list calls into one request", async () => {
    const store = useLogbookStore();
    await Promise.all([
      store.listSessions(),
      store.listSessions(),
      store.listSessions(),
    ]);
    expect(apiGet).toHaveBeenCalledTimes(1);
  });

  it("puts the filters on the query string", async () => {
    const store = useLogbookStore();
    await store.setQuery({ object: "M31", fromMs: 1000, toMs: 2000 });
    expect(apiGet).toHaveBeenCalledWith(
      "/api/capture/sessions?limit=50&object=M31&from=1000&to=2000",
    );
  });

  // A search issued from page 3 must not return an empty list that reads as "no matches".
  it("resets the page when the filters change", async () => {
    const store = useLogbookStore();
    await store.setQuery({ offset: 50 });
    apiGet.mockClear();
    await store.setQuery({ object: "M31" });
    expect(apiGet).toHaveBeenCalledWith(
      "/api/capture/sessions?limit=50&object=M31",
    );
  });

  it("keeps an explicit page change", async () => {
    const store = useLogbookStore();
    await store.setQuery({ offset: 50 });
    expect(apiGet).toHaveBeenCalledWith(
      "/api/capture/sessions?limit=50&offset=50",
    );
  });

  it("records the unpaged total so the UI can say 20 of 137", async () => {
    apiGet.mockResolvedValue({ sessions: [{ id: 1 }], total: 137 });
    const store = useLogbookStore();
    await store.listSessions();
    expect(store.total).toBe(137);
    expect(store.hasMore).toBe(true);
  });

  it("surfaces a failure instead of leaving a blank page", async () => {
    apiGet.mockRejectedValue(new Error("engine is down"));
    const store = useLogbookStore();
    await store.listSessions();
    expect(store.error).toBe("engine is down");
    expect(store.sessions).toEqual([]);
  });

  it("fetches a session's frames and tallies", async () => {
    apiGet.mockResolvedValue({
      session: { id: 7 },
      frames: [{ id: 1 }],
      frame_stats: [{ filter: "L" }],
    });
    const store = useLogbookStore();
    await store.loadSession(7);
    expect(apiGet).toHaveBeenCalledWith("/api/capture/sessions/7");
    expect(store.detail?.frameStats).toHaveLength(1);
  });

  it("fetches the conditions from their own endpoint", async () => {
    apiGet.mockResolvedValue({
      conditions: [{ at_ms: 1 }],
      forecasts: [{ kind: "start" }],
      summary: { samples: 2 },
    });
    const store = useLogbookStore();
    await store.loadConditions(7);
    expect(apiGet).toHaveBeenCalledWith("/api/capture/sessions/7/conditions");
    expect(store.conditions?.summary?.samples).toBe(2);
  });

  // The API sends {} rather than null for a session that was never sampled; the store must not
  // hand that to a component expecting a real summary.
  it("treats an empty summary object as no summary at all", async () => {
    apiGet.mockResolvedValue({
      conditions: [],
      forecasts: [],
      summary: {},
      message: "no conditions were recorded",
    });
    const store = useLogbookStore();
    await store.loadConditions(7);
    expect(store.conditions?.summary).toBeNull();
    expect(store.conditions?.message).toBe("no conditions were recorded");
  });

  it("clears the detail so one night's frames never show under another's title", async () => {
    apiGet.mockResolvedValue({
      session: { id: 7 },
      frames: [],
      frame_stats: [],
    });
    const store = useLogbookStore();
    await store.loadSession(7);
    store.clearDetail();
    expect(store.detail).toBeNull();
    expect(store.conditions).toBeNull();
  });

  it("resumes a night and reports what is left to shoot", async () => {
    apiPost.mockResolvedValue({
      progress: { session_id: 77 },
      remaining_frames: 23,
      warning: "optics came from the current configuration",
    });

    const got = await useLogbookStore().resumeSession(12);

    expect(apiPost).toHaveBeenCalledWith("/api/capture/sessions/12/resume", {});
    expect(got).toEqual({
      sessionId: 77,
      remaining: 23,
      warning: "optics came from the current configuration",
    });
  });
});

// Where the button is offered. Getting this wrong is user-visible in both directions: a missing
// button strands a night that could be finished, and one on a completed night only ever errors.
describe("canResumeSession", () => {
  const row = (status: string, done: number, total: number) =>
    ({ status, frames_done: done, total_frames: total }) as never;

  it.each([
    ["a night stopped early", "aborted", 40, 60, true],
    ["one that failed", "failed", 5, 80, true],
    ["one orphaned by a restart", "interrupted", 0, 20, true],
    ["a paused session the engine no longer holds", "paused", 3, 20, true],
    ["a completed night", "completed", 60, 60, false],
    ["a night still running", "running", 10, 60, false],
    ["one that took every frame despite stopping", "aborted", 60, 60, false],
  ])("%s", (_name, status, done, total, want) => {
    expect(canResumeSession(row(status, done, total))).toBe(want);
  });

  it("has nothing to offer without a session", () => {
    expect(canResumeSession(null)).toBe(false);
  });
});
