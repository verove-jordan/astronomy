import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { apiGet, apiPost } from "@/services/api";
import type {
  CaptureConditionRow,
  CaptureForecastRow,
  CaptureFrameStat,
  CaptureFrame,
  CaptureSessionRow,
  ConditionsSummary,
} from "@/types";

// The observing logbook: every capture session, past and present, with the sky it ran under.
//
// The conditions are fetched separately from the session because they are the expensive half — the
// archived forecast snapshots are tens of kilobytes each — and only the detail view wants them.

export interface LogbookQuery {
  limit: number;
  offset: number;
  object: string;
  fromMs: number;
  toMs: number;
}

// canResumeSession reports whether a night still owes frames.
//
// "Completed" is the one status that never does, and a run the engine is still holding cannot be
// resumed because it has not stopped. Everything else — aborted, failed, interrupted, and a session
// orphaned by an engine restart — may still be short. This is only a filter on where the button is
// OFFERED: the engine recomputes what is owed from the frames actually recorded and refuses an empty
// resume, so a stale row here costs a clear error rather than a wasted night.
export function canResumeSession(
  s: Pick<CaptureSessionRow, "status" | "frames_done" | "total_frames"> | null,
): boolean {
  if (!s) return false;
  if (s.status === "completed" || s.status === "running") return false;
  return s.frames_done < s.total_frames;
}

export interface SessionDetail {
  session: CaptureSessionRow;
  frames: CaptureFrame[];
  frameStats: CaptureFrameStat[];
}

export interface SessionConditions {
  conditions: CaptureConditionRow[];
  forecasts: CaptureForecastRow[];
  summary: ConditionsSummary | null;
  message: string;
}

const DEFAULT_LIMIT = 50;

function queryString(q: LogbookQuery): string {
  const p = new URLSearchParams();
  p.set("limit", String(q.limit));
  if (q.offset) p.set("offset", String(q.offset));
  if (q.object.trim()) p.set("object", q.object.trim());
  if (q.fromMs) p.set("from", String(q.fromMs));
  if (q.toMs) p.set("to", String(q.toMs));
  return p.toString();
}

// hasSummary rejects the empty object the API sends for a session that was never sampled, so callers
// can test one thing instead of probing for fields.
function hasSummary(s: unknown): s is ConditionsSummary {
  return !!s && typeof s === "object" && "samples" in s;
}

export const useLogbookStore = defineStore("logbook", () => {
  const sessions = ref<CaptureSessionRow[]>([]);
  const total = ref(0);
  const loading = ref(false);
  const error = ref("");
  const loaded = ref(false);

  const query = ref<LogbookQuery>({
    limit: DEFAULT_LIMIT,
    offset: 0,
    object: "",
    fromMs: 0,
    toMs: 0,
  });

  const detail = ref<SessionDetail | null>(null);
  const conditions = ref<SessionConditions | null>(null);
  const detailLoading = ref(false);
  const conditionsLoading = ref(false);

  // One in-flight request per distinct call, so a page that mounts a list and a detail at once (or a
  // user hammering refresh) issues one fetch each rather than a burst.
  let listInflight: Promise<void> | null = null;
  const detailInflight = new Map<number, Promise<void>>();
  const conditionsInflight = new Map<number, Promise<void>>();

  const hasMore = computed(
    () =>
      sessions.value.length > 0 &&
      query.value.offset + sessions.value.length < total.value,
  );

  async function listSessions(force = false): Promise<void> {
    if (loaded.value && !force) return;
    if (listInflight) return listInflight;
    loading.value = true;
    listInflight = (async () => {
      try {
        const res = await apiGet<{
          sessions: CaptureSessionRow[];
          total: number;
        }>(`/api/capture/sessions?${queryString(query.value)}`);
        sessions.value = res.sessions ?? [];
        total.value = res.total ?? sessions.value.length;
        loaded.value = true;
        error.value = "";
      } catch (e) {
        error.value = e instanceof Error ? e.message : String(e);
      } finally {
        loading.value = false;
        listInflight = null;
      }
    })();
    return listInflight;
  }

  // search/paging change what "loaded" means, so they always refetch.
  function setQuery(patch: Partial<LogbookQuery>): Promise<void> {
    // Any change to the filters invalidates the current page offset — otherwise a search issued from
    // page 3 returns an empty list and reads as "no matches".
    const resetOffset = patch.offset === undefined;
    query.value = {
      ...query.value,
      ...patch,
      ...(resetOffset ? { offset: 0 } : {}),
    };
    return listSessions(true);
  }

  async function loadSession(id: number, force = false): Promise<void> {
    if (!force && detail.value?.session.id === id) return;
    const pending = detailInflight.get(id);
    if (pending) return pending;
    detailLoading.value = true;
    const req = (async () => {
      try {
        const res = await apiGet<{
          session: CaptureSessionRow;
          frames: CaptureFrame[];
          frame_stats: CaptureFrameStat[];
        }>(`/api/capture/sessions/${id}`);
        detail.value = {
          session: res.session,
          frames: res.frames ?? [],
          frameStats: res.frame_stats ?? [],
        };
        error.value = "";
      } catch (e) {
        error.value = e instanceof Error ? e.message : String(e);
        detail.value = null;
      } finally {
        detailLoading.value = false;
        detailInflight.delete(id);
      }
    })();
    detailInflight.set(id, req);
    return req;
  }

  async function loadConditions(id: number, force = false): Promise<void> {
    if (!force && conditions.value && detail.value?.session.id === id) return;
    const pending = conditionsInflight.get(id);
    if (pending) return pending;
    conditionsLoading.value = true;
    const req = (async () => {
      try {
        const res = await apiGet<{
          conditions: CaptureConditionRow[];
          forecasts: CaptureForecastRow[];
          summary: unknown;
          message?: string;
        }>(`/api/capture/sessions/${id}/conditions`);
        conditions.value = {
          conditions: res.conditions ?? [],
          forecasts: res.forecasts ?? [],
          summary: hasSummary(res.summary) ? res.summary : null,
          message: res.message ?? "",
        };
      } catch (e) {
        error.value = e instanceof Error ? e.message : String(e);
        conditions.value = null;
      } finally {
        conditionsLoading.value = false;
        conditionsInflight.delete(id);
      }
    })();
    conditionsInflight.set(id, req);
    return req;
  }

  // clearDetail lets the detail route drop the previous session's data before the new one arrives, so
  // the page never shows one session's frames under another's title.
  function clearDetail(): void {
    detail.value = null;
    conditions.value = null;
  }

  // Finish a night that stopped early. The engine works out what is still owed from the frames this
  // session actually recorded and starts the remainder with the same request — same folder, same
  // optics, same pointing — so the resumed frames stack with the ones already there.
  //
  // It returns the new session's id: resuming creates a NEW row, because the old night happened and
  // rewriting it to look like it ran until morning would be a lie the logbook then repeats forever.
  async function resumeSession(
    id: number,
  ): Promise<{ sessionId: number; remaining: number; warning?: string }> {
    const res = await apiPost<{
      progress: { session_id: number };
      remaining_frames: number;
      warning?: string;
    }>(`/api/capture/sessions/${id}/resume`, {});
    return {
      sessionId: res.progress.session_id,
      remaining: res.remaining_frames,
      warning: res.warning,
    };
  }

  return {
    sessions,
    total,
    loading,
    error,
    query,
    hasMore,
    detail,
    conditions,
    detailLoading,
    conditionsLoading,
    listSessions,
    setQuery,
    loadSession,
    loadConditions,
    clearDetail,
    resumeSession,
  };
});
