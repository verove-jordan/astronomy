import { ref, onUnmounted } from "vue";
import { eventsUrl } from "@/services/api";

interface JobEvent {
  job_id: number;
  status: string;
  progress: number;
  step: string;
  line?: string;
  preview?: string;
  done?: boolean;
}

// useJobStream subscribes to a job's SSE stream and exposes reactive progress, a capped ring of
// recent log lines, and the latest preview path. seed() pre-fills the log from a persisted log_tail.
export function useJobStream(jobId: number, onDone?: () => void) {
  const progress = ref(0);
  const step = ref("");
  const status = ref("queued");
  const done = ref(false);
  const lines = ref<string[]>([]);
  const preview = ref("");

  function seed(initial: string[]) {
    lines.value = initial.filter((l) => l.length > 0);
  }

  const source = new EventSource(eventsUrl(jobId));
  source.onmessage = (ev: MessageEvent<string>) => {
    const e = JSON.parse(ev.data) as JobEvent;
    progress.value = e.progress;
    step.value = e.step;
    status.value = e.status;
    if (e.line) {
      lines.value.push(e.line);
      if (lines.value.length > 1000)
        lines.value.splice(0, lines.value.length - 1000);
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

  return { progress, step, status, done, lines, preview, seed };
}
