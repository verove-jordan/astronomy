import { ref, onUnmounted } from "vue";
import { eventsUrl } from "@/services/api";
import type {
  LogLine,
  IterationRecord,
  PhotomRecord,
  StagePreview,
} from "@/types";

interface JobEvent {
  job_id: number;
  status: string;
  progress: number;
  step: string;
  line?: string;
  ts?: number; // wall-clock ms a log line was captured
  preview?: string;
  session?: string; // capture night ("YYYY-MM-DD") a per-session sub-step event belongs to
  photom?: PhotomRecord; // one group's photometric-normalization record, streamed as measured
  rss_bytes?: number; // live resident memory of the whole engine tree (engine + every tool)
  cpu_percent?: number; // live CPU usage (100 == one core)
  peak_rss_bytes?: number; // job-wide peak engine memory
  cpu_cores?: number; // host core count (context for cpu_percent)
  bytes_done?: number; // S3 transfer: bytes copied so far
  bytes_total?: number; // S3 transfer: total bytes to copy
  bytes_per_sec?: number; // S3 transfer: smoothed throughput (débit)
  iteration?: IterationRecord; // one supervised-finish pass, streamed as it completes
  stage_preview?: StagePreview; // one saved processing-milestone preview, streamed as it is produced
  stage_previews?: StagePreview[]; // the accumulated milestones, sent once by the reconnect snapshot
  done?: boolean;
}

// Keep a generous tail so a long step's history survives in the browser (matches the backend cap).
const MAX_LINES = 5000;

// parseLogRow turns a persisted "<ms>|<line>" row back into a LogLine. Rows without a numeric
// timestamp prefix (legacy jobs) keep the whole text with a null timestamp.
function parseLogRow(row: string): LogLine {
  const i = row.indexOf("|");
  if (i > 0) {
    const head = row.slice(0, i);
    if (/^\d+$/.test(head)) return { ts: Number(head), text: row.slice(i + 1) };
  }
  return { ts: null, text: row };
}

// useJobStream subscribes to a job's SSE stream and exposes reactive progress, a capped ring of
// timestamped log lines, the latest preview, and the running step's live resource usage. seed()
// pre-fills the log from a persisted log_tail so a page refresh keeps the history.
// autoConnect=false skips opening the stream at setup: a page that first fetches the job and finds
// it already finished has nothing to stream — its state is fully in the fetched row — so it must
// not spend (or wait on) an SSE connection at all; call reconnect() only for a live job.
export function useJobStream(
  jobId: number,
  onDone?: () => void,
  autoConnect = true,
) {
  const progress = ref(0);
  const step = ref("");
  const status = ref("queued");
  const done = ref(false);
  const lines = ref<LogLine[]>([]);
  const preview = ref("");
  const rssBytes = ref(0);
  const cpuPercent = ref(0);
  const peakRssBytes = ref(0);
  const cpuCores = ref(0);
  // S3 transfer byte progress + throughput (0 for non-transfer jobs, which never send these fields).
  const bytesDone = ref(0);
  const bytesTotal = ref(0);
  const bytesPerSec = ref(0);
  // Supervised-finish iterations accumulated live (upsert by index, so the winner's re-emit with
  // chosen=true updates its card in place). Empty for non-supervised runs.
  const iterations = ref<IterationRecord[]>([]);
  // Processing-milestone previews accumulated live (upsert by identity → the ordered timeline).
  const stagePreviews = ref<StagePreview[]>([]);
  // Per-group photometric-normalization records streamed live (cross-session runs only).
  const photomRecords = ref<PhotomRecord[]>([]);
  // The capture night the run is currently working on ("" between per-session sub-steps).
  const currentSession = ref("");

  // Monotonic id stamped on every log line so the console keeps a stable :key when the ring buffer trims.
  let seq = 0;

  function seed(initial: string[]) {
    lines.value = initial
      .filter((l) => l.length > 0)
      .map((row) => ({ ...parseLogRow(row), seq: seq++ }));
  }

  // Milestone identity: stage + filter + session. Upserting by index alone silently overwrites a card
  // whenever two producers pick the same ordinal (parallel channels, per-session pairs) — the
  // composite key keeps every distinct milestone while re-emits still update in place; `index` stays
  // the ORDER, not the identity.
  const stageKey = (sp: StagePreview) =>
    `${sp.stage}|${sp.filter ?? ""}|${sp.session ?? ""}`;

  // upsert a milestone preview. Fed by both the live singular `stage_preview` events and the
  // reconnect snapshot's `stage_previews` array, so a page reloaded mid-run restores the whole timeline.
  function upsertStage(sp: StagePreview) {
    const key = stageKey(sp);
    const at = stagePreviews.value.findIndex((x) => stageKey(x) === key);
    if (at >= 0) stagePreviews.value[at] = sp;
    else stagePreviews.value.push(sp);
  }

  // upsert a photom record by (filter-bearing label, session) — a group re-measure updates in place.
  function upsertPhotom(rec: PhotomRecord) {
    const at = photomRecords.value.findIndex(
      (x) => x.label === rec.label && (x.session ?? "") === (rec.session ?? ""),
    );
    if (at >= 0) photomRecords.value[at] = rec;
    else photomRecords.value.push(rec);
  }

  // The stream closes itself when the job reaches a done/paused event. reconnect() re-opens it — used
  // after Continue, so a resumed (previously paused) run streams live progress again without a remount.
  let source: EventSource | null = null;
  function connect() {
    source?.close();
    done.value = false;
    const es = new EventSource(eventsUrl(jobId));
    source = es;
    es.onmessage = handleMessage(es);
    es.onerror = () => {
      // The browser auto-reconnects; nothing to do.
    };
  }

  const handleMessage = (es: EventSource) => (ev: MessageEvent<string>) => {
    const e = JSON.parse(ev.data) as JobEvent;
    progress.value = e.progress;
    step.value = e.step;
    status.value = e.status;
    if (e.line) {
      lines.value.push({ ts: e.ts ?? null, text: e.line, seq: seq++ });
      if (lines.value.length > MAX_LINES)
        lines.value.splice(0, lines.value.length - MAX_LINES);
    }
    // A resource reading always carries a real rss_bytes (the backend skips all-zero samples), so
    // its presence is an unambiguous "this is a resource event" marker. cpu_percent/peak may be
    // omitted by omitempty when zero/equal, so default them rather than reset the readout on a line.
    if (e.rss_bytes !== undefined) {
      rssBytes.value = e.rss_bytes;
      cpuPercent.value = e.cpu_percent ?? 0;
      peakRssBytes.value = e.peak_rss_bytes ?? e.rss_bytes;
      if (e.cpu_cores) cpuCores.value = e.cpu_cores;
    }
    // A paused job's engine sampler is gone: zero the live readings (the peak is history, keep it)
    // so the header doesn't freeze on the last pre-pause sample.
    if (e.status === "paused") {
      rssBytes.value = 0;
      cpuPercent.value = 0;
    }
    // Transfer byte progress. bytes_total marks a transfer event; bytes_done/bytes_per_sec are omitted
    // when zero (omitempty), so default them rather than leave a stale value from an earlier tick.
    if (e.bytes_total !== undefined) {
      bytesTotal.value = e.bytes_total;
      bytesDone.value = e.bytes_done ?? 0;
      bytesPerSec.value = e.bytes_per_sec ?? 0;
    }
    if (e.iteration) {
      const at = iterations.value.findIndex(
        (it) => it.index === e.iteration!.index,
      );
      if (at >= 0) iterations.value[at] = e.iteration;
      else iterations.value.push(e.iteration);
    }
    if (e.session !== undefined) currentSession.value = e.session;
    if (e.photom) upsertPhotom(e.photom);
    if (e.stage_preview) upsertStage(e.stage_preview);
    if (e.stage_previews) e.stage_previews.forEach(upsertStage);
    if (e.preview) preview.value = e.preview;
    if (e.done) {
      done.value = true;
      es.close();
      onDone?.();
    }
  };

  if (autoConnect) connect();
  onUnmounted(() => source?.close());

  return {
    progress,
    step,
    status,
    done,
    lines,
    preview,
    rssBytes,
    cpuPercent,
    peakRssBytes,
    cpuCores,
    bytesDone,
    bytesTotal,
    bytesPerSec,
    iterations,
    stagePreviews,
    photomRecords,
    currentSession,
    seed,
    reconnect: connect,
  };
}
