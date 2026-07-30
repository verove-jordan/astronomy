import { computed, onBeforeUnmount, ref, type Ref } from "vue";

// A ticking countdown to a deadline, for "when does the next frame land?".
//
// It exists because a long sub makes the capture screen look broken: nothing moves for five minutes,
// and there is no way to tell "still integrating" from "the camera has died". A second-by-second
// counter is the difference between waiting and worrying.
//
// The tick is 250 ms rather than 1000 ms so the displayed second changes promptly after the deadline
// is replaced — a 1 s tick can leave a stale number on screen for most of a second, which reads as a
// stutter. The value is still rendered in whole seconds.
const TICK_MS = 250;

export interface Countdown {
  // remainingMs is time left, clamped at 0. Null when there is no deadline.
  remainingMs: Ref<number | null>;
  // seconds is remainingMs rounded UP: a countdown should read "1" for the whole final second and
  // reach "0" only when the time is actually up.
  seconds: Ref<number | null>;
  // label is a compact "M:SS" for anything a minute or longer, plain seconds below that.
  label: Ref<string>;
  // progress is 0→1 through the exposure, for a bar. Null without a known duration.
  progress: Ref<number | null>;
}

// useCountdown watches an ISO-8601 deadline and an optional total duration in microseconds.
//
// deadline and totalUs are getters rather than values so the caller can pass reactive store fields
// without this composable knowing where they came from.
export function useCountdown(
  deadline: () => string | null | undefined,
  totalUs: () => number | null | undefined = () => null,
): Countdown {
  const now = ref(Date.now());
  const timer = window.setInterval(() => {
    now.value = Date.now();
  }, TICK_MS);
  onBeforeUnmount(() => window.clearInterval(timer));

  const targetMs = computed(() => {
    const iso = deadline();
    if (!iso) return null;
    const t = Date.parse(iso);
    return Number.isFinite(t) ? t : null;
  });

  const remainingMs = computed(() => {
    const t = targetMs.value;
    if (t === null) return null;
    return Math.max(0, t - now.value);
  });

  const seconds = computed(() => {
    const ms = remainingMs.value;
    return ms === null ? null : Math.ceil(ms / 1000);
  });

  const label = computed(() => {
    const s = seconds.value;
    if (s === null) return "";
    if (s < 60) return `${s}s`;
    const m = Math.floor(s / 60);
    return `${m}:${String(s % 60).padStart(2, "0")}`;
  });

  const progress = computed(() => {
    const total = totalUs();
    const ms = remainingMs.value;
    if (!total || total <= 0 || ms === null) return null;
    const totalMs = total / 1000;
    // Clamped because the deadline is deliberately a little generous (it includes readout), so a fast
    // frame can arrive with time still on the clock.
    return Math.min(1, Math.max(0, 1 - ms / totalMs));
  });

  return { remainingMs, seconds, label, progress };
}
