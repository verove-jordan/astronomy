import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useJobStream } from "./useJobStream";

// Minimal EventSource stub: captures the created instance so tests can push messages through
// `onmessage` exactly as the browser would.
class FakeEventSource {
  static last: FakeEventSource | null = null;
  url: string;
  onmessage: ((ev: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(url: string) {
    this.url = url;
    FakeEventSource.last = this;
  }
  close() {
    this.closed = true;
  }
  emit(event: object) {
    this.onmessage?.({ data: JSON.stringify(event) } as MessageEvent<string>);
  }
}

describe("useJobStream", () => {
  beforeEach(() => {
    FakeEventSource.last = null;
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  const base = { job_id: 1, status: "running", progress: 10, step: "stacking" };

  it("upserts stage previews by (stage, filter, session) — parallel sessions never overwrite", () => {
    const s = useJobStream(1);
    const es = FakeEventSource.last!;

    // Two nights emit the SAME stage+filter (and even a colliding ordinal): both cards must survive.
    es.emit({
      ...base,
      stage_preview: {
        index: 1402,
        stage: "prenorm",
        filter: "L",
        session: "2023-02-27",
        png_path: "a.png",
      },
    });
    es.emit({
      ...base,
      stage_preview: {
        index: 1402,
        stage: "prenorm",
        filter: "L",
        session: "2023-03-15",
        png_path: "b.png",
      },
    });
    expect(s.stagePreviews.value).toHaveLength(2);

    // A re-emit of the SAME identity replaces its card in place.
    es.emit({
      ...base,
      stage_preview: {
        index: 1402,
        stage: "prenorm",
        filter: "L",
        session: "2023-02-27",
        png_path: "a2.png",
      },
    });
    expect(s.stagePreviews.value).toHaveLength(2);
    expect(
      s.stagePreviews.value.find((p) => p.session === "2023-02-27")!.png_path,
    ).toBe("a2.png");
  });

  it("merges the reconnect snapshot without duplicating live cards", () => {
    const s = useJobStream(1);
    const es = FakeEventSource.last!;
    es.emit({
      ...base,
      stage_preview: { index: 300, stage: "combined", png_path: "c.png" },
    });
    es.emit({
      ...base,
      stage_previews: [
        { index: 300, stage: "combined", png_path: "c.png" },
        { index: 400, stage: "colorcal", png_path: "d.png" },
      ],
    });
    expect(s.stagePreviews.value).toHaveLength(2);
  });

  it("accumulates photom records by (label, session) and tracks the current session", () => {
    const s = useJobStream(1);
    const es = FakeEventSource.last!;

    es.emit({
      ...base,
      session: "2023-02-27",
      line: "▶ 2023-02-27 · L — calibrate (12 frames)",
    });
    expect(s.currentSession.value).toBe("2023-02-27");

    es.emit({
      ...base,
      session: "2023-03-15",
      photom: {
        label: "L g250",
        session: "2023-03-15",
        scale: 5.62,
        offset: 0,
        resid: 0.01,
        frames: 12,
        applied: true,
      },
    });
    es.emit({
      ...base,
      session: "2023-02-27",
      photom: {
        label: "L g400",
        session: "2023-02-27",
        scale: 1,
        offset: 0,
        resid: 0,
        frames: 30,
        ref: true,
        applied: false,
      },
    });
    expect(s.photomRecords.value).toHaveLength(2);

    // A re-measure of the same group updates in place.
    es.emit({
      ...base,
      session: "2023-03-15",
      photom: {
        label: "L g250",
        session: "2023-03-15",
        scale: 5.7,
        offset: 0,
        resid: 0.01,
        frames: 12,
        applied: true,
      },
    });
    expect(s.photomRecords.value).toHaveLength(2);
    expect(s.photomRecords.value.find((r) => r.label === "L g250")!.scale).toBe(
      5.7,
    );
  });

  it("keeps plain progress/log behavior intact", () => {
    const s = useJobStream(1);
    const es = FakeEventSource.last!;
    es.emit({ ...base, line: "hello", ts: 123 });
    expect(s.progress.value).toBe(10);
    expect(s.step.value).toBe("stacking");
    expect(s.lines.value.at(-1)!.text).toBe("hello");
    es.emit({ ...base, done: true });
    expect(s.done.value).toBe(true);
    expect(es.closed).toBe(true);
  });
});
