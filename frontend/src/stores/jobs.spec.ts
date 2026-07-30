import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const apiPost = vi.fn(async (..._args: unknown[]) => ({ id: 42 }));
vi.mock("@/services/api", () => ({
  ApiError: class extends Error {},
  apiGet: vi.fn(async () => ({})),
  apiPost: (...args: unknown[]) => apiPost(...args),
  health: vi.fn(async () => true),
  withS3: (u: string) => u,
}));

import { useJobsStore } from "./jobs";

describe("jobs store create body", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    apiPost.mockClear();
  });

  it("sends the target hint when provided", async () => {
    const store = useJobsStore();
    await store.create("input/triplet_m66/CapObj", "deepsky", "image", {
      target: "M66",
    });
    expect(apiPost).toHaveBeenCalledWith(
      "/api/jobs",
      expect.objectContaining({ target: "M66" }),
    );
  });

  it("omits target entirely when unset", async () => {
    const store = useJobsStore();
    await store.create("input/triplet_m66/CapObj", "deepsky", "image", {});
    const body = apiPost.mock.calls[0][1] as Record<string, unknown>;
    expect(body).not.toHaveProperty("target");
  });
});
