import { onUnmounted, ref, type Ref } from "vue";

// useNowTicker returns a shared "now" (epoch ms) ref that ticks every `ms` while the tab is
// visible, and immediately on tab return — so live alt/az readouts and meridian countdowns update
// without each consumer running its own timer. Skipping hidden-tab ticks mirrors useAutoRefresh.
export function useNowTicker(ms = 30_000): Ref<number> {
  const now = ref(Date.now());
  const tick = () => {
    if (!document.hidden) now.value = Date.now();
  };
  const id = window.setInterval(tick, ms);
  document.addEventListener("visibilitychange", tick);
  onUnmounted(() => {
    window.clearInterval(id);
    document.removeEventListener("visibilitychange", tick);
  });
  return now;
}
