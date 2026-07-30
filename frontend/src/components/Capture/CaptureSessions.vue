<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost } from "@/constants/styles";
import { useCaptureStore } from "@/stores/capture";
import type { CaptureSessionRow } from "@/types";

// Auto-runs: the one in flight, and the ones already shot.
//
// The runner shows the run you just launched, but nothing showed the night's history — so after a
// browser reload, or on the second target, there was no way to answer "did that L set finish?" or "how
// many frames did I get before the cloud came in". The rows persist in Postgres, which is what makes
// them survive a reload; the live one is refreshed from the same SSE progress the runner uses.
const { t } = useI18n();
const store = useCaptureStore();

const REFRESH_MS = 5000;
let timer = 0;

onMounted(() => {
  void store.loadSessions();
  timer = window.setInterval(
    () => void store.loadSessions().catch(() => {}),
    REFRESH_MS,
  );
});
onBeforeUnmount(() => window.clearInterval(timer));

// A finished frame changes the counts, so reload promptly rather than waiting for the next tick.
watch(
  () => store.progress?.frame_index,
  () => void store.loadSessions().catch(() => {}),
);

const ACTIVE = new Set(["running", "paused"]);

// Active first, then newest: the run you care about is never buried.
const rows = computed(() =>
  [...store.sessions].sort((a, b) => {
    const aActive = ACTIVE.has(a.status) ? 1 : 0;
    const bActive = ACTIVE.has(b.status) ? 1 : 0;
    return bActive - aActive || b.started_at - a.started_at;
  }),
);

const isActive = (s: CaptureSessionRow) => ACTIVE.has(s.status);

// The live session's counters come from the progress stream, which is seconds fresher than the
// five-second poll — otherwise the active row visibly lags the runner right above it.
function framesDone(s: CaptureSessionRow): number {
  if (isActive(s) && store.progress?.session_id === s.id) {
    return store.progress.frame_index;
  }
  return s.frames_done;
}

function totalFrames(s: CaptureSessionRow): number {
  if (isActive(s) && store.progress?.session_id === s.id) {
    return store.progress.total_frames || s.total_frames;
  }
  return s.total_frames;
}

function percent(s: CaptureSessionRow): number {
  const total = totalFrames(s);
  return total > 0 ? Math.min(100, (framesDone(s) / total) * 100) : 0;
}

// Per-filter tallies, the thing you actually plan the rest of the night from.
function perFilter(s: CaptureSessionRow): [string, number][] {
  const live =
    isActive(s) && store.progress?.session_id === s.id
      ? store.progress.captured
      : s.progress?.captured;
  return Object.entries(live ?? {}).filter(([, n]) => n > 0);
}

function elapsed(s: CaptureSessionRow): string {
  if (!s.started_at) return "";
  const end = s.ended_at || Date.now();
  const mins = Math.max(0, Math.round((end - s.started_at) / 60000));
  if (mins < 60) return `${mins} min`;
  return `${Math.floor(mins / 60)} h ${String(mins % 60).padStart(2, "0")}`;
}

const started = (ms: number) =>
  ms
    ? new Date(ms).toLocaleString(undefined, {
        dateStyle: "short",
        timeStyle: "short",
      })
    : "";

const statusClass = (status: string) =>
  ({
    running: "text-brand-600 dark:text-brand-300",
    paused: "text-amber-600 dark:text-amber-400",
    completed: "text-emerald-600 dark:text-emerald-400",
    failed: "text-danger-500",
    aborted: "text-slate-500",
    interrupted: "text-amber-600 dark:text-amber-400",
  })[status] ?? "text-slate-500";
</script>

<template>
  <div class="space-y-2">
    <div class="flex items-center justify-between">
      <p class="text-[11px] text-slate-500 dark:text-slate-400">
        {{ t("capture.sessions.blurb") }}
      </p>
      <button
        :class="btnGhost"
        class="!px-2 !py-0.5 text-xs"
        @click="store.loadSessions()"
      >
        {{ t("common.refresh") }}
      </button>
    </div>

    <p v-if="!rows.length" class="text-xs text-slate-400">
      {{ t("capture.sessions.none") }}
    </p>

    <ul v-else class="space-y-2">
      <li
        v-for="s in rows"
        :key="s.id"
        class="rounded-md border p-2"
        :class="
          isActive(s)
            ? 'border-brand-500/60 bg-brand-500/5'
            : 'border-slate-200 dark:border-slate-700'
        "
      >
        <div
          class="flex flex-wrap items-baseline justify-between gap-x-2 text-xs"
        >
          <span class="font-semibold text-slate-700 dark:text-slate-200">
            {{ s.object || t("capture.sessions.untitled") }}
            <span v-if="s.panel" class="font-mono text-slate-400">{{
              s.panel
            }}</span>
          </span>
          <span :class="statusClass(s.status)">{{
            t(`capture.status.${s.status}`)
          }}</span>
        </div>

        <div class="mt-1 flex items-center gap-2">
          <span
            class="h-1.5 flex-1 overflow-hidden rounded bg-slate-200 dark:bg-slate-700"
          >
            <span
              class="block h-full rounded transition-[width] duration-300"
              :class="
                isActive(s) ? 'bg-brand-500' : 'bg-slate-400 dark:bg-slate-500'
              "
              :style="{ width: percent(s) + '%' }"
            />
          </span>
          <span class="font-mono text-[11px] tabular-nums text-slate-500">
            {{ framesDone(s) }}/{{ totalFrames(s) }}
          </span>
        </div>

        <div
          class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-slate-500 dark:text-slate-400"
        >
          <span v-for="[f, n] in perFilter(s)" :key="f" class="font-mono">
            {{ f }} {{ n }}
          </span>
          <span v-if="elapsed(s)">· {{ elapsed(s) }}</span>
          <span v-if="s.started_at" class="text-slate-400"
            >· {{ started(s.started_at) }}</span
          >
        </div>

        <p v-if="s.progress?.error" class="mt-1 text-[11px] text-danger-500">
          {{ s.progress.error }}
        </p>
      </li>
    </ul>
  </div>
</template>
