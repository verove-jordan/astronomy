import { computed, onBeforeUnmount, ref, watch, type Ref } from "vue";

/**
 * The time machine.
 *
 * One reactive instant, advanced on the animation frame by a rate expressed as simulated
 * milliseconds per real millisecond. Everything the scene draws is a function of this number, so
 * running, scrubbing, stepping and jumping to an exact date are all the same operation — nothing in
 * the renderer knows the difference between playing and being dragged.
 *
 * The rate is signed, so time runs backwards by setting it negative rather than by a separate mode.
 */

/**
 * RATES are the speeds the readout names, in simulated milliseconds per REAL MILLISECOND.
 *
 * The unit is the whole of the arithmetic here and it is easy to get wrong by three orders of
 * magnitude: "a day per second" is 86 400 000 simulated milliseconds crossed in 1 000 real ones,
 * which is 86 400 — not 86 400 000. Quoting the per-second span directly is what makes the label
 * a lie the first time someone watches Jupiter cross its whole orbit in a blink.
 */
export const RATES = [
  { key: "realtime", rate: 1 },
  { key: "minute", rate: 60 },
  { key: "hour", rate: 3_600 },
  { key: "day", rate: 86_400 },
  { key: "week", rate: 604_800 },
  { key: "month", rate: 2_592_000 },
  { key: "year", rate: 31_557_600 },
] as const;

export type RateKey = (typeof RATES)[number]["key"];

/** The slowest and fastest the slider reaches — real time, up to a century per second. */
export const MIN_RATE = 1;
export const MAX_RATE = 3_155_760_000;

export interface SimClock {
  /** The simulated instant, as a Unix timestamp in milliseconds. */
  timeMs: Ref<number>;
  /** Simulated milliseconds per real millisecond. Negative runs time backwards. */
  rate: Ref<number>;
  playing: Ref<boolean>;
  /** True when the clock is running at real time, forwards — the "now" everyone means. */
  live: Ref<boolean>;
  play(): void;
  pause(): void;
  toggle(): void;
  /** Jump to an instant and stop following the wall clock. */
  seek(ms: number): void;
  /** Return to the present and resume real time. */
  now(): void;
  /** Move by a fixed span without changing the rate — what the arrow keys and the step buttons do. */
  step(deltaMs: number): void;
}

export interface SimClockOptions {
  /** Refuse instants outside this span rather than showing positions nothing stands behind. */
  min?: Ref<number> | number;
  max?: Ref<number> | number;
  /** Called whenever the instant changes, so a renderer can mark itself dirty. */
  onChange?: () => void;
}

function unref(v: Ref<number> | number | undefined, fallback: number): number {
  if (v === undefined) return fallback;
  return typeof v === "number" ? v : v.value;
}

export function useSimClock(options: SimClockOptions = {}): SimClock {
  const timeMs = ref(Date.now());
  const rate = ref(1);
  const playing = ref(true);
  // followWall is what makes "now" mean now: at real time the clock reads the system clock rather
  // than accumulating frame deltas, so leaving the tab for an hour and coming back does not leave
  // the scene an hour in the past.
  const followWall = ref(true);

  const live = computed(
    () => playing.value && followWall.value && rate.value === 1,
  );

  let raf = 0;
  let last = 0;

  function clamp(ms: number): number {
    return Math.min(
      unref(options.max, Number.POSITIVE_INFINITY),
      Math.max(unref(options.min, Number.NEGATIVE_INFINITY), ms),
    );
  }

  function set(ms: number) {
    const next = clamp(ms);
    if (next === timeMs.value) return;
    timeMs.value = next;
    options.onChange?.();
  }

  function tick(now: number) {
    raf = requestAnimationFrame(tick);
    if (!playing.value) {
      last = now;
      return;
    }
    if (followWall.value && rate.value === 1) {
      set(Date.now());
      last = now;
      return;
    }
    const dt = last ? now - last : 0;
    last = now;
    if (dt > 0) {
      // A tab that was hidden hands back one enormous delta on its first frame. Clamping it keeps a
      // return from a background tab from launching the scene a decade into the future.
      set(timeMs.value + Math.min(dt, 250) * rate.value);
    }
  }

  function play() {
    playing.value = true;
    last = 0;
  }
  function pause() {
    playing.value = false;
  }
  function toggle() {
    if (playing.value) pause();
    else play();
  }
  function seek(ms: number) {
    followWall.value = false;
    set(ms);
  }
  function now() {
    followWall.value = true;
    rate.value = 1;
    playing.value = true;
    set(Date.now());
  }
  function step(deltaMs: number) {
    followWall.value = false;
    set(timeMs.value + deltaMs);
  }

  // Leaving real time is what any non-unit rate means; there is no separate mode to keep in sync.
  watch(rate, (r) => {
    if (r !== 1) followWall.value = false;
    last = 0;
  });

  raf = requestAnimationFrame(tick);
  onBeforeUnmount(() => cancelAnimationFrame(raf));

  return { timeMs, rate, playing, live, play, pause, toggle, seek, now, step };
}
