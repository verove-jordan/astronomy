import { ref, onUnmounted } from "vue";
import { eventsUrl } from "@/services/api";
import type { LogLine, IterationRecord, StagePreview } from "@/types";

interface JobEvent {
  job_id: number;
  status: string;
  progress: number;
  step: string;
  line?: string;
  ts?: number; // wall-clock ms a log line was captured
  preview?: string;
  rss_bytes?: number; // live resident memory of the running step's subprocess
  cpu_percent?: number; // live CPU usage (100 == one core)
  peak_rss_bytes?: number; // peak resident memory seen this step
  iteration?: IterationRecord; // one supervised-finish pass, streamed as it completes
  stage_preview?: StagePreview; // one saved processing-milestone preview, streamed as it is produced
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
export function useJobStream(jobId: number, onDone?: () => void) {
  const progress = ref(0);
  const step = ref("");
  const status = ref("queued");
  const done = ref(false);
  const lines = ref<LogLine[]>([]);
  const preview = ref("");
  const rssBytes = ref(0);
  const cpuPercent = ref(0);
  const peakRssBytes = ref(0);
  // Supervised-finish iterations accumulated live (upsert by index, so the winner's re-emit with
  // chosen=true updates its card in place). Empty for non-supervised runs.
  const iterations = ref<IterationRecord[]>([]);
  // Processing-milestone previews accumulated live (upsert by index → the ordered timeline).
  const stagePreviews = ref<StagePreview[]>([]);

  function seed(initial: string[]) {
    lines.value = initial.filter((l) => l.length > 0).map(parseLogRow);
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
      lines.value.push({ ts: e.ts ?? null, text: e.line });
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
    }
    if (e.iteration) {
      const at = iterations.value.findIndex(
        (it) => it.index === e.iteration!.index,
      );
      if (at >= 0) iterations.value[at] = e.iteration;
      else iterations.value.push(e.iteration);
    }
    if (e.stage_preview) {
      const at = stagePreviews.value.findIndex(
        (sp) => sp.index === e.stage_preview!.index,
      );
      if (at >= 0) stagePreviews.value[at] = e.stage_preview;
      else stagePreviews.value.push(e.stage_preview);
    }
    if (e.preview) preview.value = e.preview;
    if (e.done) {
      done.value = true;
      es.close();
      onDone?.();
    }
  };

  connect();
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
    iterations,
    stagePreviews,
    seed,
    reconnect: connect,
  };
}
