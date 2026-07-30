import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

vi.mock("@/services/api", () => ({
  ApiError: class extends Error {},
  apiGet: vi.fn(async () => ({})),
  apiPost: vi.fn(async () => ({})),
  apiPut: vi.fn(async () => ({})),
  apiDelete: vi.fn(async () => ({})),
  previewUrl: (p: string) => p,
  withS3: (u: string) => u,
}));

import { useBrowseStore } from "./browse";
import type { ProcessedGroup, SavedSelectionInfo } from "@/types";

function group(
  jobId: number,
  paths: string[],
  extra: Partial<ProcessedGroup> = {},
): ProcessedGroup {
  return {
    job_id: jobId,
    kind: "deepsky",
    status: "succeeded",
    created_at_ms: jobId * 1000,
    signature: paths
      .map((p) => p.toLowerCase())
      .sort()
      .join("|"),
    paths: paths.map((p) => ({ path: p, exists: true, local: true, rel: p })),
    ...extra,
  };
}

function selection(
  id: number,
  name: string,
  signature: string,
  favorite = false,
): SavedSelectionInfo {
  return {
    id,
    name,
    favorite,
    signature,
    mode: "deepsky",
    format: "image",
    updated_at_ms: 500,
    paths: [{ path: "/data/old", exists: false, local: false, rel: "old" }],
  };
}

describe("browse store processingHistory", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("collapses duplicate folder-sets and counts runs", () => {
    const store = useBrowseStore();
    store.processedGroups = [
      group(2, ["/data/M101"]),
      group(1, ["/data/M101"]),
    ];
    expect(store.processingHistory).toHaveLength(1);
    expect(store.processingHistory[0].runs).toBe(2);
    expect(store.processingHistory[0].jobId).toBe(2); // newest first supplies the entry
  });

  it("joins saved-selection name/favorite by signature", () => {
    const store = useBrowseStore();
    const g = group(1, ["/data/M101"]);
    store.processedGroups = [g];
    store.savedSelections = [selection(7, "My M101", g.signature!)];
    expect(store.processingHistory[0].selection).toEqual({
      id: 7,
      name: "My M101",
      favorite: false,
    });
  });

  it("pins favorites first, newest-first within each band", () => {
    const store = useBrowseStore();
    const a = group(3, ["/data/a"]);
    const b = group(2, ["/data/b"]);
    store.processedGroups = [a, b];
    store.savedSelections = [selection(1, "B fav", b.signature!, true)];
    const rows = store.processingHistory;
    expect(rows[0].signature).toBe(b.signature);
    expect(rows[1].signature).toBe(a.signature);
  });

  it("appends an orphan row for a saved selection with no matching job", () => {
    const store = useBrowseStore();
    store.processedGroups = [group(1, ["/data/live"])];
    store.savedSelections = [selection(9, "Archived", "/data/old", true)];
    const rows = store.processingHistory;
    expect(rows).toHaveLength(2);
    const orphan = rows.find((r) => r.selection?.id === 9)!;
    expect(orphan.jobId).toBe(0);
    expect(orphan.runs).toBe(0);
    expect(orphan.mode).toBe("deepsky"); // snapshot pre-fills the launch form
    expect(orphan.paths[0].path).toBe("/data/old");
    expect(rows[0]).toBe(orphan); // favorite → pinned on top
  });

  it("falls back to a local signature when the backend omits it", () => {
    const store = useBrowseStore();
    const g = group(1, ["/data/B", "/data/a"]);
    delete g.signature;
    store.processedGroups = [g];
    expect(store.processingHistory[0].signature).toBe("/data/a|/data/b");
  });
});
