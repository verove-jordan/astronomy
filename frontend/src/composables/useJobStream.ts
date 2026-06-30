import { ref, onUnmounted } from "vue";
import { eventsUrl } from "@/services/api";
import type { LogLine } from "@/types";

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

  function seed(initial: string[]) {
    lines.value = initial.filter((l) => l.length > 0).map(parseLogRow);
  }

  const source = new EventSource(eventsUrl(jobId));
  source.onmessage = (ev: MessageEvent<string>) => {
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
    if (e.preview) preview.value = e.preview;
    if (e.done) {
      done.value = true;
      source.close();
      onDone?.();
    }
  };
  source.onerror = () => {
    // The browser auto-reconnects; nothing to do.
  };

  onUnmounted(() => source.close());

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
    seed,
  };
}
