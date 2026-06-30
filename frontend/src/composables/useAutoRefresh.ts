import { onBeforeUnmount, ref, watch } from "vue";

// useAutoRefresh runs `cb` every `ms` milliseconds while `enabled` is true, skipping ticks when the
// tab is hidden, and cleans up on unmount. Generic enough to reuse beyond the Tonight page.
export function useAutoRefresh(cb: () => unknown, ms: number) {
  const enabled = ref(false);
  let timer: ReturnType<typeof setInterval> | null = null;

  function tick() {
    if (typeof document !== "undefined" && document.hidden) return;
    void cb();
  }
  function stop() {
    if (timer !== null) {
      clearInterval(timer);
      timer = null;
    }
  }
  function start() {
    stop();
    timer = setInterval(tick, ms);
  }

  watch(enabled, (on) => (on ? start() : stop()));
  onBeforeUnmount(stop);

  return { enabled };
}
